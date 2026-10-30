package console

import (
	"encoding/json"
	"strings"
	"testing"
)

type fakeProvider struct {
	work      Object
	calls     []string
	writes    int
	reads     int
	race      bool
	timeout   bool
	damage    bool
	dbCreated bool
	lastBody  Object
}

func (f *fakeProvider) Provider(service, method, path string, body any, envelope bool) (any, error) {
	f.calls = append(f.calls, method+" "+path)
	if method == "GET" {
		switch path {
		case "/api/getApps":
			f.reads++
			if f.race && f.reads == 2 {
				obj(f.work["metadata"])["resourceVersion"] = "11"
			}
			if f.work == nil {
				return []any{}, nil
			}
			return []any{clone(f.work)}, nil
		case "/api/platform/getInitData":
			return Object{}, nil
		case "/api/platform/getVersion":
			return Object{"postgresql": []any{Object{"id": "postgresql-14.8.0"}}}, nil
		case "/api/getDBList":
			if f.dbCreated {
				return []any{Object{"name": "demo-db", "dbType": "postgresql", "status": Object{"value": "Running"}}}, nil
			}
			return []any{}, nil
		}
	}
	if method == "POST" || method == "PATCH" {
		f.writes++
		f.lastBody = obj(clone(body))
		if f.timeout {
			return nil, fail("TRANSPORT", "timeout")
		}
		switch path {
		case "/api/updateApp":
			patch := at(arr(at(body, "patch"))[0], "value")
			if at(patch, "metadata", "resourceVersion") != at(f.work, "metadata", "resourceVersion") {
				return nil, fail("CONFLICT", "version conflict")
			}
			f.work = obj(mergePatch(f.work, patch))
			if f.damage {
				obj(f.work["spec"])["replicas"] = float64(3)
			}
			obj(f.work["metadata"])["resourceVersion"] = "11"
			return nil, nil
		case "/api/applyApp":
			var w Object
			_ = json.Unmarshal([]byte(str(arr(at(body, "yamlList"))[0])), &w)
			obj(w["metadata"])["namespace"] = "ns-demo"
			f.work = w
			return nil, nil
		case "/api/createDB":
			f.dbCreated = true
			return nil, nil
		}
	}
	return nil, fail("TEST", "Unexpected route %s %s", method, path)
}
func TestUpgradePreviewAndApply(t *testing.T) {
	f := &fakeProvider{work: fixture()}
	o := Operations{Session: f, Namespace: "ns-demo"}
	plan, e := o.Upgrade("demo", "registry.example.com/demo:v2", "main", false, 0)
	if e != nil {
		t.Fatal(e)
	}
	if f.writes != 0 || plan["executed"] != false {
		t.Fatal("preview performed a write")
	}
	b, _ := json.Marshal(plan)
	if strings.Contains(string(b), "SENSITIVE_FIXTURE_VALUE") {
		t.Fatal("preview leaked value")
	}
	out, e := o.Upgrade("demo", "registry.example.com/demo:v2", "main", true, 0)
	if e != nil {
		t.Fatal(e)
	}
	if f.writes != 1 || out["state"] != "image-updated-and-verified" {
		t.Fatal("write not verified")
	}
}
func TestWriteConflictTimeoutAndUnexpectedChange(t *testing.T) {
	for _, mode := range []string{"race", "timeout", "damage"} {
		t.Run(mode, func(t *testing.T) {
			f := &fakeProvider{work: fixture(), race: mode == "race", timeout: mode == "timeout", damage: mode == "damage"}
			o := Operations{Session: f, Namespace: "ns-demo"}
			_, e := o.Upgrade("demo", "registry.example.com/demo:v2", "main", true, 0)
			if e == nil {
				t.Fatal("expected failure")
			}
			wanted := 1
			if mode == "race" {
				wanted = 0
			}
			if f.writes != wanted {
				t.Fatal("unexpected retry, fallback or rollback")
			}
		})
	}
}
func TestCreationPreviewAndApply(t *testing.T) {
	f := &fakeProvider{}
	o := Operations{Session: f, Namespace: "ns-demo"}
	if _, e := o.CreateApp(appConfig(), false, 0); e != nil {
		t.Fatal(e)
	}
	if f.writes != 0 {
		t.Fatal("creation preview wrote")
	}
	if _, e := o.CreateApp(appConfig(), true, 0); e != nil {
		t.Fatal(e)
	}
	if f.writes != 1 {
		t.Fatal("wrong write count")
	}
	if _, e := o.CreateApp(appConfig(), true, 0); e == nil {
		t.Fatal("existing app should not be overwritten")
	}
	if f.writes != 1 {
		t.Fatal("duplicate create wrote")
	}
	if _, e := o.CreateDB(dbConfig(), false, 0); e != nil {
		t.Fatal(e)
	}
	if f.writes != 1 {
		t.Fatal("db preview wrote")
	}
	if _, e := o.CreateDB(dbConfig(), true, 0); e != nil {
		t.Fatal(e)
	}
	if f.writes != 2 {
		t.Fatal("wrong db write count")
	}
}
func TestAppInventoryPreservesComplexApps(t *testing.T) {
	f := &fakeProvider{work: fixture()}
	o := Operations{Session: f, Namespace: "ns-demo"}
	result, e := o.Apps()
	if e != nil {
		t.Fatal(e)
	}
	if len(arr(result["items"])) != 1 || at(arr(result["items"])[0], "multiContainer") != true {
		t.Fatal("complex app missing from inventory")
	}
}
func TestWaitDoesNotReportStaleReadyReplicas(t *testing.T) {
	f := &fakeProvider{work: fixture()}
	obj(f.work["metadata"])["generation"] = float64(2)
	o := Operations{Session: f, Namespace: "ns-demo"}
	if _, e := o.WaitApp("demo", 0, "", ""); e == nil {
		t.Fatal("stale observedGeneration marked ready")
	}
	if f.writes != 0 {
		t.Fatal("wait wrote resources")
	}
}
