package console

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStorageAndObservationCLI(t *testing.T) {
	base, _ := localServer(t, "none")
	t.Setenv("SEALOS_CONSOLE_CONFIG_DIR", t.TempDir())
	invoke := func(input string, want int, args ...string) Object {
		t.Helper()
		var out, stderr bytes.Buffer
		code := Run(args, strings.NewReader(input), &out, &stderr, "test")
		if code != want {
			t.Fatalf("command %v code %d: %s", args, code, stderr.String())
		}
		var data Object
		if code == 0 {
			if json.Unmarshal(out.Bytes(), &data) != nil {
				t.Fatal("invalid JSON", out.String())
			}
		} else {
			json.Unmarshal(stderr.Bytes(), &data)
		}
		if strings.Contains(out.String(), "mock-secret") {
			t.Fatal("credential leaked")
		}
		return data
	}
	invoke("demo\n", 0, "auth", "login", "--url", base, "--username", "demo", "--password-stdin", "--local-test", "--workspace", "ns-demo")
	if len(arr(invoke("", 0, "storage", "buckets")["items"])) != 1 {
		t.Fatal("bucket inventory missing")
	}
	if result := invoke("", 1, "storage", "ls", "archive"); at(result, "error", "code") != "STORAGE_LOGIN_REQUIRED" {
		t.Fatal("unexpected S3 query failure", result)
	}
	invoke("", 0, "storage", "connect")
	invoke("", 1, "storage", "ls", "archive")
	invoke("", 0, "storage", "connect", "--execute")
	invoke("", 0, "storage", "create", "artifacts")
	invoke("", 1, "storage", "bucket", "artifacts")
	invoke("", 0, "storage", "create", "artifacts", "--execute")
	invoke("", 1, "storage", "create", "artifacts", "--execute")
	dir := t.TempDir()
	source := filepath.Join(dir, "input.txt")
	content := []byte("test payload\n")
	os.WriteFile(source, content, 0600)
	key := "folder/a+b %?#中文.txt"
	invoke("", 0, "storage", "upload", "artifacts", key, "--file", source)
	if len(arr(invoke("", 0, "storage", "ls", "artifacts")["items"])) != 0 {
		t.Fatal("upload preview wrote")
	}
	invoke("", 0, "storage", "upload", "artifacts", key, "--file", source, "--execute")
	invoke("", 0, "storage", "upload", "artifacts", "second.txt", "--file", source, "--execute")
	listing := invoke("", 0, "storage", "ls", "artifacts", "--limit", "1")
	if listing["truncated"] != true || at(arr(listing["items"])[0], "key") != key {
		t.Fatal("pagination or encoded key broken")
	}
	if len(arr(invoke("", 0, "storage", "ls", "artifacts", "--next-token", str(listing["nextToken"]))["items"])) != 1 {
		t.Fatal("pagination failed")
	}
	if at(invoke("", 1, "storage", "upload", "artifacts", key, "--file", source, "--execute"), "error", "code") != "ALREADY_EXISTS" {
		t.Fatal("upload overwrote existing object")
	}
	target := filepath.Join(dir, "download.txt")
	invoke("", 0, "storage", "download", "artifacts", key, "--output", target)
	got, e := os.ReadFile(target)
	if e != nil || !bytes.Equal(got, content) {
		t.Fatal("download differs", e)
	}
	invoke("", 1, "storage", "download", "artifacts", key, "--output", target)
	pods := invoke("", 0, "app", "pods", "worker")
	if len(arr(at(arr(pods["items"])[0], "containers"))) != 2 {
		t.Fatal("sidecar missing")
	}
	stats := invoke("", 0, "app", "stats", "worker")
	if len(arr(at(arr(stats["items"])[0], "containers"))) != 2 {
		t.Fatal("sidecar metrics missing")
	}
	invoke("", 1, "app", "logs", "worker")
	invoke("", 1, "app", "logs", "worker", "--container", "absent")
	logs := invoke("", 0, "app", "logs", "worker", "--container", "sidecar")
	if !strings.Contains(str(logs["logs"]), "simulated") {
		t.Fatal("logs missing")
	}
	invoke("", 0, "app", "logs", "worker", "--container", "sidecar", "--follow", "--timeout", "1")
	invoke("", 0, "app", "events", "worker")
	if invoke("", 0, "app", "metrics", "worker")["state"] != "data" {
		t.Fatal("metrics missing")
	}
	b, _ := json.Marshal(dbConfig())
	invoke(string(b), 0, "db", "create", "--file", "-", "--execute")
	invoke("", 0, "db", "pods", "demo-db")
	invoke("", 0, "db", "logs", "demo-db")
	invoke("", 0, "db", "metrics", "demo-db")
	invoke("", 0, "db", "events", "demo-db")
	invoke("", 0, "auth", "logout")
	invoke("", 1, "storage", "ls", "archive")
}
func TestS3EndpointScopeAndReadOnlyInitialization(t *testing.T) {
	base, _ := localServer(t, "none")
	s := localSession(t, base)
	for _, endpoint := range []string{"https://outside.example.com", "http://127.0.0.1:9"} {
		if _, e := s.storageEndpoint(endpoint); e == nil {
			t.Fatal("credential forwarding accepted")
		}
	}
	calls := 0
	original := s.HTTP.Client.Transport
	if original == nil {
		original = http.DefaultTransport
	}
	s.HTTP.Client.Transport = roundTripper(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/api/user/init" {
			calls++
		}
		return original.RoundTrip(r)
	})
	if _, e := s.ConnectStorage(false); e != nil {
		t.Fatal(e)
	}
	_, _ = s.ListObjects("archive", "", "", 5)
	if calls != 0 {
		t.Fatal("preview or query initialized a user")
	}
}
func TestMissingMetricsAndLogTargetValidation(t *testing.T) {
	base, _ := localServer(t, "none")
	s := localSession(t, base)
	o := Operations{Session: s, Namespace: s.Namespace}
	if _, e := o.Logs("app", "worker", "another-pod", "sidecar", "1h", 10, false, false, time.Second); e == nil {
		t.Fatal("out-of-app Pod allowed")
	}
	if _, e := o.Logs("app", "worker", "", "sidecar", "0h", 10, false, false, time.Second); e == nil {
		t.Fatal("unbounded duration allowed")
	}
	original := s.HTTP.Client.Transport
	if original == nil {
		original = http.DefaultTransport
	}
	s.HTTP.Client.Transport = roundTripper(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/api/getPodsMetrics" || r.URL.Path == "/api/monitor/getMonitorData" {
			return response(200, `{"code":200,"data":[]}`), nil
		}
		return original.RoundTrip(r)
	})
	stats, e := o.Stats("worker")
	if e != nil || len(arr(stats["missingPods"])) != 1 {
		t.Fatal("missing Pod was silently treated as zero", e)
	}
	metrics, e := o.Metrics("app", "worker", "cpu", "15m")
	if e != nil || metrics["state"] != "no-data" {
		t.Fatal("empty series was silently treated as zero", e)
	}
}

func TestEmptyLogsAndMalformedMetrics(t *testing.T) {
	base, _ := localServer(t, "none")
	s := localSession(t, base)
	o := Operations{Session: s, Namespace: s.Namespace}
	original := http.DefaultTransport
	payload := `{"code":200}`
	s.HTTP.Client.Transport = roundTripper(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/api/getPodLogs" || r.URL.Path == "/api/monitor/getMonitorData" {
			return response(200, payload), nil
		}
		return original.RoundTrip(r)
	})
	v, e := o.Logs("app", "web", "", "", "1h", 5, false, false, time.Second)
	if e != nil || at(v, "state") != "no-data" || at(v, "logs") != "" {
		t.Fatal("empty logs mishandled", e)
	}
	payload = `{"code":200,"data":[{"name":"web","xData":[1],"yData":[]}]}`
	if _, e = o.Metrics("app", "web", "cpu", "15m"); e == nil {
		t.Fatal("misaligned metrics accepted")
	}
}

func TestStorageDownloadFailureCleanupAndCredentialScope(t *testing.T) {
	base, _ := localServer(t, "none")
	s := localSession(t, base)
	if _, e := s.ConnectStorage(true); e != nil {
		t.Fatal(e)
	}
	other := *s
	other.Namespace = "ns-other"
	if _, e := other.s3(); e == nil {
		t.Fatal("credentials crossed workspace")
	}
	original := http.DefaultTransport
	s.HTTP.Client.Transport = roundTripper(func(r *http.Request) (*http.Response, error) {
		res, e := original.RoundTrip(r)
		if e == nil && r.URL.Path == "/demo-archive/hello.txt" {
			res.Header.Set("X-Amz-Meta-Sealos-Console-Sha256", "mismatched")
		}
		return res, e
	})
	dir := t.TempDir()
	if _, e := s.Download("archive", "hello.txt", filepath.Join(dir, "out")); e == nil {
		t.Fatal("corrupt download accepted")
	}
	files, _ := os.ReadDir(dir)
	if len(files) != 0 {
		t.Fatal("failed download left files")
	}
}

func TestUncertainUploadIsNotRetried(t *testing.T) {
	base, _ := localServer(t, "none")
	s := localSession(t, base)
	if _, e := s.ConnectStorage(true); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(t.TempDir(), "input")
	if e := os.WriteFile(path, []byte("data"), 0600); e != nil {
		t.Fatal(e)
	}
	writes := 0
	original := http.DefaultTransport
	s.HTTP.Client.Transport = roundTripper(func(r *http.Request) (*http.Response, error) {
		res, e := original.RoundTrip(r)
		if r.Method == "PUT" {
			writes++
			if res != nil {
				res.Body.Close()
			}
			return nil, fmt.Errorf("simulated lost response")
		}
		return res, e
	})
	if _, e := s.Upload("archive", "new.txt", path, true); e == nil {
		t.Fatal("uncertain upload reported success")
	}
	if writes != 1 {
		t.Fatal("write retried", writes)
	}
	v, e := s.ListObjects("archive", "new.txt", "", 10)
	if e != nil || len(arr(v["items"])) != 1 {
		t.Fatal("committed object missing", e)
	}
}
