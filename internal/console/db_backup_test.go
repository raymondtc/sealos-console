package console

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDBBackupCLIAgainstSimulator(t *testing.T) {
	base, _ := localServer(t, "none")
	t.Setenv("SEALOS_CONSOLE_CONFIG_DIR", t.TempDir())
	call := func(input string, wantCode int, args ...string) Object {
		t.Helper()
		var out, stderr bytes.Buffer
		args = append([]string{"--profile", "mock"}, args...)
		if code := Run(args, strings.NewReader(input), &out, &stderr, "test"); code != wantCode {
			t.Fatalf("exit=%d, want %d; %s", code, wantCode, stderr.String())
		}
		output := out.String() + stderr.String()
		if strings.Contains(output, "SIMULATED_BACKUP_PRIVATE_VALUE") || strings.Contains(output, "connection-password") {
			t.Fatal("backup output exposed a credential")
		}
		var result Object
		if err := json.Unmarshal([]byte(output), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	call("demo\n", 0, "auth", "login", "--url", base, "--username", "demo", "--password-stdin", "--local-test", "--workspace", "ns-demo")
	cfg, err := json.Marshal(dbConfig())
	if err != nil {
		t.Fatal(err)
	}
	call(string(cfg), 0, "db", "create", "--file", "-", "--execute")
	preview := call("", 0, "db", "backup", "create", "demo-db", "before-update", "--notes", "备份")
	if preview["executed"] != false || preview["backupMethod"] != "pg-basebackup" || preview["policyName"] != "demo-db-postgresql-backup-policy" {
		t.Fatalf("unexpected preview: %v", preview)
	}
	empty := call("", 0, "db", "backup", "list", "demo-db")
	if len(arr(empty["items"])) != 0 {
		t.Fatal("preview created a backup")
	}
	created := call("", 0, "db", "backup", "create", "demo-db", "before-update", "--notes", "备份", "--execute", "--wait", "1")
	if created["executed"] != true || created["state"] != "completed" || at(created, "backup", "remark") != "备份" {
		t.Fatalf("unexpected create result: %v", created)
	}
	backup := call("", 0, "db", "backup", "get", "demo-db", "before-update")
	if backup["status"] != "Completed" || backup["namespace"] != "ns-demo" || backup["totalSize"] != "1024" || backup["simulation"] != true {
		t.Fatalf("backup metadata missing: %v", backup)
	}
	seen := call("", 0, "db", "backup", "wait", "demo-db", "before-update", "--timeout", "0")
	if seen["uid"] != backup["uid"] || seen["status"] != "Completed" {
		t.Fatal("wait did not observe the requested backup")
	}
	duplicate := call("", 1, "db", "backup", "create", "demo-db", "before-update", "--execute")
	if at(duplicate, "error", "code") != "ALREADY_EXISTS" {
		t.Fatal("duplicate name did not fail before writing")
	}
}

func TestDBBackupProviderScopeAndTerminalStatus(t *testing.T) {
	for _, tt := range []struct{ name, namespace, database, phase, errorCode string }{
		{"failed", "ns-demo", "demo-db", "Failed", "BACKUP_FAILED"},
		{"wrong database", "ns-demo", "other-db", "Completed", "PROTOCOL"},
		{"wrong workspace", "ns-other", "demo-db", "Completed", "PROTOCOL"},
		{"pending", "ns-demo", "demo-db", "", "WAIT_TIMEOUT"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.RequestURI() != "/api/backup/getBackupList?dbName=demo-db" {
					t.Errorf("unexpected provider route: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				row := Object{
					"metadata": Object{"namespace": tt.namespace, "name": "before-update",
						"labels": Object{"app.kubernetes.io/instance": tt.database}},
					"status": Object{"phase": tt.phase, "failureReason": "SENSITIVE_FAILURE_DETAILS"},
				}
				_ = json.NewEncoder(w).Encode(Object{"code": 200, "data": []Object{row}})
			}))
			defer server.Close()
			s := backupTestSession(server.URL)
			_, err := s.waitDBBackup(backupDatabase{Name: "demo-db", Engine: "postgresql"}, "before-update", 0, "", "")
			if err == nil || publicFailure(err).Code != tt.errorCode || strings.Contains(err.Error(), "SENSITIVE_FAILURE_DETAILS") {
				t.Fatalf("unexpected wait result: %v", err)
			}
		})
	}
}

func TestDBBackupCreateProviderWriteIsNotRetried(t *testing.T) {
	var writes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var response Object
		switch r.Method + " " + r.URL.RequestURI() {
		case "GET /api/getEnv":
			response = Object{"code": 200, "data": Object{"BACKUP_ENABLED": true}}
		case "GET /api/backup/getBackupList?dbName=demo-db":
			response = Object{"code": 200, "data": []Object{}}
		case "POST /api/backup/create":
			writes.Add(1)
			var body Object
			if json.NewDecoder(r.Body).Decode(&body) != nil || !sameJSON(body, Object{
				"dbName": "demo-db", "dbType": "postgresql", "backupName": "before-update", "remark": "manual",
			}) {
				t.Error("wrong manual backup request")
			}
			response = Object{"code": 500, "error": Object{"message": "PRIVATE_PROVIDER_ERROR"}}
		default:
			t.Errorf("unexpected provider route: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()
	s := backupTestSession(server.URL)
	_, err := s.createDBBackup(backupDatabase{Name: "demo-db", Engine: "postgresql"}, "before-update", "manual", true, time.Second)
	if err == nil || writes.Load() != 1 || strings.Contains(err.Error(), "PRIVATE_PROVIDER_ERROR") {
		t.Fatalf("unexpected failed write outcome: %v, writes=%d", err, writes.Load())
	}
}

func backupTestSession(origin string) *Session {
	tr := NewTransport()
	tr.LocalTest = true
	return &Session{HTTP: tr, Namespace: "ns-demo", kubeconfig: "synthetic", providers: map[string]string{"dbprovider": origin}}
}
