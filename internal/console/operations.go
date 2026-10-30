package console

import (
	"errors"
	"time"
)

type Provider interface {
	Provider(service, method, path string, body any, envelope bool) (any, error)
}
type Operations struct {
	Session   Provider
	Namespace string
}

func (o Operations) call(service, method, path string, body any, envelope bool) (any, error) {
	return o.Session.Provider(service, method, path, body, envelope)
}

func (o Operations) Workloads() ([]any, error) {
	data, err := o.call("applaunchpad", "GET", "/api/getApps", nil, true)
	if err != nil {
		return nil, err
	}
	rows, ok := data.([]any)
	if !ok {
		return nil, fail("PROTOCOL", "Unexpected console app list.")
	}
	for _, row := range rows {
		if str(at(row, "metadata", "namespace")) != o.Namespace {
			return nil, fail("PROTOCOL", "App list contains a different namespace.")
		}
	}
	return rows, nil
}
func (o Operations) Workload(appName string) (Object, error) {
	if e := validName(appName); e != nil {
		return nil, e
	}
	rows, err := o.Workloads()
	if err != nil {
		return nil, err
	}
	var found Object
	count := 0
	for _, w := range rows {
		if str(at(w, "metadata", "name")) == appName {
			found = obj(w)
			count++
		}
	}
	if count != 1 {
		return nil, fail("NOT_FOUND", "App not found uniquely in console list.")
	}
	return found, nil
}
func (o Operations) Apps() (Object, error) {
	rows, err := o.Workloads()
	if err != nil {
		return nil, err
	}
	items := []any{}
	for _, w := range rows {
		items = append(items, AppSummary(obj(w)))
	}
	result := Object{"namespace": o.Namespace, "source": "console", "items": items}
	return result, nil
}
func (o Operations) App(appName string) (Object, error) {
	w, err := o.Workload(appName)
	if err != nil {
		return nil, err
	}
	out := Object{"source": "console", "app": AppSummary(w)}
	return out, nil
}
func publicFailure(err error) *Failure {
	var e *Failure
	if errors.As(err, &e) {
		return e
	}
	return &Failure{Code: "CLIENT_ERROR", Message: "Unexpected client failure; no automatic retry."}
}
func (o Operations) Databases() ([]any, error) {
	data, err := o.call("dbprovider", "GET", "/api/getDBList", nil, true)
	if err != nil {
		return nil, err
	}
	rows, ok := data.([]any)
	if !ok {
		return nil, fail("PROTOCOL", "Unexpected database list.")
	}
	out := []any{}
	for _, row := range rows {
		m := Object{}
		for _, k := range []string{"name", "dbType", "cpu", "memory", "storage", "replicas"} {
			if v, ok := obj(row)[k]; ok {
				m[k] = v
			}
		}
		m["status"] = at(row, "status", "value")
		out = append(out, m)
	}
	return out, nil
}
func (o Operations) DB(n string) (Object, error) {
	if e := validName(n); e != nil {
		return nil, e
	}
	rows, err := o.Databases()
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if str(at(r, "name")) == n {
			return obj(r), nil
		}
	}
	return nil, fail("NOT_FOUND", "Database not found in console list.")
}
func (o Operations) Engines() (Object, error) {
	data, err := o.call("dbprovider", "GET", "/api/platform/getVersion", nil, true)
	if err != nil {
		return nil, err
	}
	m, ok := data.(map[string]any)
	if !ok {
		return nil, fail("PROTOCOL", "Unexpected database engine list.")
	}
	return m, nil
}

func (o Operations) CreateApp(raw Object, execute bool, wait time.Duration) (Object, error) {
	s, body, err := AppCreate(raw)
	if err != nil {
		return nil, err
	}
	rows, err := o.Workloads()
	if err != nil {
		return nil, err
	}
	n := str(s["name"])
	for _, w := range rows {
		if str(at(w, "metadata", "name")) == n {
			return nil, fail("ALREADY_EXISTS", "App already exists; creation never updates it.")
		}
	}
	probe, path := "/api/platform/getInitData", "/api/applyApp"
	if _, err = o.call("applaunchpad", "GET", probe, nil, true); err != nil {
		return nil, err
	}
	envNames := []any{}
	for _, e := range arr(s["env"]) {
		envNames = append(envNames, at(e, "name"))
	}
	plan := Object{"operation": "app.create", "api": "console", "method": "POST", "path": path, "namespace": o.Namespace, "name": n, "image": s["image"], "cpu": s["cpu"], "memoryMi": s["memoryMi"], "replicas": s["replicas"], "ports": s["ports"], "storage": s["storage"], "environmentNames": envNames, "executed": false}
	if !execute {
		return plan, nil
	}
	if _, err = o.call("applaunchpad", "POST", path, body, true); err != nil {
		return nil, err
	}
	w, err := o.Workload(n)
	matched := false
	if err == nil {
		for _, c := range arr(at(w, "spec", "template", "spec", "containers")) {
			if str(at(c, "name")) == n && at(c, "image") == s["image"] {
				matched = true
			}
		}
	}
	if !matched {
		return nil, fail("WRITE_UNVERIFIED", "Create was accepted but console read-back failed. Inspect the app before retrying; no rollback sent.")
	}
	plan["executed"] = true
	plan["state"] = "created-and-visible"
	plan["app"] = AppSummary(w)
	if wait > 0 {
		rollout, e := o.WaitApp(n, wait, str(s["image"]), n)
		if e != nil {
			return nil, e
		}
		plan["rollout"] = rollout
	}
	return plan, nil
}
func (o Operations) CreateDB(raw Object, execute bool, wait time.Duration) (Object, error) {
	body, err := DBCreate(raw)
	if err != nil {
		return nil, err
	}
	engines, err := o.Engines()
	if err != nil {
		return nil, err
	}
	allowed := false
	for _, v := range arr(engines[str(raw["type"])]) {
		if v == raw["version"] || at(v, "id") == raw["version"] {
			allowed = true
		}
	}
	if !allowed {
		return nil, fail("INPUT", "Database version is not available on this platform.")
	}
	rows, err := o.Databases()
	if err != nil {
		return nil, err
	}
	n := str(raw["name"])
	for _, r := range rows {
		if str(at(r, "name")) == n {
			return nil, fail("ALREADY_EXISTS", "Database already exists; create never edits it.")
		}
	}
	path := "/api/createDB"
	plan := Object{"operation": "db.create", "api": "console", "method": "POST", "path": path, "namespace": o.Namespace, "configuration": raw, "executed": false}
	if !execute {
		return plan, nil
	}
	if _, err = o.call("dbprovider", "POST", path, body, true); err != nil {
		return nil, err
	}
	db, err := o.DB(n)
	if err != nil {
		return nil, fail("WRITE_UNVERIFIED", "Create was accepted but database read-back failed. Inspect it before retrying; no rollback sent.")
	}
	plan["executed"] = true
	plan["state"] = "created-and-visible"
	plan["database"] = db
	if wait > 0 {
		end := time.Now().Add(wait)
		for str(db["status"]) != "Running" {
			if !time.Now().Before(end) {
				return nil, fail("WAIT_TIMEOUT", "Database did not reach Running; no rollback sent.")
			}
			sleepUntil(end)
			db, err = o.DB(n)
			if err != nil {
				return nil, err
			}
		}
		plan["database"] = db
	}
	return plan, nil
}

func (o Operations) Upgrade(appName, target, container string, execute bool, wait time.Duration) (Object, error) {
	w, err := o.Workload(appName)
	if err != nil {
		return nil, err
	}
	body, diff, err := ImageUpdate(w, target, container)
	if err != nil {
		return nil, err
	}
	path, method, concurrency := "/api/updateApp", "POST", "resourceVersion in merge patch"
	kept := []any{}
	for _, c := range arr(at(w, "spec", "template", "spec", "containers")) {
		kept = append(kept, at(c, "name"))
	}
	plan := Object{"operation": "app.upgrade", "api": "console", "method": method, "path": path, "namespace": o.Namespace, "name": appName, "diff": diff, "resourceVersion": at(w, "metadata", "resourceVersion"), "concurrency": concurrency, "preservedContainers": kept, "executed": false}
	if body == nil {
		plan["state"] = "unchanged"
		return plan, nil
	}
	if !execute {
		return plan, nil
	}
	latest, err := o.Workload(appName)
	if err != nil {
		return nil, err
	}
	if at(latest, "metadata", "resourceVersion") != at(w, "metadata", "resourceVersion") {
		return nil, fail("CONFLICT", "Workload changed during planning. No write sent; generate a new plan.")
	}
	if _, err = o.call("applaunchpad", method, path, body, true); err != nil {
		return nil, err
	}
	after, err := o.Workload(appName)
	expected := obj(clone(w["spec"]))
	for _, c := range arr(at(expected, "template", "spec", "containers")) {
		if at(c, "name") == diff["container"] {
			obj(c)["image"] = target
		}
	}
	if err != nil || !sameJSON(after["spec"], expected) {
		return nil, fail("WRITE_UNVERIFIED", "Write was accepted but image-only preservation could not be verified. Inspect the app; no retry or rollback sent.")
	}
	plan["executed"] = true
	plan["state"] = "image-updated-and-verified"
	plan["app"] = AppSummary(after)
	if wait > 0 {
		rollout, e := o.WaitApp(appName, wait, target, str(diff["container"]))
		if e != nil {
			return nil, e
		}
		plan["rollout"] = rollout
	}
	return plan, nil
}
func sleepUntil(end time.Time) {
	d := time.Until(end)
	if d > 3*time.Second {
		d = 3 * time.Second
	}
	if d > 0 {
		time.Sleep(d)
	}
}
func (o Operations) WaitApp(n string, timeout time.Duration, image, container string) (Object, error) {
	end := time.Now().Add(timeout)
	for {
		w, err := o.Workload(n)
		if err != nil {
			return nil, err
		}
		status, spec := obj(w["status"]), obj(w["spec"])
		replicas := num(spec["replicas"])
		if image != "" {
			match := false
			for _, c := range arr(at(spec, "template", "spec", "containers")) {
				if at(c, "name") == container && at(c, "image") == image {
					match = true
				}
			}
			if !match {
				return nil, fail("CONFLICT", "Target image changed while waiting; another operation may be in progress.")
			}
		}
		observed := num(status["observedGeneration"]) >= num(at(w, "metadata", "generation")) && num(at(w, "metadata", "generation")) > 0
		ready := replicas > 0 && observed && num(status["readyReplicas"]) == replicas && num(status["updatedReplicas"]) == replicas
		if w["kind"] == "StatefulSet" {
			ready = ready && str(status["updateRevision"]) != "" && status["currentRevision"] == status["updateRevision"]
		} else {
			ready = ready && num(status["replicas"]) == replicas && num(status["availableReplicas"]) >= replicas
		}
		if ready {
			return Object{"state": "ready", "app": AppSummary(w)}, nil
		}
		if !time.Now().Before(end) {
			return nil, fail("WAIT_TIMEOUT", "Rollout did not become ready before timeout; no rollback sent.")
		}
		sleepUntil(end)
	}
}
func Doctor(s *Session) (Object, error) {
	config, err := s.Desktop("GET", "/api/platform/getAppConfig", nil)
	if err != nil {
		return nil, err
	}
	services, err := s.Providers()
	if err != nil {
		return nil, err
	}
	checks := Object{}
	o := Operations{Session: s, Namespace: s.Namespace}
	for _, test := range []struct {
		name string
		run  func() (any, error)
	}{
		{"apps", func() (any, error) { return o.Apps() }}, {"databases", func() (any, error) { return o.Databases() }}, {"databaseVersions", func() (any, error) { return o.Engines() }},
	} {
		data, e := test.run()
		if e != nil {
			checks[test.name] = Object{"ok": false, "error": publicFailure(e)}
		} else {
			checks[test.name] = Object{"ok": true, "result": data}
		}
	}
	return Object{"url": s.Profile.URL, "namespace": s.Namespace, "providers": services, "kubeconfigMenuEnabled": at(config, "desktop", "layout", "kubeconfigEnabled"), "authentication": "Regional token stored locally; provider kubeconfig stays in memory; no Kubernetes transport", "platformRelease": "unconfirmed; API versions do not identify the installed platform release", "checks": checks}, nil
}
