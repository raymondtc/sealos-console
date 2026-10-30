package console

import (
	"encoding/json"
	"strings"
	"testing"
)

func fixture() Object {
	var w Object
	_ = json.Unmarshal([]byte(`{
  "apiVersion":"apps/v1","kind":"Deployment",
  "metadata":{"name":"demo","namespace":"ns-demo","resourceVersion":"10","generation":1,
    "labels":{"cloud.sealos.io/app-deploy-manager":"demo","custom":"keep"},
    "annotations":{"originImageName":"registry.example.com/demo:v1","custom":"keep"}},
  "spec":{"replicas":1,"selector":{"matchLabels":{"app":"demo"}},"template":{
    "metadata":{"labels":{"app":"demo"},"annotations":{"custom":"keep"}},
    "spec":{"imagePullSecrets":[{"name":"pull-secret"}],"serviceAccountName":"custom-account",
      "initContainers":[{"name":"init","image":"registry.example.com/init:v1"}],
      "volumes":[{"name":"data","emptyDir":{}}],
      "containers":[
        {"name":"main","image":"registry.example.com/demo:v1",
         "env":[{"name":"POD_IP","valueFrom":{"fieldRef":{"fieldPath":"status.podIP"}}},
                {"name":"PASSWORD","value":"SENSITIVE_FIXTURE_VALUE"},
                {"name":"CONFIG","valueFrom":{"configMapKeyRef":{"name":"settings","key":"mode"}}}],
         "envFrom":[{"secretRef":{"name":"settings"}}],
         "livenessProbe":{"httpGet":{"path":"/health","port":8080}},
         "volumeMounts":[{"name":"data","mountPath":"/data"}],
         "resources":{"requests":{"cpu":"100m"},"limits":{"cpu":"1"}},
         "securityContext":{"readOnlyRootFilesystem":true},"futureField":{"preserve":true}},
        {"name":"sidecar","image":"registry.example.com/sidecar:v1","args":["serve"],
         "env":[{"name":"CONFIG","value":"KEEP_ME"}],"startupProbe":{"exec":{"command":["true"]}}}
      ]}}},
  "status":{"observedGeneration":1,"replicas":1,"updatedReplicas":1,"readyReplicas":1,"availableReplicas":1}
}`), &w)
	return w
}

// Actual RFC 7396 semantics: objects merge, arrays are replaced wholesale.
func mergePatch(original, patch any) any {
	p, ok := patch.(map[string]any)
	if !ok {
		return clone(patch)
	}
	out := obj(clone(original))
	for k, v := range p {
		if v == nil {
			delete(out, k)
		} else {
			out[k] = mergePatch(out[k], v)
		}
	}
	return out
}

func TestImageOnlyMergePreservesComplexWorkload(t *testing.T) {
	for _, target := range []string{"main", "sidecar"} {
		t.Run(target, func(t *testing.T) {
			before := fixture()
			snapshot := clone(before)
			body, diff, err := ImageUpdate(before, "registry.example.com/new:v2", target, "console")
			if err != nil {
				t.Fatal(err)
			}
			if !sameJSON(before, snapshot) {
				t.Fatal("planner mutated original")
			}
			patch := at(arr(body["patch"])[0], "value")
			if at(patch, "metadata", "resourceVersion") != "10" {
				t.Fatal("missing concurrency precondition")
			}
			after := obj(mergePatch(before, patch))
			expected := obj(clone(before))
			for _, c := range arr(at(expected, "spec", "template", "spec", "containers")) {
				if at(c, "name") == target {
					obj(c)["image"] = "registry.example.com/new:v2"
				}
			}
			if target == "main" {
				obj(at(expected, "metadata", "annotations"))["originImageName"] = "registry.example.com/new:v2"
			}
			if !sameJSON(after, expected) {
				t.Fatal("image update changed unrelated fields")
			}
			if diff["container"] != target {
				t.Fatal("wrong container selected")
			}
		})
	}
}
func TestUpgradeGuardrails(t *testing.T) {
	w := fixture()
	for _, test := range []struct{ name, container, api, image string }{
		{"ambiguous", "", "console", "registry.example.com/demo:v2"},
		{"missing", "absent", "console", "registry.example.com/demo:v2"},
		{"openapi-multicontainer", "main", "openapi", "registry.example.com/demo:v2"},
		{"registry-change", "main", "console", "other.example.com/demo:v2"},
		{"untagged", "main", "console", "registry.example.com/demo"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := ImageUpdate(w, test.image, test.container, test.api); err == nil {
				t.Fatal("expected refusal")
			}
		})
	}
	cs := arr(at(w, "spec", "template", "spec", "containers"))
	obj(at(w, "spec", "template", "spec"))["containers"] = cs[:1]
	if _, _, err := ImageUpdate(w, "registry.example.com/demo:v2", "main", "openapi"); err == nil {
		t.Fatal("fieldRef must not be forced through incompatible OpenAPI")
	}
	delete(obj(cs[0]), "env")
	b, _, err := ImageUpdate(w, "registry.example.com/demo:v2", "main", "openapi")
	if err != nil {
		t.Fatal(err)
	}
	if !sameJSON(b, Object{"image": Object{"imageName": "registry.example.com/demo:v2"}}) {
		t.Fatal("OpenAPI sent extra update fields")
	}
}
func TestSummaryDoesNotExposeEnvValues(t *testing.T) {
	s := AppSummary(fixture())
	b, _ := json.Marshal(s)
	for _, secret := range []string{"SENSITIVE_FIXTURE_VALUE", "KEEP_ME"} {
		if strings.Contains(string(b), secret) {
			t.Fatal("summary exposed env value")
		}
	}
	if len(arr(s["openapiIssues"])) != 2 {
		t.Fatal("missing incompatible reference diagnostics")
	}
}
func appConfig() Object {
	return Object{"name": "demo", "image": "nginx:1.27.5", "cpu": .5, "memoryMi": float64(512), "ports": []any{Object{"number": float64(8080)}}}
}
func TestAppCreateContracts(t *testing.T) {
	s, body, err := AppCreate(appConfig(), "console")
	if err != nil {
		t.Fatal(err)
	}
	if body["mode"] != "create" || len(arr(body["yamlList"])) != 2 {
		t.Fatal("wrong creation shape")
	}
	var work Object
	_ = json.Unmarshal([]byte(str(arr(body["yamlList"])[0])), &work)
	if at(work, "metadata", "labels", ManagerLabel) != "demo" {
		t.Fatal("missing platform manager label")
	}
	c := arr(at(work, "spec", "template", "spec", "containers"))[0]
	if at(c, "resources", "limits", "cpu") != "500m" || at(c, "resources", "requests", "memory") != "51Mi" {
		t.Fatal("resource conversion mismatch")
	}
	if at(work, "spec", "template", "spec", "automountServiceAccountToken") != false {
		t.Fatal("unexpected token mount")
	}
	_, v2, err := AppCreate(appConfig(), "openapi")
	if err != nil {
		t.Fatal(err)
	}
	if at(v2, "quota", "memory") != .5 || at(arr(v2["ports"])[0], "isPublic") != false {
		t.Fatal("OpenAPI changed units or exposed a port")
	}
	if s["replicas"] != float64(1) {
		t.Fatal("wrong replicas default")
	}
}
func TestCreateRejectsUnsupportedOrUnknownInput(t *testing.T) {
	c := appConfig()
	c["arbitraryKubernetesObject"] = Object{}
	if _, _, e := AppCreate(c, "console"); e == nil {
		t.Fatal("unknown field allowed")
	}
	c = appConfig()
	c["replicas"] = float64(0)
	if _, _, e := AppCreate(c, "console"); e == nil {
		t.Fatal("zero replicas silently defaulted")
	}
	c = appConfig()
	c["env"] = []any{Object{"name": "POD_IP", "valueFrom": Object{"fieldRef": Object{"fieldPath": "status.podIP"}}}}
	if _, _, e := AppCreate(c, "console"); e != nil {
		t.Fatal(e)
	}
	if _, _, e := AppCreate(c, "openapi"); e == nil {
		t.Fatal("incompatible env accepted")
	}
	c = appConfig()
	obj(arr(c["ports"])[0])["public"] = true
	if _, _, e := AppCreate(c, "console"); e == nil {
		t.Fatal("public port must not silently become private")
	}
}
func dbConfig() Object {
	return Object{"name": "demo-db", "type": "postgresql", "version": "postgresql-14.8.0", "cpu": float64(1), "memoryMi": float64(1024), "storageGi": float64(10), "replicas": float64(1), "terminationPolicy": "Delete"}
}
func TestDatabaseCreateUnitsAndNoEdit(t *testing.T) {
	console, err := DBCreate(dbConfig(), "console")
	if err != nil {
		t.Fatal(err)
	}
	if console["isEdit"] != false || at(console, "dbForm", "cpu") != float64(1000) || at(console, "dbForm", "memory") != float64(1024) {
		t.Fatal("incorrect console creation contract")
	}
	v1, err := DBCreate(dbConfig(), "openapi")
	if err != nil {
		t.Fatal(err)
	}
	if at(v1, "resource", "memory") != float64(1) {
		t.Fatal("v1 memory not converted")
	}
	small := dbConfig()
	small["cpu"] = .5
	small["memoryMi"] = float64(512)
	if _, e := DBCreate(small, "openapi"); e == nil {
		t.Fatal("unsupported v1 resource accepted")
	}
	if _, e := DBCreate(small, "console"); e != nil {
		t.Fatal(e)
	}
}
