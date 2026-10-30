// Package console operates Sealos HTTP services, never the Kubernetes API server.
package console

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

type Object = map[string]any

type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status,omitempty"`
}

func (e *Failure) Error() string { return e.Message }
func fail(code, format string, args ...any) error {
	return &Failure{Code: code, Message: fmt.Sprintf(format, args...)}
}
func obj(v any) Object {
	m, _ := v.(map[string]any)
	if m == nil {
		return Object{}
	}
	return m
}
func arr(v any) []any  { a, _ := v.([]any); return a }
func str(v any) string { s, _ := v.(string); return s }
func num(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	}
	return 0
}
func at(v any, keys ...string) any {
	for _, k := range keys {
		v = obj(v)[k]
	}
	return v
}
func clone(v any) any { b, _ := json.Marshal(v); var out any; _ = json.Unmarshal(b, &out); return out }

func Origin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" ||
		(u.Port() != "" && u.Port() != "443") {
		return "", fail("CONFIG", "Use an HTTPS platform origin on port 443, without credentials, path, query or fragment.")
	}
	if strings.Contains(u.Hostname(), ":") {
		return "", fail("CONFIG", "Use the platform's DNS hostname.")
	}
	return "https://" + strings.ToLower(u.Hostname()), nil
}

func originFor(raw string, localTest bool) (string, error) {
	if !localTest {
		return Origin(raw)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return "", fail("CONFIG", "Local simulation requires http://127.0.0.1:PORT; remote hosts are forbidden in local-test mode.")
	}
	return "http://" + u.Host, nil
}

// Transport never retries requests or forwards credentials across redirects.
type Transport struct {
	Client    *http.Client
	LocalTest bool
}

func NewTransport() *Transport {
	return &Transport{Client: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("redirect refused")
	}}}
}

func (t *Transport) Send(base, method, path, credential string, body any, envelope bool) (any, error) {
	base, err := originFor(base, t.LocalTest)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(path, "/api/") || strings.ContainsAny(path, "#\r\n") {
		return nil, fail("CONFIG", "Only Sealos /api/ paths are supported.")
	}
	var reader io.Reader
	if body != nil {
		b, e := json.Marshal(body)
		if e != nil {
			return nil, fail("INPUT", "Cannot encode request body.")
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, base+path, reader)
	if err != nil {
		return nil, fail("CONFIG", "Invalid request URL.")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "sealos-console")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if credential != "" {
		req.Header.Set("Authorization", strings.ReplaceAll(url.QueryEscape(credential), "+", "%20"))
	}
	resp, err := t.Client.Do(req)
	if err != nil {
		return nil, fail("TRANSPORT", "Request failed or timed out; no retry sent. Inspect any in-flight write before retrying.")
	}
	defer resp.Body.Close()
	if resp.StatusCode == 204 {
		return nil, nil
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, fail("TRANSPORT", "Response read failed; inspect any in-flight write before retrying.")
	}
	var result any
	if json.Unmarshal(b, &result) != nil {
		return nil, &Failure{Code: "PROTOCOL", Message: fmt.Sprintf("Non-JSON response: HTTP %d at %s.", resp.StatusCode, strings.Split(path, "?")[0]), Status: resp.StatusCode}
	}
	code, hasCode := obj(result)["code"]
	failed := resp.StatusCode < 200 || resp.StatusCode >= 300
	if envelope && hasCode {
		n, ok := code.(float64)
		failed = failed || !ok || n < 200 || n >= 300
	}
	if failed {
		// Server error bodies can echo kubeconfig, env values or registry passwords.
		symbol := str(at(result, "error", "code"))
		if !regexp.MustCompile(`^[A-Z_]{1,64}$`).MatchString(symbol) {
			symbol = "API_ERROR"
		}
		business := "n/a"
		if n, ok := code.(float64); ok {
			business = fmt.Sprint(n)
		}
		return nil, &Failure{Code: symbol, Status: resp.StatusCode, Message: fmt.Sprintf("%s %s failed: HTTP %d, business code %s. No write retry sent.", method, strings.Split(path, "?")[0], resp.StatusCode, business)}
	}
	if envelope && hasCode {
		return obj(result)["data"], nil
	}
	return result, nil
}

type Profile struct {
	URL       string                       `json:"url"`
	Token     string                       `json:"token"`
	Workspace string                       `json:"workspace,omitempty"`
	LocalTest bool                         `json:"localTest,omitempty"`
	Storage   map[string]StorageCredential `json:"storage,omitempty"`
}

type Profiles struct{ Directory string }

func DefaultProfiles() (Profiles, error) {
	dir := os.Getenv("SEALOS_CONSOLE_CONFIG_DIR")
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return Profiles{}, err
		}
		dir = filepath.Join(base, "sealos-console")
	}
	return Profiles{Directory: dir}, nil
}
func (p Profiles) path(name string) (string, error) {
	if !regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`).MatchString(name) {
		return "", fail("INPUT", "Invalid profile name.")
	}
	return filepath.Join(p.Directory, name+".json"), nil
}
func (p Profiles) Load(name string) (Profile, error) {
	path, err := p.path(name)
	if err != nil {
		return Profile{}, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return Profile{}, fail("LOGIN_REQUIRED", "No saved session; run auth login.")
	}
	if !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return Profile{}, fail("CONFIG", "Profile must be a private regular file (0600 on Unix).")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Profile{}, fail("CONFIG", "Cannot read session profile.")
	}
	var profile Profile
	if json.Unmarshal(b, &profile) != nil || profile.Token == "" {
		return Profile{}, fail("CONFIG", "Invalid session profile.")
	}
	return profile, nil
}
func (p Profiles) Save(name string, profile Profile) error {
	path, err := p.path(name)
	if err != nil {
		return err
	}
	if info, e := os.Lstat(path); e == nil && !info.Mode().IsRegular() {
		return fail("CONFIG", "Refusing to replace a non-regular profile.")
	}
	if err = os.MkdirAll(p.Directory, 0700); err != nil {
		return fail("CONFIG", "Cannot create profile directory.")
	}
	f, err := os.CreateTemp(p.Directory, ".session-")
	if err != nil {
		return fail("CONFIG", "Cannot save session.")
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = json.NewEncoder(f).Encode(profile); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		return fail("CONFIG", "Cannot replace session profile.")
	}
	return nil
}
func (p Profiles) Remove(name string) error {
	path, err := p.path(name)
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

type Session struct {
	Profile    Profile
	HTTP       *Transport
	Namespace  string
	kubeconfig string // Never serialized to disk or returned from CLI output.
	providers  map[string]string
}

func NewSession(p Profile, t *Transport) (*Session, error) {
	base, err := originFor(p.URL, p.LocalTest)
	if err != nil {
		return nil, err
	}
	p.URL = base
	if t == nil {
		t = NewTransport()
	}
	t.LocalTest = p.LocalTest
	return &Session{Profile: p, HTTP: t}, nil
}
func Login(base, username, password string, t *Transport) (*Session, error) {
	if username == "" || password == "" {
		return nil, fail("INPUT", "Username and password are required.")
	}
	if t == nil {
		t = NewTransport()
	}
	data, err := t.Send(base, "POST", "/api/auth/password", "", Object{"user": username, "password": password}, true)
	if err != nil {
		return nil, err
	}
	if str(at(data, "token")) == "" || at(data, "needInit") == true {
		return nil, fail("LOGIN_REQUIRED", "Account is not initialized; no workspace was created.")
	}
	region, err := t.Send(base, "POST", "/api/auth/regionToken", str(at(data, "token")), Object{}, true)
	if err != nil {
		return nil, err
	}
	if str(at(region, "token")) == "" {
		return nil, fail("PROTOCOL", "Login returned no regional token.")
	}
	return NewSession(Profile{URL: base, Token: str(at(region, "token")), LocalTest: t.LocalTest}, t)
}
func (s *Session) Desktop(method, path string, body any) (any, error) {
	return s.HTTP.Send(s.Profile.URL, method, path, s.Profile.Token, body, true)
}
func (s *Session) Workspaces() ([]any, error) {
	result, err := s.Desktop("GET", "/api/auth/namespace/list", nil)
	if err != nil {
		return nil, err
	}
	rows, ok := at(result, "namespaces").([]any)
	if !ok {
		return nil, fail("PROTOCOL", "Unexpected workspace list.")
	}
	return rows, nil
}
func tokenWorkspace(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var data Object
	if json.Unmarshal(b, &data) != nil {
		return ""
	}
	return str(data["workspaceId"])
}
func (s *Session) Bind(workspace string, writing bool) error {
	if workspace == "" {
		workspace = s.Profile.Workspace
	}
	if writing && workspace == "" {
		return fail("INPUT", "Creation and upgrade require an explicit --workspace (namespace or UID).")
	}
	rows, err := s.Workspaces()
	if err != nil {
		return err
	}
	current := tokenWorkspace(s.Profile.Token)
	target := workspace
	if target == "" {
		target = current
	}
	var selected Object
	count := 0
	for _, row := range rows {
		m := obj(row)
		if target == str(m["id"]) || target == str(m["uid"]) {
			selected = m
			count++
		}
	}
	if count != 1 {
		return fail("INPUT", "Workspace is absent or ambiguous; use its namespace or UID.")
	}
	ns := str(selected["id"])
	if current != ns {
		switched, e := s.Desktop("POST", "/api/auth/namespace/switch", Object{"ns_uid": selected["uid"]})
		if e != nil {
			return e
		}
		token := str(at(switched, "token"))
		if token == "" {
			return fail("PROTOCOL", "Workspace switch returned no token.")
		}
		s.Profile.Token = token
	}
	data, err := s.Desktop("GET", "/api/auth/getKubeconfig", nil)
	if err != nil {
		return err
	}
	kc := str(at(data, "kubeconfig"))
	matches := regexp.MustCompile(`(?m)^\s+namespace:\s*["']?([a-z0-9-]+)["']?\s*$`).FindAllStringSubmatch(kc, -1)
	if len(matches) == 0 {
		return fail("PROTOCOL", "Workspace credential has no namespace.")
	}
	for _, m := range matches {
		if m[1] != ns {
			return fail("PROTOCOL", "Credential namespace differs from selected workspace.")
		}
	}
	s.kubeconfig = kc
	s.Namespace = ns
	return nil
}
func (s *Session) Providers() (map[string]string, error) {
	if s.providers != nil {
		return s.providers, nil
	}
	data, err := s.Desktop("GET", "/api/desktop/getInstalledApps", nil)
	if err != nil {
		return nil, err
	}
	rows, ok := data.([]any)
	if !ok {
		return nil, fail("PROTOCOL", "Unexpected installed app list.")
	}
	root, _ := url.Parse(s.Profile.URL)
	found := map[string]string{}
	for _, row := range rows {
		key := str(at(row, "key"))
		if key != "system-applaunchpad" && key != "system-dbprovider" && key != "system-objectstorage" {
			continue
		}
		base, e := originFor(str(at(row, "data", "url")), s.Profile.LocalTest)
		if e != nil {
			return nil, e
		}
		u, _ := url.Parse(base)
		if u.Hostname() != root.Hostname() && !strings.HasSuffix(u.Hostname(), "."+root.Hostname()) {
			return nil, fail("CONFIG", "Provider is outside the platform domain; credential forwarding refused.")
		}
		if s.Profile.LocalTest && base != s.Profile.URL {
			return nil, fail("CONFIG", "Mock provider must use the exact same local origin.")
		}
		found[strings.TrimPrefix(key, "system-")] = base
	}
	s.providers = found
	return found, nil
}
func (s *Session) Provider(service, method, path string, body any, envelope bool) (any, error) {
	if s.kubeconfig == "" {
		return nil, fail("CONFIG", "Bind a workspace before provider operations.")
	}
	services, err := s.Providers()
	if err != nil {
		return nil, err
	}
	base := services[service]
	if base == "" {
		return nil, fail("CONFIG", "Provider %s is unavailable.", service)
	}
	return s.HTTP.Send(base, method, path, s.kubeconfig, body, envelope)
}
