package console

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/raymondtc/sealos-console/internal/simulator"
	"io"
	"os"
	"strings"
	"time"
)

const help = `sealos-console — Sealos console API client

Usage:
  sealos-console mock serve [--listen 127.0.0.1:8787] [--fault conflict|timeout-after-write]
  sealos-console auth login --url https://cloud.example.com --username USER --password-stdin
  sealos-console auth status | auth logout | workspace list | doctor
  sealos-console app list | app get NAME | app domains | app wait NAME
  sealos-console app create --file app.json [--execute] [--wait 120]
  sealos-console app upgrade NAME --image IMAGE [--container NAME] [--execute]
  sealos-console app pods|events|stats NAME
  sealos-console app logs NAME [--pod POD] [--container NAME] [--tail 100] [--since 1h] [--follow]
  sealos-console app metrics NAME [--metric cpu|memory] [--since 1h]
  sealos-console db pods|events|logs|metrics NAME
  sealos-console storage buckets | storage bucket NAME | storage connect [--execute]
  sealos-console storage create NAME [--execute]
  sealos-console storage ls BUCKET [--prefix PREFIX] [--limit 100] [--next-token TOKEN]
  sealos-console storage upload BUCKET KEY --file PATH [--execute]
  sealos-console storage download BUCKET KEY --output NEW_PATH
  sealos-console db list | db get NAME | db engines
  sealos-console db create --file database.json [--execute] [--wait 120]

Common flags (may appear before or after the command):
  --profile NAME       Saved login profile (default: default)
  --workspace NS_OR_UID Exact workspace; required for creation/upgrade unless saved at login
  --json               Output is JSON; followed logs emit one JSON object per line
  --local-test         Login to a loopback-only mock server (never a remote host)

Writes default to a read-only preview. --execute sends the mutation.
No CPU/configuration updates, deletion, credential export or direct Kubernetes access.
Use SEALOS_PASSWORD or --password-stdin; passwords are not accepted in command arguments.
`

type options struct {
	profile, workspace, base, username, file, image, container string
	listen, fault                                              string
	pod, since, metric, prefix, nextToken, output              string
	tail, limit                                                int
	follow, previous                                           bool
	passwordStdin, execute, json, help, version                bool
	localTest                                                  bool
	wait, timeout                                              int
}

func parse(args []string) (options, []string, error) {
	var o options
	f := flag.NewFlagSet("sealos-console", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	f.StringVar(&o.pod, "pod", "", "")
	f.StringVar(&o.since, "since", "1h", "")
	f.StringVar(&o.metric, "metric", "cpu", "")
	f.StringVar(&o.prefix, "prefix", "", "")
	f.StringVar(&o.nextToken, "next-token", "", "")
	f.StringVar(&o.output, "output", "", "")
	f.IntVar(&o.tail, "tail", 100, "")
	f.IntVar(&o.limit, "limit", 100, "")
	f.BoolVar(&o.follow, "follow", false, "")
	f.BoolVar(&o.previous, "previous", false, "")
	f.StringVar(&o.profile, "profile", "default", "")
	f.StringVar(&o.workspace, "workspace", "", "")
	f.StringVar(&o.base, "url", "", "")
	f.StringVar(&o.username, "username", os.Getenv("SEALOS_USERNAME"), "")
	f.StringVar(&o.file, "file", "", "")
	f.StringVar(&o.image, "image", "", "")
	f.StringVar(&o.container, "container", "", "")
	f.StringVar(&o.listen, "listen", "127.0.0.1:8787", "")
	f.StringVar(&o.fault, "fault", "none", "")
	f.BoolVar(&o.localTest, "local-test", false, "")
	f.BoolVar(&o.passwordStdin, "password-stdin", false, "")
	f.BoolVar(&o.execute, "execute", false, "")
	f.BoolVar(&o.json, "json", false, "")
	f.BoolVar(&o.help, "help", false, "")
	f.BoolVar(&o.help, "h", false, "")
	f.BoolVar(&o.version, "version", false, "")
	f.IntVar(&o.wait, "wait", 0, "")
	f.IntVar(&o.timeout, "timeout", 120, "")
	bools := map[string]bool{"follow": true, "previous": true, "password-stdin": true, "execute": true, "json": true, "help": true, "h": true, "version": true, "local-test": true}
	flags, pos := []string{}, []string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			key := strings.TrimLeft(a, "-")
			if j := strings.IndexByte(key, '='); j >= 0 {
				key = key[:j]
			}
			if f.Lookup(key) == nil {
				return o, nil, fail("INPUT", "Unknown option; use --help.")
			}
			flags = append(flags, a)
			if !strings.Contains(a, "=") && !bools[key] {
				i++
				if i == len(args) {
					return o, nil, fail("INPUT", "Option requires a value.")
				}
				flags = append(flags, args[i])
			}
		} else {
			pos = append(pos, a)
		}
	}
	if err := f.Parse(flags); err != nil {
		return o, nil, fail("INPUT", "Invalid option value; use --help.")
	}
	if o.wait < 0 || o.wait > 3600 || o.timeout < 0 || o.timeout > 3600 {
		return o, nil, fail("INPUT", "Wait duration must be 0–3600 seconds.")
	}
	return o, pos, nil
}
func readConfig(path string, stdin io.Reader) (Object, error) {
	if path == "" {
		return nil, fail("INPUT", "Provide --file with a JSON configuration.")
	}
	var reader io.Reader = stdin
	if path != "-" {
		f, err := os.Open(path)
		if err != nil {
			return nil, fail("INPUT", "Cannot open configuration file.")
		}
		defer f.Close()
		reader = f
	}
	b, err := io.ReadAll(io.LimitReader(reader, 4<<20))
	if err != nil {
		return nil, fail("INPUT", "Cannot read configuration.")
	}
	var m Object
	if json.Unmarshal(b, &m) != nil || m == nil {
		return nil, fail("INPUT", "Configuration must be one valid JSON object.")
	}
	return m, nil
}
func commandShape(pos []string) error {
	if len(pos) == 1 && (pos[0] == "doctor") {
		return nil
	}
	if len(pos) < 2 {
		return fail("INPUT", "A command is required; use --help.")
	}
	key := pos[0] + " " + pos[1]
	counts := map[string]int{"app pods": 3, "app logs": 3, "app events": 3, "app stats": 3, "app metrics": 3, "db pods": 3, "db logs": 3, "db events": 3, "db metrics": 3, "storage connect": 2, "storage buckets": 2, "storage bucket": 3, "storage create": 3, "storage ls": 3, "storage upload": 4, "storage download": 4, "auth login": 2, "auth status": 2, "auth logout": 2, "workspace list": 2, "app domains": 2, "app list": 2, "app get": 3, "app create": 2, "app upgrade": 3, "app wait": 3, "db list": 2, "db get": 3, "db engines": 2, "db create": 2}
	if n, ok := counts[key]; !ok || len(pos) != n {
		return fail("INPUT", "Unknown command or incorrect positional arguments; use --help.")
	}
	return nil
}

func run(o options, pos []string, stdin io.Reader) (result any, err error) {
	if err := commandShape(pos); err != nil {
		return nil, err
	}
	profiles, err := DefaultProfiles()
	if err != nil {
		return nil, fail("CONFIG", "Cannot resolve user configuration directory.")
	}
	if pos[0] == "auth" {
		switch pos[1] {
		case "login":
			password, hasEnv := os.LookupEnv("SEALOS_PASSWORD")
			if o.passwordStdin {
				line, e := bufio.NewReader(stdin).ReadString('\n')
				if e != nil && e != io.EOF {
					return nil, fail("INPUT", "Cannot read password.")
				}
				password = strings.TrimRight(line, "\r\n")
			} else if !hasEnv {
				return nil, fail("INPUT", "Provide the password via --password-stdin or SEALOS_PASSWORD, not command arguments.")
			}
			transport := NewTransport()
			transport.LocalTest = o.localTest
			s, e := Login(o.base, o.username, password, transport)
			if e != nil {
				return nil, e
			}
			if o.workspace != "" {
				if e = s.Bind(o.workspace, false); e != nil {
					return nil, e
				}
				s.Profile.Workspace = s.Namespace
			}
			if e = profiles.Save(o.profile, s.Profile); e != nil {
				return nil, e
			}
			return Object{"authenticated": true, "profile": o.profile, "url": s.Profile.URL, "workspace": s.Profile.Workspace, "simulation": s.Profile.LocalTest, "stored": "regional token only; no password or kubeconfig"}, nil
		case "logout":
			if e := profiles.Remove(o.profile); e != nil {
				return nil, fail("CONFIG", "Cannot remove local session.")
			}
			return Object{"localSessionRemoved": true, "serverSessionRevoked": false}, nil
		case "status":
			p, e := profiles.Load(o.profile)
			if e != nil {
				return nil, e
			}
			s, e := NewSession(p, nil)
			if e != nil {
				return nil, e
			}
			rows, e := s.Workspaces()
			if e != nil {
				return nil, e
			}
			return Object{"authenticated": true, "profile": o.profile, "url": p.URL, "workspace": p.Workspace, "workspaceCount": len(rows)}, nil
		}
	}
	p, err := profiles.Load(o.profile)
	if err != nil {
		return nil, err
	}
	defer func() {
		if p.LocalTest {
			if m, ok := result.(map[string]any); ok && m != nil {
				m["simulation"] = true
			}
		}
	}()
	s, err := NewSession(p, nil)
	if err != nil {
		return nil, err
	}
	if pos[0] == "workspace" {
		rows, e := s.Workspaces()
		return Object{"workspaces": rows}, e
	}
	writing := len(pos) > 1 && (pos[1] == "create" || pos[1] == "upgrade" || pos[1] == "upload" || pos[1] == "connect")
	if err = s.Bind(o.workspace, writing); err != nil {
		return nil, err
	}
	if pos[0] == "doctor" {
		return Doctor(s)
	}
	ops := Operations{Session: s, Namespace: s.Namespace}
	wait := time.Duration(o.wait) * time.Second
	if pos[0] == "storage" {
		switch pos[1] {
		case "buckets":
			rows, e := ops.Buckets()
			return Object{"namespace": s.Namespace, "items": rows}, e
		case "bucket":
			return ops.Bucket(pos[2])
		case "create":
			return ops.CreateBucket(pos[2], o.execute)
		case "connect":
			plan, e := s.ConnectStorage(o.execute)
			if e == nil && o.execute {
				p.Storage = s.Profile.Storage
				e = profiles.Save(o.profile, p)
			}
			return plan, e
		case "ls":
			return s.ListObjects(pos[2], o.prefix, o.nextToken, o.limit)
		case "upload":
			return s.Upload(pos[2], pos[3], o.file, o.execute)
		case "download":
			return s.Download(pos[2], pos[3], o.output)
		}
	}
	if pos[0] == "app" || pos[0] == "db" {
		switch pos[1] {
		case "pods":
			return ops.Pods(pos[0], pos[2])
		case "events":
			return ops.Events(pos[0], pos[2], o.pod)
		case "logs":
			return ops.Logs(pos[0], pos[2], o.pod, o.container, o.since, o.tail, o.previous, o.follow, time.Duration(o.timeout)*time.Second)
		case "metrics":
			return ops.Metrics(pos[0], pos[2], o.metric, o.since)
		case "stats":
			return ops.Stats(pos[2])
		}
	}
	switch pos[0] + " " + pos[1] {
	case "app domains":
		return ops.Domains()
	case "app list":
		return ops.Apps()
	case "app get":
		return ops.App(pos[2])
	case "app wait":
		return ops.WaitApp(pos[2], time.Duration(o.timeout)*time.Second, "", "")
	case "app create":
		raw, e := readConfig(o.file, stdin)
		if e != nil {
			return nil, e
		}
		return ops.CreateApp(raw, o.execute, wait)
	case "app upgrade":
		return ops.Upgrade(pos[2], o.image, o.container, o.execute, wait)
	case "db list":
		rows, e := ops.Databases()
		return Object{"namespace": s.Namespace, "api": "console", "items": rows}, e
	case "db get":
		return ops.DB(pos[2])
	case "db engines":
		return ops.Engines()
	case "db create":
		raw, e := readConfig(o.file, stdin)
		if e != nil {
			return nil, e
		}
		return ops.CreateDB(raw, o.execute, wait)
	}
	return nil, fail("INPUT", "Unknown command.")
}

// Run returns an exit code, keeping payloads and credentials out of error output.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer, version string) (exitCode int) {
	defer func() {
		if recover() != nil {
			_ = json.NewEncoder(stderr).Encode(Object{"error": Object{"code": "CLIENT_ERROR", "message": "Unexpected client failure; inspect any in-flight write before retrying."}})
			exitCode = 1
		}
	}()
	o, pos, err := parse(args)
	if err == nil && o.help {
		fmt.Fprint(stdout, help)
		return 0
	}
	if err == nil && o.version {
		fmt.Fprintln(stdout, version)
		return 0
	}
	if err == nil && len(pos) == 2 && pos[0] == "mock" && pos[1] == "serve" {
		if e := simulator.Serve(o.listen, o.fault, stdout); e != nil {
			_ = json.NewEncoder(stderr).Encode(Object{"error": Object{"code": "MOCK_ERROR", "message": e.Error()}})
			return 1
		}
		return 0
	}
	var result any
	if err == nil {
		result, err = run(o, pos, stdin)
	}
	if err != nil {
		_ = json.NewEncoder(stderr).Encode(Object{"error": publicFailure(err)})
		return 1
	}
	if stream, ok := result.(*LogStream); ok {
		defer stream.Close()
		scanner := bufio.NewScanner(stream.Body)
		scanner.Buffer(make([]byte, 4096), 1<<20)
		encoder := json.NewEncoder(stdout)
		for scanner.Scan() {
			line := Object{"pod": stream.Pod, "container": stream.Container, "line": scanner.Text()}
			if stream.Simulation {
				line["simulation"] = true
			}
			if e := encoder.Encode(line); e != nil {
				return 1
			}
		}
		if e := scanner.Err(); e != nil && !errors.Is(e, context.DeadlineExceeded) {
			_ = json.NewEncoder(stderr).Encode(Object{"error": Object{"code": "STREAM_ERROR", "message": "Log stream interrupted or line exceeded 1 MiB; no reconnect attempted."}})
			return 1
		}
		return 0
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err = encoder.Encode(result); err != nil {
		return 1
	}
	return 0
}
