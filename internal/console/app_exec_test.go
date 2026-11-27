package console

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestAppExecThroughTerminal(t *testing.T) {
	auth := base64.StdEncoding.EncodeToString([]byte("demo:synthetic"))
	var starts, commands atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var data any
		switch r.Method + " " + r.URL.RequestURI() {
		case "GET /api/getApps":
			data = []Object{{"metadata": Object{"namespace": "ns-demo", "name": "files"}}}
		case "GET /api/getAppPodsByAppName?name=files":
			data = []Object{{"metadata": Object{"namespace": "ns-demo", "name": "files-pod"},
				"spec": Object{"containers": []Object{{"name": "files"}, {"name": "proxy"}}}}}
		case "POST /api/apply":
			starts.Add(1)
			data = "http://" + r.Host + "/?authorization=" + url.QueryEscape(auth)
		case "GET /ws":
			if r.Header.Get("Authorization") != "Basic "+auth || r.Header.Get("Origin") != "http://"+r.Host {
				t.Error("wrong Terminal credentials or origin")
				w.WriteHeader(http.StatusForbidden)
				return
			}
			conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"tty"}})
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.CloseNow()
			ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
			defer cancel()
			_, hello, err := conn.Read(ctx)
			var message Object
			if err != nil || json.Unmarshal(hello, &message) != nil || message["AuthToken"] != auth {
				t.Error("wrong Terminal hello")
				return
			}
			if err := conn.Write(ctx, websocket.MessageBinary, []byte("0bootstrap prompt")); err != nil {
				t.Error(err)
				return
			}
			_, input, err := conn.Read(ctx)
			fields := strings.Split(string(input), "'")
			if err != nil || len(fields) != 5 || !strings.HasPrefix(string(input), "0 ") {
				t.Error("wrong Terminal command frame")
				return
			}
			commands.Add(1)
			script, err := base64.StdEncoding.DecodeString(fields[3])
			if err != nil || !strings.Contains(string(script), "'kubectl' '-n' 'ns-demo' 'exec' 'files-pod' '-c' 'files' '--' '/dufs' '--version'") {
				t.Error("target command must use direct argv and the requested namespace")
				return
			}
			nonce := regexp.MustCompile(`cli-[a-f0-9]{24}`).FindString(string(script))
			if nonce == "" {
				t.Error("missing command status nonce")
				return
			}
			// Frames can split the markers, and the PTY can echo input.
			for _, chunk := range []string{"echoed input\x1e" + nonce, "-start\x1fdufs 0.47.0\n\x1e" + nonce + "-exit:", "7\x1f"} {
				if err := conn.Write(ctx, websocket.MessageBinary, []byte("0"+chunk)); err != nil {
					t.Error(err)
					return
				}
			}
			return
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
	s := Session{HTTP: tr, Profile: Profile{URL: server.URL, LocalTest: true}, Namespace: "ns-demo", kubeconfig: "synthetic",
		providers: map[string]string{"applaunchpad": server.URL, "terminal": server.URL}}

	o := options{pod: "files-pod", timeout: 4, command: []string{"/dufs", "--version"}}
	if _, err := s.appExec("files", o); err == nil || starts.Load() != 0 {
		t.Fatal("ambiguous containers must fail before opening the Terminal")
	}
	o.container = "files"
	preview, err := s.appExec("files", o)
	if err != nil || at(preview, "executed") != false || starts.Load() != 0 {
		t.Fatalf("preview unexpectedly started the Terminal: %v", err)
	}
	o.execute = true
	out, err := s.appExec("files", o)
	if err != nil {
		t.Fatal(err)
	}
	result := out.(*appExecResult)
	if result.code != 7 || string(result.data) != "dufs 0.47.0\n" || starts.Load() != 1 || commands.Load() != 1 {
		t.Fatal("expected the remote output and exit status from a single execution")
	}
}

func TestAppTerminalRejectsForeignOrigin(t *testing.T) {
	s := Session{Profile: Profile{URL: "https://console.example.test"}}
	auth := url.QueryEscape(base64.StdEncoding.EncodeToString([]byte("demo:synthetic")))
	for _, endpoint := range []string{
		"https://console.example.test.other.test/?authorization=" + auth,
		"http://box.console.example.test/?authorization=" + auth,
		"https://box.console.example.test/?authorization=" + auth + "&authorization=" + auth,
	} {
		if _, _, _, err := s.terminalSocket(endpoint); err == nil {
			t.Fatal("unsafe Terminal session URL accepted")
		}
	}
}
