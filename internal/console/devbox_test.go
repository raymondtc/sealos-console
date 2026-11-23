package console

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDevboxPublicToggleOnlyUpdatesSelectedIngress(t *testing.T) {
	const domain = "preview.example.test"
	other := Object{"port": 9090, "portName": "admin", "openPublicDomain": true,
		"protocol": "HTTP", "publicDomain": "other", "domain": domain, "networkName": "other-ingress"}
	private := Object{"port": 8080, "portName": "http", "openPublicDomain": false}
	current := private
	var writes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var data any
		switch r.Method + " " + r.URL.Path {
		case "GET /api/getDevboxByName":
			data = Object{"name": "demo", "id": "demo-uid", "networks": []Object{current, other}}
		case "GET /api/getEnv":
			data = Object{"namespace": "ns-demo", "ingressDomain": domain, "ingressSecret": "wildcard-cert",
				"customPublicDomainPrefixEnabled": "true"}
		case "POST /api/updateDevbox":
			writes.Add(1)
			var req Object
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req["devboxName"] != "demo" || len(arr(req["patch"])) != 1 {
				t.Errorf("unexpected patch body: %v", req)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			p := obj(arr(req["patch"])[0])
			if p["kind"] != "Ingress" {
				t.Errorf("patched %v instead of a single ingress", p["kind"])
			}
			switch p["type"] {
			case "create":
				var ingress Object
				if err := json.Unmarshal([]byte(str(p["value"])), &ingress); err != nil {
					t.Errorf("invalid manifest: %v", err)
				}
				network := at(ingress, "metadata", "name")
				prefix := at(ingress, "metadata", "labels", publicDomainLabel)
				rules := arr(at(ingress, "spec", "rules"))
				tls := arr(at(ingress, "spec", "tls"))
				if len(rules) != 1 || len(tls) != 1 {
					t.Error("missing ingress rule or TLS")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				paths := arr(at(rules[0], "http", "paths"))
				if len(paths) != 1 || at(rules[0], "host") != str(prefix)+"."+domain ||
					at(tls[0], "secretName") != "wildcard-cert" ||
					at(paths[0], "backend", "service", "name") != "demo" ||
					num(at(paths[0], "backend", "service", "port", "number")) != 8080 {
					t.Errorf("wrong route: %v", ingress)
				}
				current = Object{"port": 8080, "portName": "http", "openPublicDomain": true,
					"networkName": network, "publicDomain": prefix, "domain": domain, "protocol": "HTTP"}
			case "delete":
				if current["networkName"] == nil || p["name"] != current["networkName"] {
					t.Errorf("deleted ingress %v instead of %v", p["name"], current["networkName"])
				}
				current = private
			default:
				t.Errorf("unexpected ingress operation: %v", p)
			}
		default:
			t.Errorf("unexpected route %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(Object{"code": 200, "data": data})
	}))
	defer server.Close()
	tr := NewTransport()
	tr.LocalTest = true
	s := Session{HTTP: tr, Namespace: "ns-demo", kubeconfig: "synthetic", providers: map[string]string{"devbox": server.URL}}

	preview, err := s.setDevboxPublic("demo", 8080, true, false)
	if err != nil || preview["executed"] != false || writes.Load() != 0 {
		t.Fatalf("preview=%v, err=%v, writes=%d", preview, err, writes.Load())
	}
	out, err := s.setDevboxPublic("demo", 8080, true, true)
	if err != nil || out["executed"] != true || out["publicURL"] != preview["publicURL"] || writes.Load() != 1 {
		t.Fatalf("enable=%v, err=%v, writes=%d", out, err, writes.Load())
	}
	out, err = s.setDevboxPublic("demo", 8080, true, true)
	if err != nil || out["changeRequired"] != false || writes.Load() != 1 {
		t.Fatalf("repeat enable=%v, err=%v, writes=%d", out, err, writes.Load())
	}
	out, err = s.setDevboxPublic("demo", 8080, false, true)
	if err != nil || out["executed"] != true || writes.Load() != 2 {
		t.Fatalf("disable=%v, err=%v, writes=%d", out, err, writes.Load())
	}
	out, err = s.devboxPorts("demo")
	if err != nil {
		t.Fatal(err)
	}
	items := out["items"].([]Object)
	if len(items) != 2 || items[0]["openPublicDomain"] != false || items[0]["portName"] != "http" ||
		items[1]["networkName"] != "other-ingress" || items[1]["publicURL"] != "https://other."+domain+"/" {
		t.Fatalf("unexpected ports after disable: %v", items)
	}
}

func TestDevboxConnectUsesDesktopAppToken(t *testing.T) {
	claims := base64.RawURLEncoding.EncodeToString([]byte(`{"workspaceId":"ns-demo"}`))
	regional := "regional." + claims + ".signature"
	app := "app." + claims + ".signature"
	devbox := "devbox." + claims + ".signature"
	initialized := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var data any
		switch r.Method + " " + r.URL.Path {
		case "GET /api/auth/namespace/list":
			data = Object{"namespaces": []Object{{"id": "ns-demo", "uid": "workspace-uid"}}}
		case "GET /api/auth/getKubeconfig":
			data = Object{"kubeconfig": "contexts:\n- context:\n    namespace: ns-demo\n"}
		case "GET /api/desktop/getInstalledApps":
			data = []Object{{"key": "system-devbox", "data": Object{"url": "http://" + r.Host}}}
		case "POST /api/auth/namespace/switch":
			var request Object
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request["ns_uid"] != "workspace-uid" || r.Header.Get("Authorization") != regional {
				t.Error("requested wrong workspace or session")
			}
			data = Object{"token": regional, "appToken": app}
		case "POST /api/auth/init":
			if r.Header.Get("Authorization-Bearer") != app {
				t.Error("DevBox init did not use the application token")
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(Object{"code": 403})
				return
			}
			initialized++
			data = devbox
		case "GET /api/templateRepository/template/list":
			if r.Header.Get("Authorization-Bearer") != devbox {
				t.Error("template request did not use the DevBox token")
			}
			data = Object{"templateList": []Object{{"uid": "v1", "name": "v1", "image": "example.test/demo:1", "config": "{\"secret\":\"synthetic\"}"}}}
		default:
			t.Errorf("unexpected route %s %s", r.Method, r.URL.Path)
			return
		}
		_ = json.NewEncoder(w).Encode(Object{"code": 200, "data": data})
	}))
	defer server.Close()

	t.Setenv("SEALOS_CONSOLE_CONFIG_DIR", t.TempDir())
	profiles, err := DefaultProfiles()
	if err != nil {
		t.Fatal(err)
	}
	if err := profiles.Save("devbox-test", Profile{URL: server.URL, Token: regional, Workspace: "ns-demo", LocalTest: true}); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) string {
		t.Helper()
		var output, diagnostics bytes.Buffer
		if code := Run(append([]string{"--profile", "devbox-test"}, args...), strings.NewReader(""), &output, &diagnostics, "test"); code != 0 {
			t.Fatal(diagnostics.String())
		}
		return output.String()
	}
	run("devbox", "connect")
	run("devbox", "connect", "--execute")
	got := run("devbox", "template", "versions", "repo")
	if initialized != 1 || !strings.Contains(got, "example.test/demo:1") || strings.Contains(got, "synthetic") {
		t.Fatalf("initialization, version discovery or filtering failed: %s", got)
	}
}

func TestDevboxStreamExitMustBeVerified(t *testing.T) {
	const marker = "private-marker:"
	for _, tt := range []struct {
		name, input string
		code        int
		wantError   bool
	}{
		{"remote exit", "stdout\nstderr\n" + marker + "7\n__DEVBOX_EXEC_STATUS__:7\n", 7, false},
		{"truncated stream", "stdout\n", 1, true},
		{"provider disagrees", marker + "0\n__DEVBOX_EXEC_STATUS__:1\n", 1, true},
		{"duplicated marker", marker + "0\n" + marker + "0\n", 1, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			stream := devboxExecStream{body: io.NopCloser(strings.NewReader(tt.input)), marker: marker, json: true}
			var out bytes.Buffer
			code, err := stream.write(&out)
			if code != tt.code || (err != nil) != tt.wantError || strings.Contains(out.String(), marker) {
				t.Fatalf("code=%d err=%v output=%s", code, err, out.String())
			}
		})
	}
}

func TestDevboxReleaseSnapshotsRunningBoxFirst(t *testing.T) {
	state := "Running"
	var writes []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var data any
		switch r.Method + " " + r.URL.Path {
		case "GET /api/getDevboxByName":
			data = Object{"id": "box-uid", "name": "demo", "status": Object{"value": state}}
		case "GET /api/getDevboxVersionList":
			data = []Object{}
		case "POST /api/shutdownDevbox":
			var body Object
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["shutdownMode"] != "Stopped" {
				t.Error("Release must request a snapshot while preserving the port")
			}
			writes = append(writes, "stop")
			state = "Stopped"
		case "POST /api/releaseDevbox":
			if state != "Stopped" {
				t.Error("Release started before the DevBox stopped")
			}
			writes = append(writes, "release")
		default:
			t.Errorf("unexpected route %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(Object{"code": 200, "data": data})
	}))
	defer server.Close()
	tr := NewTransport()
	tr.LocalTest = true
	s := Session{HTTP: tr, Namespace: "ns-demo", kubeconfig: "synthetic", providers: map[string]string{"devbox": server.URL}}
	plan, err := s.releaseDevbox("demo", "v1", "", false, 0)
	if err != nil || plan["stopBeforeRelease"] != true || len(writes) != 0 {
		t.Fatalf("preview did not report the snapshot without sending writes: %v, %v", plan, err)
	}
	_, err = s.releaseDevbox("demo", "v1", "", true, 0)
	if err != nil || strings.Join(writes, ",") != "stop,release" {
		t.Fatalf("writes=%v err=%v", writes, err)
	}
}
