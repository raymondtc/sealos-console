package console

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/raymondtc/sealos-console/internal/simulator"
)

func localServer(t *testing.T, fault string) (string, *simulator.Server) {
	t.Helper()
	sim := simulator.New("", fault)
	server := httptest.NewServer(sim)
	sim.BaseURL = server.URL
	t.Cleanup(server.Close)
	return server.URL, sim
}
func localSession(t *testing.T, base string) *Session {
	t.Helper()
	transport := NewTransport()
	transport.LocalTest = true
	s, e := Login(base, "demo", "demo", transport)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Bind("ns-demo", true); e != nil {
		t.Fatal(e)
	}
	return s
}
func TestCLIEndToEndAgainstLocalHTTP(t *testing.T) {
	base, _ := localServer(t, "none")
	t.Setenv("SEALOS_CONSOLE_CONFIG_DIR", t.TempDir())
	invoke := func(input string, args ...string) Object {
		t.Helper()
		var out, stderr bytes.Buffer
		args = append([]string{"--profile", "mock"}, args...)
		if status := Run(args, strings.NewReader(input), &out, &stderr, "test"); status != 0 {
			t.Fatalf("%v: %s", args, stderr.String())
		}
		var result Object
		if e := json.Unmarshal(out.Bytes(), &result); e != nil {
			t.Fatal(e)
		}
		return result
	}
	invoke("demo\n", "auth", "login", "--url", base, "--username", "demo", "--password-stdin", "--workspace", "ns-demo", "--local-test")
	list := invoke("", "app", "list", "--compare")
	if list["simulation"] != true || len(arr(list["missingFromOpenapi"])) != 1 {
		t.Fatal("simulation or omission diagnostics missing")
	}
	out := invoke("", "app", "upgrade", "worker", "--container", "sidecar", "--image", "busybox:1.37.0", "--execute", "--wait", "1")
	if out["state"] != "image-updated-and-verified" {
		t.Fatal("complex upgrade not verified")
	}
	for _, api := range []string{"console", "openapi"} {
		config := appConfig()
		config["name"] = "created-" + api
		b, _ := json.Marshal(config)
		preview := invoke(string(b), "app", "create", "--file", "-", "--api", api)
		if preview["executed"] != false {
			t.Fatal("preview executed")
		}
		created := invoke(string(b), "app", "create", "--file", "-", "--api", api, "--execute", "--wait", "1")
		if created["simulation"] != true || created["state"] != "created-and-visible" {
			t.Fatal("creation not observed")
		}
		db := dbConfig()
		db["name"] = "db-" + api
		dbBytes, _ := json.Marshal(db)
		invoke(string(dbBytes), "db", "create", "--file", "-", "--api", api, "--execute", "--wait", "1")
	}
	invoke("", "app", "upgrade", "web", "--image", "nginx:1.27.5", "--api", "openapi", "--execute", "--wait", "1")
	invoke("", "doctor")
}
func TestSimulatorConflictAndTimeoutAfterCommit(t *testing.T) {
	for _, fault := range []string{"conflict", "timeout-after-write"} {
		t.Run(fault, func(t *testing.T) {
			base, sim := localServer(t, fault)
			sim.Delay = 200 * time.Millisecond
			s := localSession(t, base)
			o := Operations{Session: s, Namespace: s.Namespace}
			if fault == "timeout-after-write" {
				s.HTTP.Client.Timeout = 20 * time.Millisecond
			}
			_, err := o.Upgrade("web", "nginx:1.27.5", "", "console", true, 0)
			if err == nil {
				t.Fatal("expected injected fault")
			}
			s.HTTP.Client.Timeout = time.Second
			w, e := o.Workload("web")
			if e != nil {
				t.Fatal(e)
			}
			actual := at(arr(at(w, "spec", "template", "spec", "containers"))[0], "image")
			wanted := "nginx:1.27.4"
			if fault == "timeout-after-write" {
				wanted = "nginx:1.27.5"
			}
			if actual != wanted {
				t.Fatal("unexpected mutation result after fault")
			}
		})
	}
}
func TestLocalModeCannotReachRemoteHosts(t *testing.T) {
	for _, raw := range []string{"http://example.com:8787", "https://cloud.example.com", "http://127.0.0.1.example.com:8787", "http://user:pass@127.0.0.1:8787"} {
		if _, e := originFor(raw, true); e == nil {
			t.Fatal("local mode accepted remote or credential-bearing origin")
		}
	}
	if _, e := Origin("http://127.0.0.1:8787"); e == nil {
		t.Fatal("production mode accepted insecure test origin")
	}
}
