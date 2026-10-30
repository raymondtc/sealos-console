package console

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
}
func jwt(ns string) string {
	b, _ := json.Marshal(Object{"workspaceId": ns})
	return "header." + base64.RawURLEncoding.EncodeToString(b) + ".signature"
}

func TestTransportCredentialsAndBusinessErrors(t *testing.T) {
	tr := NewTransport()
	calls := 0
	tr.Client.Transport = roundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Scheme != "https" || r.URL.Port() != "" {
			t.Fatal("incorrect origin")
		}
		decoded, e := url.QueryUnescape(r.Header.Get("Authorization"))
		if e != nil || decoded != "line one\nline two" {
			t.Fatal("incorrect credential encoding")
		}
		return response(200, `{"code":500,"message":"SERVER_SECRET_SHOULD_NOT_APPEAR","data":{"kubeconfig":"PRIVATE"}}`), nil
	})
	_, e := tr.Send("https://cloud.example.com", "POST", "/api/updateApp", "line one\nline two", Object{}, true)
	if e == nil || strings.Contains(e.Error(), "SERVER_SECRET") || strings.Contains(e.Error(), "PRIVATE") {
		t.Fatal("business error handling or redaction failed")
	}
	if calls != 1 {
		t.Fatal("unexpected retry")
	}
}
func TestRedirectNeverForwardsCredential(t *testing.T) {
	tr := NewTransport()
	calls := 0
	tr.Client.Transport = roundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		resp := response(302, "")
		resp.Header.Set("Location", "https://elsewhere.example.net/api/getApps")
		return resp, nil
	})
	if _, e := tr.Send("https://cloud.example.com", "GET", "/api/getApps", "PRIVATE", nil, true); e == nil {
		t.Fatal("redirect was accepted")
	}
	if calls != 1 {
		t.Fatal("credential forwarded")
	}
}
func TestUnsafeOriginsRejected(t *testing.T) {
	for _, u := range []string{"http://cloud.example.com", "https://cloud.example.com:6443", "https://user:pass@cloud.example.com", "https://cloud.example.com/path", "https://cloud.example.com?token=x"} {
		if _, e := Origin(u); e == nil {
			t.Fatalf("accepted unsafe origin %s", u)
		}
	}
}
func TestWorkspaceCredentialMismatch(t *testing.T) {
	tr := NewTransport()
	tr.Client.Transport = roundTripper(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/api/auth/namespace/list":
			return response(200, `{"code":200,"data":{"namespaces":[{"id":"ns-demo","uid":"uid-demo"}]}}`), nil
		case "/api/auth/getKubeconfig":
			return response(200, `{"code":200,"data":{"kubeconfig":"contexts:\n- context:\n    namespace: ns-wrong\n"}}`), nil
		}
		t.Fatal("unexpected request")
		return nil, nil
	})
	s, e := NewSession(Profile{URL: "https://cloud.example.com", Token: jwt("ns-demo")}, tr)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Bind("", true); e == nil {
		t.Fatal("write allowed without explicit workspace")
	}
	if e = s.Bind("ns-demo", true); e == nil {
		t.Fatal("wrong namespace accepted")
	}
	if s.kubeconfig != "" {
		t.Fatal("mismatched credential retained")
	}
}
func TestProviderOutsidePlatformRejected(t *testing.T) {
	tr := NewTransport()
	tr.Client.Transport = roundTripper(func(r *http.Request) (*http.Response, error) {
		return response(200, `{"code":200,"data":[{"key":"system-applaunchpad","data":{"url":"https://untrusted.example.net"}}]}`), nil
	})
	s, _ := NewSession(Profile{URL: "https://cloud.example.com", Token: "session"}, tr)
	if _, e := s.Providers(); e == nil {
		t.Fatal("external provider accepted")
	}
}
func TestProfileContainsOnlySessionToken(t *testing.T) {
	store := Profiles{Directory: t.TempDir()}
	profile := Profile{URL: "https://cloud.example.com", Token: "SESSION_TOKEN", Workspace: "ns-demo"}
	if e := store.Save("default", profile); e != nil {
		t.Fatal(e)
	}
	loaded, e := store.Load("default")
	if e != nil || loaded != profile {
		t.Fatal("profile round trip failed", e)
	}
	b, _ := os.ReadFile(filepath.Join(store.Directory, "default.json"))
	var m Object
	_ = json.Unmarshal(b, &m)
	if len(m) != 3 || m["token"] != "SESSION_TOKEN" {
		t.Fatal("unexpected profile fields")
	}
	if _, e := store.path("../escape"); e == nil {
		t.Fatal("profile path traversal accepted")
	}
}
func TestLoginDoesNotInitializeNewAccount(t *testing.T) {
	tr := NewTransport()
	calls := 0
	tr.Client.Transport = roundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		return response(200, `{"code":200,"data":{"token":"session","needInit":true}}`), nil
	})
	if _, e := Login("https://cloud.example.com", "demo", "password", tr); e == nil {
		t.Fatal("uninitialized account accepted")
	}
	if calls != 1 {
		t.Fatal("initialization request sent")
	}
}
func TestCLIFlagsAndErrors(t *testing.T) {
	for _, args := range [][]string{
		{"app", "list", "--compare"},
		{"app", "list", "--api", "openapi"},
		{"app", "list", "--api", "console"},
	} {
		if _, _, err := parse(args); err == nil {
			t.Fatal("removed option accepted", args)
		}
	}
	o, pos, e := parse([]string{"app", "upgrade", "demo", "--image", "nginx:1.27.5", "--workspace", "ns-demo"})
	if e != nil || o.image != "nginx:1.27.5" || len(pos) != 3 {
		t.Fatal("flags after name failed", e)
	}
	var out, stderr bytes.Buffer
	if code := Run([]string{"app", "resize", "demo"}, strings.NewReader(""), &out, &stderr, "test"); code != 1 {
		t.Fatal("unsupported mutation accepted")
	}
	if out.Len() != 0 || !json.Valid(stderr.Bytes()) {
		t.Fatal("error is not structured JSON")
	}
	if _, e = readConfig("-", strings.NewReader(`{"name":"demo"} {}`)); e == nil {
		t.Fatal("trailing JSON accepted")
	}
}
