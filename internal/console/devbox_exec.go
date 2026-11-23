package console

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"strconv"
	"strings"
	"time"
)

func devboxNonce() (string, error) {
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fail("CLIENT_ERROR", "Cannot generate DevBox request identifier.")
	}
	return "cli-" + hex.EncodeToString(random[:]), nil
}

type devboxExecStream struct {
	body       io.ReadCloser
	marker     string
	json       bool
	simulation bool
}

func (s *Session) devboxExec(name string, args []string, execute, jsonOutput bool, timeout time.Duration) (any, error) {
	if len(args) == 0 || args[0] == "" {
		return nil, fail("INPUT", "Provide a command after --.")
	}
	quoted := make([]string, len(args))
	for i, arg := range args {
		if strings.ContainsRune(arg, 0) {
			return nil, fail("INPUT", "Command arguments cannot contain NUL.")
		}
		quoted[i] = "'" + strings.ReplaceAll(arg, "'", "'\\''") + "'"
	}
	box, err := s.devboxGet(name)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(devboxState(box), "Running") {
		return nil, fail("INPUT", "The DevBox must be running to execute a command.")
	}
	if !execute {
		return Object{"operation": "devbox exec", "namespace": s.Namespace, "name": name,
			"uid": devboxID(box), "argumentCount": len(args), "workDir": "/home/devbox/project", "executed": false}, nil
	}
	if timeout <= 0 {
		return nil, fail("INPUT", "Remote command timeout must be positive.")
	}
	nonce, err := devboxNonce()
	if err != nil {
		return nil, err
	}
	marker := "__SEALOS_CONSOLE_EXIT_" + nonce + "__:"
	command := "(cd /home/devbox/project && " + strings.Join(quoted, " ") + ") 2>&1; rc=$?; printf '\\n" + marker + "%s\\n' \"$rc\"; exit \"$rc\""
	// Some provider versions clean up idePath on disconnect. Use an unused
	// per-request path, never the project directory or user-supplied data.
	body := Object{"devboxName": name, "command": command, "idePath": "/tmp/sealos-console-" + nonce}
	providers, err := s.Providers()
	if err != nil {
		return nil, err
	}
	client := *s.HTTP.Client
	client.Timeout = timeout
	resp, err := s.HTTP.request(&client, providers["devbox"], "POST", "/api/execCommandInDevboxPod", s.kubeconfig, "", body)
	if err != nil {
		return nil, err
	}
	contentType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if resp.StatusCode != 200 || (contentType != "text/event-stream" && contentType != "text/plain") {
		defer resp.Body.Close()
		if contentType == "application/json" {
			if _, err := decodeResponse(resp, "POST", "/api/execCommandInDevboxPod", true); err != nil {
				return nil, err
			}
		}
		return nil, fail("PROTOCOL", "DevBox exec did not return a command stream (HTTP %d, media type %q).", resp.StatusCode, contentType)
	}
	return &devboxExecStream{body: resp.Body, marker: marker, json: jsonOutput, simulation: s.Profile.LocalTest}, nil
}

func (stream *devboxExecStream) write(stdout io.Writer) (int, error) {
	defer stream.body.Close()
	scanner := bufio.NewScanner(stream.body)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	encoder := json.NewEncoder(stdout)
	exitCode := -1
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, stream.marker) {
			n, err := strconv.Atoi(strings.TrimPrefix(line, stream.marker))
			if err != nil || n < 0 || n > 255 || exitCode != -1 {
				return 1, fail("EXEC_UNVERIFIED", "Invalid remote exit status.")
			}
			exitCode = n
			continue
		}
		// New providers add their own status line after the command output.
		if exitCode >= 0 {
			if line == "" {
				continue
			}
			if status, ok := strings.CutPrefix(line, "__DEVBOX_EXEC_STATUS__:"); ok {
				n, err := strconv.Atoi(strings.TrimSpace(status))
				if err == nil && n == exitCode {
					continue
				}
			}
			return 1, fail("EXEC_UNVERIFIED", "Unexpected data after the remote exit status.")
		}
		var err error
		if stream.json {
			event := Object{"line": line}
			if stream.simulation {
				event["simulation"] = true
			}
			err = encoder.Encode(event)
		} else {
			_, err = io.WriteString(stdout, line+"\n")
		}
		if err != nil {
			return 1, err
		}
	}
	if scanner.Err() != nil || exitCode < 0 {
		return 1, fail("EXEC_UNVERIFIED", "Remote command stream closed without a verified exit status; inspect the DevBox before retrying.")
	}
	if stream.json {
		event := Object{"exitCode": exitCode}
		if stream.simulation {
			event["simulation"] = true
		}
		if err := encoder.Encode(event); err != nil {
			return 1, err
		}
	}
	return exitCode, nil
}
