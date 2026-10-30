package console

import (
	"fmt"
	"net/http"
	"os"
	"sort"
	"testing"
)

// Opt-in verification against a saved session. No real credentials or deployment
// identifiers belong in this test or its output. The transport fails closed on
// any request outside the read-only console endpoint allowlist.
func TestLiveReadOnlyConsoleEndpoints(t *testing.T) {
	profile := os.Getenv("SEALOS_CONSOLE_READONLY_PROFILE")
	if profile == "" {
		t.Skip("set SEALOS_CONSOLE_READONLY_PROFILE to opt in")
	}
	profiles, err := DefaultProfiles()
	if err != nil {
		t.Fatal("cannot resolve profile directory")
	}
	p, err := profiles.Load(profile)
	if err != nil {
		t.Fatal("cannot load read-only verification profile")
	}
	if p.LocalTest {
		t.Fatal("live verification requires a real HTTPS profile")
	}
	allowed := map[string]bool{
		"/api/auth/namespace/list":      true,
		"/api/auth/getKubeconfig":       true,
		"/api/desktop/getInstalledApps": true,
		"/api/platform/getAppConfig":    true,
		"/api/platform/getInitData":     true,
		"/api/platform/getVersion":      true,
		"/api/getApps":                  true,
		"/api/getAppByAppName":          true,
		"/api/getDBList":                true,
	}
	requests := map[string]int{}
	tr := NewTransport()
	tr.Client.Transport = roundTripper(func(r *http.Request) (*http.Response, error) {
		validQuery := r.URL.RawQuery == ""
		if r.URL.Path == "/api/getAppByAppName" {
			q := r.URL.Query()
			validQuery = len(q) == 1 && len(q["appName"]) == 1 && validName(q.Get("appName")) == nil
		}
		if r.Method != http.MethodGet || !allowed[r.URL.Path] || !validQuery {
			return nil, fmt.Errorf("request outside read-only allowlist")
		}
		requests[r.Method+" "+r.URL.Path]++
		return http.DefaultTransport.RoundTrip(r)
	})
	s, err := NewSession(p, tr)
	if err != nil {
		t.Fatal("cannot initialize session")
	}
	rows, err := s.Workspaces()
	if err != nil {
		t.Fatal("workspace query failed:", publicFailure(err).Code)
	}
	t.Logf("workspaces: %d", len(rows))
	if err = s.Bind("", false); err != nil {
		t.Fatal("workspace binding failed (saved and current workspace must match):", publicFailure(err).Code)
	}
	o := Operations{Session: s, Namespace: s.Namespace}
	apps, err := o.Apps()
	if err != nil {
		t.Fatal("app list failed:", publicFailure(err).Code)
	}
	complexApps, references, publicNetworks := 0, 0, 0
	for _, row := range arr(apps["items"]) {
		app, e := o.App(str(at(row, "name")))
		if e != nil {
			t.Fatal("app get failed:", publicFailure(e).Code)
		}
		for _, network := range arr(app["networks"]) {
			if at(network, "openPublicDomain") == true {
				publicNetworks++
			}
		}
		if at(app, "app", "multiContainer") == true {
			complexApps++
		}
		for _, c := range arr(at(app, "app", "containers")) {
			references += len(arr(at(c, "references")))
		}
	}
	t.Logf("app list/get: %d apps, %d multi-container apps, %d environment references", len(arr(apps["items"])), complexApps, references)
	dbs, err := o.Databases()
	if err != nil {
		t.Fatal("database list failed:", publicFailure(err).Code)
	}
	for _, row := range dbs {
		if _, e := o.DB(str(at(row, "name"))); e != nil {
			t.Fatal("database get failed:", publicFailure(e).Code)
		}
	}
	t.Logf("existing public networks read: %d", publicNetworks)
	domains, err := o.Domains()
	if err != nil {
		t.Fatal("public domain discovery failed:", publicFailure(err).Code)
	}
	t.Logf("platform domain choices: %d; HTTPS: %v", len(domains["domains"].([]string)), domains["https"])
	preview := Object{"name": "console-readonly-public-preview", "image": "nginx:1.27.5", "cpu": 0.5, "memoryMi": 512, "ports": []any{Object{"number": 8080, "public": true}}}
	plan, err := o.CreateApp(preview, false, 0)
	if err != nil {
		t.Fatal("public creation preview failed:", publicFailure(err).Code)
	}
	if plan["executed"] != false || len(arr(plan["publicEndpoints"])) != 1 {
		t.Fatal("invalid public creation preview")
	}
	t.Log("public creation preview passed; no resources submitted")
	t.Logf("database list/get: %d databases", len(dbs))
	engines, err := o.Engines()
	if err != nil {
		t.Fatal("database engine catalog failed:", publicFailure(err).Code)
	}
	versions := 0
	for _, v := range engines {
		versions += len(arr(v))
	}
	t.Logf("database catalog: %d engine keys, %d versions (availability not provision-tested)", len(engines), versions)
	if _, err = o.call("applaunchpad", "GET", "/api/platform/getInitData", nil, true); err != nil {
		t.Fatal("app creation metadata read failed:", publicFailure(err).Code)
	}
	doctor, err := Doctor(s)
	if err != nil {
		t.Fatal("doctor failed:", publicFailure(err).Code)
	}
	for key, check := range obj(doctor["checks"]) {
		if at(check, "ok") != true {
			t.Errorf("doctor check failed: %s", key)
		}
	}
	paths := []string{}
	for path := range requests {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		t.Logf("%s: %d requests", path, requests[path])
	}
}
