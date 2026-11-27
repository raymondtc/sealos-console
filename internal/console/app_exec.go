package console

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
)

const appExecOutputLimit = 8 << 20

type appExecResult struct {
	target Object
	data   []byte
	code   int
}

func (r *appExecResult) write(out io.Writer, asJSON bool) error {
	var err error
	if asJSON {
		r.target["executed"], r.target["exitCode"], r.target["output"] = true, r.code, string(r.data)
		err = json.NewEncoder(out).Encode(r.target)
	} else {
		_, err = out.Write(r.data)
	}
	if err != nil {
		return fail("OUTPUT", "Could not write remote command output.")
	}
	return nil
}

func (s *Session) appExec(app string, o options) (any, error) {
	if o.execute && o.dryRun {
		return nil, fail("INPUT", "Use either --dry-run or --execute.")
	}
	if len(o.command) == 0 || o.command[0] == "" || o.timeout == 0 {
		return nil, fail("INPUT", "Supply a command after -- and use a positive --timeout.")
	}
	ops := Operations{Session: s, Namespace: s.Namespace}
	pod, err := ops.selectedPod("app", app, o.pod)
	if err != nil {
		return nil, err
	}
	name := podName(pod)
	if !dnsName(name) || !dnsName(s.Namespace) {
		return nil, fail("PROTOCOL", "Pod or namespace name is invalid.")
	}
	container, err := selectAppExecContainer(pod, o.container)
	if err != nil {
		return nil, err
	}
	if _, err := s.providerBase("terminal"); err != nil {
		return nil, err
	}
	nonce, err := devboxNonce()
	if err != nil {
		return nil, err
	}
	line, err := terminalExecInput(s.Namespace, name, container, nonce, o.command)
	if err != nil {
		return nil, err
	}
	target := Object{"operation": "app.exec", "app": app, "namespace": s.Namespace, "pod": name, "container": container,
		"argumentCount": len(o.command), "transport": "sealos-terminal", "executed": false}
	if !o.execute {
		target["mayStartTerminal"] = true
		return target, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(o.timeout)*time.Second)
	defer cancel()
	endpoint, err := s.openAppTerminal(ctx)
	if err != nil {
		return nil, err
	}
	result, err := s.runAppTerminal(ctx, endpoint, nonce, line)
	if err != nil {
		return nil, err
	}
	result.target = target
	return result, nil
}

func selectAppExecContainer(pod Object, name string) (string, error) {
	containers := arr(at(pod, "spec", "containers"))
	if name == "" {
		if len(containers) != 1 {
			return "", fail("INPUT", "Specify --container when the Pod does not have exactly one regular container.")
		}
		name = str(at(containers[0], "name"))
	}
	matches := 0
	for _, container := range containers {
		if at(container, "name") == name {
			matches++
		}
	}
	if matches != 1 || !dnsName(name) || strings.Contains(name, ".") {
		return "", fail("INPUT", "Container is absent or ambiguous in the selected Pod.")
	}
	return name, nil
}

func terminalExecInput(namespace, pod, container, nonce string, command []string) ([]byte, error) {
	args := append([]string{"kubectl", "-n", namespace, "exec", pod, "-c", container, "--"}, command...)
	quoted := make([]string, len(args))
	for i, arg := range args {
		if strings.ContainsRune(arg, 0) {
			return nil, fail("INPUT", "Command arguments cannot contain NUL.")
		}
		quoted[i] = "'" + strings.ReplaceAll(arg, "'", "'\\''") + "'"
	}
	// This wrapper runs in the managed Terminal. kubectl sends the caller's
	// argv directly to the target container, with neither stdin nor a TTY.
	script := "stty -opost </dev/tty 2>/dev/null || true\n" +
		"printf '\\036%s\\037' '" + nonce + "-start'\n" +
		strings.Join(quoted, " ") + "\n" +
		"code=$?\nprintf '\\036%s:%s\\037' '" + nonce + "-exit' \"$code\"\n"
	line := "0 printf '%s' '" + base64.StdEncoding.EncodeToString([]byte(script)) + "' | base64 -d | sh; exit\r"
	// ttyd's interactive Terminal starts with a canonical PTY input line.
	if len(line) > 3800 {
		return nil, fail("INPUT", "Command arguments exceed the Terminal input limit.")
	}
	return []byte(line), nil
}

func (s *Session) openAppTerminal(ctx context.Context) (string, error) {
	base, err := s.providerBase("terminal")
	if err != nil {
		return "", err
	}
	for {
		resp, err := s.HTTP.requestContext(ctx, s.HTTP.Client, base, "POST", "/api/apply", s.kubeconfig, "", nil)
		if err != nil {
			if ctx.Err() != nil {
				return "", fail("WAIT_TIMEOUT", "Sealos Terminal did not become ready before timeout.")
			}
			return "", err
		}
		response, err := decodeResponse(resp, "POST", "/api/apply", false)
		resp.Body.Close()
		if err != nil {
			return "", err
		}
		switch at(response, "code") {
		case float64(200):
			endpoint := str(at(response, "data"))
			if endpoint == "" {
				return "", fail("PROTOCOL", "Sealos Terminal returned no session URL.")
			}
			return endpoint, nil
		case float64(201):
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return "", fail("WAIT_TIMEOUT", "Sealos Terminal did not become ready before timeout.")
			case <-timer.C:
			}
		default:
			return "", fail("API_ERROR", "Sealos Terminal could not start the session.")
		}
	}
}

func (s *Session) terminalSocket(endpoint string) (socket, origin, auth string, err error) {
	u, e := url.Parse(endpoint)
	if e != nil || u.User != nil || u.Host == "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		err = fail("PROTOCOL", "Sealos Terminal returned an invalid session URL.")
		return
	}
	origin, e = originFor(u.Scheme+"://"+u.Host, s.Profile.LocalTest)
	root, _ := url.Parse(s.Profile.URL)
	if e != nil || (s.Profile.LocalTest && origin != s.Profile.URL) ||
		(!s.Profile.LocalTest && (root == nil || root.Hostname() == "" ||
			(u.Hostname() != root.Hostname() && !strings.HasSuffix(u.Hostname(), "."+root.Hostname())))) {
		err = fail("CONFIG", "Sealos Terminal session URL is outside the platform origin.")
		return
	}
	params, e := url.ParseQuery(u.RawQuery)
	values := params["authorization"]
	if e != nil || len(params) != 1 || len(values) != 1 {
		err = fail("PROTOCOL", "Sealos Terminal returned invalid session authorization.")
		return
	}
	auth = strings.ReplaceAll(values[0], " ", "+")
	plain, e := base64.StdEncoding.DecodeString(auth)
	if e != nil || len(auth) > 240 || !bytes.Contains(plain, []byte(":")) {
		err = fail("PROTOCOL", "Sealos Terminal did not return supported session authorization.")
		return
	}
	scheme := "wss"
	if u.Scheme == "http" {
		scheme = "ws"
	}
	socket = (&url.URL{Scheme: scheme, Host: u.Host, Path: "/ws"}).String()
	return
}

func (s *Session) runAppTerminal(ctx context.Context, endpoint, nonce string, line []byte) (*appExecResult, error) {
	socket, origin, auth, err := s.terminalSocket(endpoint)
	if err != nil {
		return nil, err
	}
	conn, resp, err := websocket.Dial(ctx, socket, &websocket.DialOptions{
		HTTPClient: s.HTTP.Client, Subprotocols: []string{"tty"},
		HTTPHeader: http.Header{"Origin": {origin}, "Authorization": {"Basic " + auth}},
	})
	if err != nil {
		if resp != nil {
			return nil, fail("TERMINAL", "Sealos Terminal WebSocket was rejected (HTTP %d).", resp.StatusCode)
		}
		return nil, fail("TERMINAL", "Could not open the Sealos Terminal WebSocket.")
	}
	defer conn.CloseNow()
	if conn.Subprotocol() != "tty" {
		return nil, fail("PROTOCOL", "Sealos Terminal did not negotiate the tty protocol.")
	}
	conn.SetReadLimit(appExecOutputLimit)
	hello, _ := json.Marshal(Object{"AuthToken": auth, "columns": 120, "rows": 30})
	if err := conn.Write(ctx, websocket.MessageBinary, hello); err != nil {
		return nil, fail("TERMINAL", "Sealos Terminal authentication frame could not be sent.")
	}
	start := []byte("\x1e" + nonce + "-start\x1f")
	exit := []byte("\x1e" + nonce + "-exit:")
	var output []byte
	queued, started := false, false
	for {
		_, frame, err := conn.Read(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil, fail("WAIT_TIMEOUT", "Command did not finish before timeout. Its remote outcome is unknown.")
			}
			return nil, fail("WRITE_UNVERIFIED", "Terminal closed before a verified command exit status.")
		}
		if len(frame) < 2 || frame[0] != '0' {
			continue
		}
		if !queued {
			if err := conn.Write(ctx, websocket.MessageBinary, line); err != nil {
				return nil, fail("WRITE_UNVERIFIED", "Could not send the Terminal command. Its remote outcome is unknown.")
			}
			queued = true
			continue
		}
		output = append(output, frame[1:]...)
		if !started {
			if i := bytes.Index(output, start); i >= 0 {
				output, started = output[i+len(start):], true
			} else if len(output) >= len(start) {
				output = append(output[:0], output[len(output)-len(start)+1:]...)
			}
		}
		if len(output) > appExecOutputLimit {
			return nil, fail("OUTPUT_LIMIT", "Command output exceeds the CLI's 8 MiB limit.")
		}
		if started {
			if i := bytes.Index(output, exit); i >= 0 {
				tail := output[i+len(exit):]
				if end := bytes.IndexByte(tail, '\x1f'); end >= 0 {
					code, err := strconv.Atoi(string(tail[:end]))
					if err != nil || code < 0 || code > 255 {
						return nil, fail("PROTOCOL", "Terminal returned an invalid remote exit status.")
					}
					return &appExecResult{data: output[:i], code: code}, nil
				}
			}
		}
	}
}
