package console

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestAppUpdateMainAndInitInOnePatch(t *testing.T) {
	const image = "registry.example.com/demo@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	work := fixture()
	pod := obj(at(work, "spec", "template", "spec"))
	main := obj(arr(pod["containers"])[0])
	main["ports"] = []any{Object{"name": "http", "containerPort": 8080}}
	main["env"] = append(arr(main["env"]),
		Object{"name": "AUTH_PROVIDER", "value": "EXISTING_PROVIDER_SECRET"},
		Object{"name": "CREDENTIAL", "valueFrom": Object{"secretKeyRef": Object{"name": "creds", "key": "token"}}},
	)
	migrate := obj(arr(pod["initContainers"])[0])
	migrate["name"] = "migrate"
	migrate["command"] = []any{"migrate", "up"}
	migrate["envFrom"] = []any{Object{"configMapRef": Object{"name": "config"}}, Object{"secretRef": Object{"name": "creds"}}}
	migrate["env"] = []any{
		Object{"name": "DATABASE_URL", "valueFrom": Object{"secretKeyRef": Object{"name": "creds", "key": "db"}}},
		Object{"name": "UNCHANGED", "value": "INIT_ONLY_SECRET"},
	}
	pod["initContainers"] = append(arr(pod["initContainers"]), Object{"name": "prepare", "image": "busybox:1.36"})
	snapshot := obj(clone(work))
	env := []any{Object{"name": "AUTH_PROVIDER", "value": "sso"}, Object{"name": "AUTH_ORIGIN", "value": "https://auth.example.test"}}
	request := Object{
		"containers":      []any{Object{"name": "main", "image": image, "env": env}},
		"initContainers":  []any{Object{"name": "migrate", "image": image, "env": env}},
		"originImageName": image,
	}
	f := &fakeProvider{work: work}
	ops := Operations{Session: f, Namespace: "ns-demo"}
	preview, err := ops.UpdateApp("demo", request, false, 0)
	if err != nil || preview["executed"] != false || f.writes != 0 || !sameJSON(f.work, snapshot) {
		t.Fatalf("unexpected preview or write: %v", err)
	}
	result, err := ops.UpdateApp("demo", request, true, time.Second)
	if err != nil || f.writes != 1 || result["state"] != "updated-and-verified" || at(result, "rollout", "state") != "ready" {
		t.Fatalf("unexpected update or rollout: %v, %d writes", err, f.writes)
	}
	for _, output := range []Object{preview, result} {
		b, _ := json.Marshal(output)
		for _, forbidden := range []string{"SENSITIVE_FIXTURE_VALUE", "EXISTING_PROVIDER_SECRET", "INIT_ONLY_SECRET", "KEEP_ME", "https://auth.example.test"} {
			if strings.Contains(string(b), forbidden) {
				t.Fatal("update output leaked an env value")
			}
		}
		for _, group := range []string{"containers", "initContainers"} {
			changes := arr(at(clone(output), "diff", group))
			if len(changes) != 1 || !sameJSON(at(changes[0], "environmentNames"), []string{"AUTH_PROVIDER", "AUTH_ORIGIN"}) {
				t.Fatalf("missing env change names in %s diff", group)
			}
		}
	}
	items := arr(f.lastBody["patch"])
	if len(items) != 1 || at(items[0], "kind") != "Deployment" || at(items[0], "value", "metadata", "resourceVersion") != "10" ||
		len(f.lastBody) != 2 || len(obj(at(items[0], "value", "spec", "template", "spec"))) != 2 {
		t.Fatal("expected one versioned workload patch scoped to both container arrays")
	}
	expectedPod := obj(at(snapshot, "spec", "template", "spec"))
	expectedMain := obj(arr(expectedPod["containers"])[0])
	expectedMain["image"] = image
	expectedEnv := arr(expectedMain["env"])
	obj(expectedEnv[3])["value"] = "sso"
	expectedMain["env"] = append(expectedEnv, env[1])
	expectedInit := obj(arr(expectedPod["initContainers"])[0])
	expectedInit["image"] = image
	expectedInit["env"] = append(arr(expectedInit["env"]), env...)
	obj(at(snapshot, "metadata", "annotations"))["originImageName"] = image
	obj(snapshot["metadata"])["resourceVersion"] = "11"
	if !sameJSON(f.work, snapshot) {
		t.Fatal("read-back differs from intended image/env/annotation changes")
	}
	again, err := ops.UpdateApp("demo", request, true, 0)
	if err != nil || again["state"] != "unchanged" || f.writes != 1 {
		t.Fatal("repeating the update must be a no-op")
	}
}

func TestAppUpdateRefusesAmbiguousOrUnsafeTargets(t *testing.T) {
	for _, test := range []struct {
		name, input string
	}{
		{"wrong group", `{"containers":[{"name":"init","image":"busybox:1.36"}]}`},
		{"duplicate container", `{"initContainers":[{"name":"init","image":"busybox:1.36"},{"name":"init","image":"busybox:1.37"}]}`},
		{"overwrite reference", `{"containers":[{"name":"main","env":[{"name":"POD_IP","value":"constant"}]}]}`},
		{"inject reference", `{"containers":[{"name":"main","env":[{"name":"NEW","valueFrom":{"fieldRef":{}}}]}]}`},
		{"invalid annotation", `{"originImageName":""}`},
		{"invalid digest", `{"initContainers":[{"name":"init","image":"registry.example.com/demo@sha256:1234"}]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var request Object
			if err := json.Unmarshal([]byte(test.input), &request); err != nil {
				t.Fatal(err)
			}
			f := &fakeProvider{work: fixture()}
			_, err := (Operations{Session: f, Namespace: "ns-demo"}).UpdateApp("demo", request, true, 0)
			if err == nil || f.writes != 0 {
				t.Fatal("unsafe update did not stop before the write")
			}
		})
	}
}
