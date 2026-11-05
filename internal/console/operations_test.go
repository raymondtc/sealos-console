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
	pvcs      []Object
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
			// Match the console's pre-patch PVC reconciliation: missing templates
			// delete app-owned claims even when the workload patch is image-only.
			var stateful Object
			_ = json.Unmarshal([]byte(str(at(body, "stateFulSetYaml"))), &stateful)
			retained := []Object{}
			for _, pvc := range f.pvcs {
				for _, claim := range arr(at(stateful, "spec", "volumeClaimTemplates")) {
					if at(claim, "metadata", "annotations", "path") == at(pvc, "metadata", "annotations", "path") {
						retained = append(retained, pvc)
						break
					}
				}
			}
			f.pvcs = retained
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

func TestStatefulImageUpgradeRetainsProviderPVCs(t *testing.T) {
	w := fixture()
	w["kind"] = "StatefulSet"
	claims := []any{}
	pvcs := []Object{}
	for _, path := range []string{"/data", "/uploads"} {
		claims = append(claims, Object{"metadata": Object{"annotations": Object{"path": path, "value": "5"}}, "spec": Object{"resources": Object{"requests": Object{"storage": "5Gi"}}}})
		pvcs = append(pvcs, Object{"metadata": Object{"annotations": Object{"path": path, "value": "5"}}, "data": "must-survive"})
	}
	obj(w["spec"])["volumeClaimTemplates"] = claims
	f := &fakeProvider{work: w, pvcs: pvcs}
	o := Operations{Session: f, Namespace: "ns-demo"}
	if _, err := o.Upgrade("demo", "registry.example.com/demo:v2", "main", true, 0); err != nil {
		t.Fatal(err)
	}
	if !sameJSON(f.pvcs, pvcs) {
		t.Fatal("console PVC reconciliation deleted persistent application data")
	}
	var supplied Object
	if err := json.Unmarshal([]byte(str(f.lastBody["stateFulSetYaml"])), &supplied); err != nil {
		t.Fatal(err)
	}
	if !sameJSON(at(supplied, "spec", "volumeClaimTemplates"), claims) {
		t.Fatal("storage templates changed during image upgrade")
	}
	if at(arr(at(supplied, "spec", "template", "spec", "containers"))[0], "image") != "registry.example.com/demo:v2" {
		t.Fatal("StatefulSet preservation document has stale image")
	}
}

func TestUnsafeStorageUpgradeRefusesBeforeProviderWrite(t *testing.T) {
	for _, mode := range []string{"explicit-pvc", "missing-claims", "missing-annotations", "duplicate-paths"} {
		t.Run(mode, func(t *testing.T) {
			w := fixture()
			if mode == "explicit-pvc" {
				obj(at(w, "spec", "template", "spec"))["volumes"] = []any{Object{"name": "data", "persistentVolumeClaim": Object{"claimName": "shared"}}}
			} else {
				w["kind"] = "StatefulSet"
				if mode == "missing-annotations" {
					obj(w["spec"])["volumeClaimTemplates"] = []any{Object{"metadata": Object{"name": "data"}}}
				} else if mode == "duplicate-paths" {
					claim := Object{"metadata": Object{"annotations": Object{"path": "/data", "value": "5"}}}
					obj(w["spec"])["volumeClaimTemplates"] = []any{claim, clone(claim)}
				}
			}
			f := &fakeProvider{work: w}
			_, err := (Operations{Session: f, Namespace: "ns-demo"}).Upgrade("demo", "registry.example.com/demo:v2", "main", true, 0)
			if err == nil || f.writes != 0 {
				t.Fatal("unsafe storage reconciliation was not refused before writing")
			}
		})
	}
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
