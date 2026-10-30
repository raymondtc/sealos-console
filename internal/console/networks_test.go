package console

import (
	"encoding/json"
	"strings"
	"testing"
)

func networkConfig() Object {
	return Object{"SEALOS_DOMAIN": "apps.example.test", "SEALOS_USER_DOMAINS": []any{Object{"name": "alt.example.test", "secretName": "alternate-cert"}}, "DISABLE_HTTPS": false, "DOMAIN_PORT": "", "HTTP_PORT": ""}
}
func publicAppConfig() Object {
	c := appConfig()
	c["ports"] = []any{Object{"number": float64(8080), "public": true}, Object{"number": float64(9090), "public": true, "domain": "alt.example.test"}, Object{"number": float64(5432), "public": false}}
	return c
}
func TestPublicIngressContract(t *testing.T) {
	for _, https := range []bool{true, false} {
		t.Run(map[bool]string{true: "https", false: "http"}[https], func(t *testing.T) {
			config := networkConfig()
			config["DISABLE_HTTPS"] = !https
			config["DOMAIN_PORT"] = "8443"
			config["HTTP_PORT"] = "8081"
			app, body, err := AppCreate(publicAppConfig(), config)
			if err != nil {
				t.Fatal(err)
			}
			resources := []Object{}
			for _, y := range arr(body["yamlList"]) {
				var r Object
				if err := json.Unmarshal([]byte(str(y)), &r); err != nil {
					t.Fatal(err)
				}
				resources = append(resources, r)
			}
			if len(resources) != 4 {
				t.Fatal("expected workload, service and two public ingresses")
			}
			endpoints := arr(app["publicEndpoints"])
			if len(endpoints) != 2 || at(endpoints[0], "host") == at(endpoints[1], "host") {
				t.Fatal("public endpoints not distinct")
			}
			for i, r := range resources[2:] {
				endpoint := endpoints[i]
				host := str(at(endpoint, "host"))
				prefix := str(at(endpoint, "publicDomain"))
				if r["kind"] != "Ingress" || r["apiVersion"] != "networking.k8s.io/v1" || at(r, "metadata", "labels", ManagerLabel) != "demo" || at(r, "metadata", "labels", publicDomainLabel) != prefix || str(at(r, "metadata", "labels", publicPortLabel)) != map[int]string{0: "8080", 1: "9090"}[i] {
					t.Fatal("missing platform ingress identity")
				}
				if at(r, "metadata", "annotations", "kubernetes.io/ingress.class") != "nginx" || at(r, "metadata", "annotations", "nginx.ingress.kubernetes.io/backend-protocol") != "HTTP" {
					t.Fatal("wrong ingress annotations")
				}
				rule := arr(at(r, "spec", "rules"))[0]
				path := arr(at(rule, "http", "paths"))[0]
				if at(rule, "host") != host || at(path, "path") != "/" || at(path, "pathType") != "Prefix" || at(path, "backend", "service", "name") != "demo" || num(at(path, "backend", "service", "port", "number")) != num(at(endpoint, "port")) {
					t.Fatal("wrong public routing graph")
				}
				tls := arr(at(r, "spec", "tls"))
				scheme, port := "http", ":8081/"
				if https {
					scheme, port = "https", ":8443/"
					secret := "wildcard-cert"
					if i == 1 {
						secret = "alternate-cert"
					}
					if len(tls) != 1 || at(tls[0], "secretName") != secret || len(arr(at(tls[0], "hosts"))) != 1 || arr(at(tls[0], "hosts"))[0] != host {
						t.Fatal("wrong TLS certificate mapping")
					}
				} else if len(tls) != 0 {
					t.Fatal("TLS enabled on HTTP platform")
				}
				if at(endpoint, "url") != scheme+"://"+host+port {
					t.Fatal("wrong public URL scheme/port")
				}
			}
			servicePorts := arr(at(resources[1], "spec", "ports"))
			if at(servicePorts[0], "appProtocol") != "http" || at(servicePorts[2], "appProtocol") != nil {
				t.Fatal("private port became public HTTP")
			}
		})
	}
}
func TestPublicDomainInputValidation(t *testing.T) {
	for _, test := range []struct {
		name   string
		modify func(Object, Object)
	}{
		{"unknown-domain", func(a, p Object) { obj(arr(a["ports"])[0])["domain"] = "external.example.com" }},
		{"domain-on-private-port", func(a, p Object) { obj(arr(a["ports"])[2])["domain"] = "apps.example.test" }},
		{"invalid-domain-type", func(a, p Object) { obj(arr(a["ports"])[0])["domain"] = true }},
		{"missing-platform-domain", func(a, p Object) { delete(p, "SEALOS_DOMAIN") }},
		{"bad-https-flag", func(a, p Object) { p["DISABLE_HTTPS"] = "false" }},
		{"bad-domain-list", func(a, p Object) { p["SEALOS_USER_DOMAINS"] = "bad" }},
		{"bad-certificate", func(a, p Object) { obj(arr(p["SEALOS_USER_DOMAINS"])[0])["secretName"] = "bad/name" }},
		{"bad-url-port", func(a, p Object) { p["DOMAIN_PORT"] = "65536" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			a, p := publicAppConfig(), networkConfig()
			test.modify(a, p)
			if _, _, e := AppCreate(a, p); e == nil {
				t.Fatal("invalid public configuration accepted")
			}
		})
	}
	a := publicAppConfig()
	obj(arr(a["ports"])[0])["domain"] = " APPS.EXAMPLE.TEST. "
	if _, _, e := AppCreate(a, networkConfig()); e != nil {
		t.Fatal("valid domain normalization failed", e)
	}
}

// Decorate the real HTTP session only to corrupt read-back data. The simulator
// still processes the actual creation request and records its resources.
type networkReadback struct {
	base    Provider
	corrupt func(Object)
	writes  int
}

func (p *networkReadback) Provider(service, method, path string, body any, envelope bool) (any, error) {
	if method != "GET" {
		p.writes++
	}
	data, err := p.base.Provider(service, method, path, body, envelope)
	if err == nil && strings.HasPrefix(path, "/api/getAppByAppName?") && p.corrupt != nil {
		p.corrupt(obj(data))
	}
	return data, err
}
func TestPublicCreationAndReadback(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "https", true: "http"}[disabled], func(t *testing.T) {
			base, sim := localServer(t, "none")
			sim.DisableHTTPS = disabled
			p := &networkReadback{base: localSession(t, base)}
			o := Operations{Session: p, Namespace: "ns-demo"}
			domains, e := o.Domains()
			if e != nil || domains["https"] == disabled || len(domains["domains"].([]string)) != 2 {
				t.Fatal("domain discovery failed", e)
			}
			preview, e := o.CreateApp(publicAppConfig(), false, 0)
			if e != nil || preview["executed"] != false || p.writes != 0 || len(arr(preview["publicEndpoints"])) != 2 {
				t.Fatal("invalid public preview", e)
			}
			created, e := o.CreateApp(publicAppConfig(), true, 0)
			if e != nil {
				t.Fatal(e)
			}
			if created["publicRoutingVerified"] != true || p.writes != 1 || created["publicReachability"] == "ready" {
				t.Fatal("incorrect public creation state")
			}
			app, e := o.App("demo")
			if e != nil {
				t.Fatal(e)
			}
			b, _ := json.Marshal(app)
			if strings.Contains(string(b), "SIMULATED_PRIVATE_VALUE") {
				t.Fatal("detail exposed environment value")
			}
			if len(arr(app["networks"])) != 3 {
				t.Fatal("public or private network missing")
			}
		})
	}
}
func TestUnverifiedPublicCreationIsNotRetried(t *testing.T) {
	for _, mode := range []string{"missing", "wrong-app", "wrong-host", "wrong-service", "wrong-route", "duplicate", "private"} {
		t.Run(mode, func(t *testing.T) {
			base, _ := localServer(t, "none")
			p := &networkReadback{base: localSession(t, base)}
			p.corrupt = func(data Object) {
				rows := arr(data["networks"])
				n := obj(rows[0])
				switch mode {
				case "missing":
					data["networks"] = []any{}
				case "wrong-app":
					data["appName"] = "other"
				case "wrong-host":
					n["publicDomain"] = "other"
				case "wrong-service":
					n["serviceName"] = "other"
				case "wrong-route":
					obj(arr(n["routes"])[0])["servicePort"] = float64(9999)
				case "duplicate":
					data["networks"] = append(rows, clone(n))
				case "private":
					n["openPublicDomain"] = false
				}
			}
			o := Operations{Session: p, Namespace: "ns-demo"}
			_, e := o.CreateApp(publicAppConfig(), true, 0)
			if e == nil || publicFailure(e).Code != "WRITE_UNVERIFIED" || p.writes != 1 {
				t.Fatal("unverified routing accepted or mutation retried", e)
			}
		})
	}
}
