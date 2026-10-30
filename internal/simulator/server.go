// Package simulator is an in-memory API simulator, not a Sealos or Kubernetes distribution.
// It intentionally has no dependency on the client implementation or Kubernetes libraries.
package simulator

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
)

type object = map[string]any

func objectOf(v any) object {
	m, _ := v.(map[string]any)
	if m == nil {
		return object{}
	}
	return m
}
func list(v any) []any  { a, _ := v.([]any); return a }
func text(v any) string { s, _ := v.(string); return s }
func lookup(v any, path ...string) any {
	for _, p := range path {
		v = objectOf(v)[p]
	}
	return v
}
func copyJSON(v any) any {
	b, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}
func number(v any) float64  { n, _ := v.(float64); return n }
func good(v any) (int, any) { return 200, object{"code": 200, "data": v} }
func bad(status int, message string) (int, any) {
	return status, object{"code": status, "message": message, "error": object{"code": "MOCK_REJECTED"}}
}

type Server struct {
	mu                sync.Mutex
	BaseURL           string
	Fault             string
	Delay             time.Duration
	apps              map[string]object
	dbs               map[string]object
	version           int
	token, credential string
}

func New(baseURL, fault string) *Server {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"workspaceId":"ns-demo","workspaceUid":"00000000-0000-4000-8000-000000000001"}`))
	s := &Server{BaseURL: baseURL, Fault: fault, Delay: 35 * time.Second, apps: map[string]object{}, dbs: map[string]object{}, version: 1,
		token: "mock." + payload + ".not-a-real-signature", credential: "apiVersion: v1\ncontexts:\n- context:\n    namespace: ns-demo\n  name: mock\ncurrent-context: mock\n"}
	s.apps["web"] = s.newApp("web", "nginx:1.27.4", 1)
	worker := s.newApp("worker", "busybox:1.36.0", 1)
	pod := objectOf(lookup(worker, "spec", "template", "spec"))
	main := objectOf(list(pod["containers"])[0])
	main["env"] = []any{object{"name": "POD_NAME", "valueFrom": object{"fieldRef": object{"fieldPath": "metadata.name"}}}, object{"name": "POD_IP", "valueFrom": object{"fieldRef": object{"fieldPath": "status.podIP"}}}}
	main["readinessProbe"] = object{"exec": object{"command": []any{"true"}}}
	main["volumeMounts"] = []any{object{"name": "cache", "mountPath": "/cache"}}
	pod["volumes"] = []any{object{"name": "cache", "emptyDir": object{}}}
	pod["containers"] = append(list(pod["containers"]), object{"name": "sidecar", "image": "busybox:1.36.0", "args": []any{"sleep", "3600"}})
	s.apps["worker"] = worker
	return s
}
func (s *Server) newApp(name, image string, replicas float64) object {
	w := object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": object{"name": name, "namespace": "ns-demo", "labels": object{"cloud.sealos.io/app-deploy-manager": name, "sealos-console.io/simulated": "true"}, "annotations": object{"originImageName": image}},
		"spec": object{"replicas": replicas, "selector": object{"matchLabels": object{"app": name}}, "template": object{"metadata": object{"labels": object{"app": name}}, "spec": object{"containers": []any{object{"name": name, "image": image, "resources": object{"limits": object{"cpu": "500m", "memory": "512Mi"}}}}}}}}
	s.markReady(w)
	return w
}
func (s *Server) markReady(w object) {
	s.version++
	meta := objectOf(w["metadata"])
	meta["resourceVersion"] = fmt.Sprint(s.version)
	meta["generation"] = float64(s.version)
	meta["namespace"] = "ns-demo"
	r := number(lookup(w, "spec", "replicas"))
	w["status"] = object{"observedGeneration": float64(s.version), "replicas": r, "updatedReplicas": r, "readyReplicas": r, "availableReplicas": r, "currentRevision": fmt.Sprint(s.version), "updateRevision": fmt.Sprint(s.version)}
}
func (s *Server) sortedApps() []any {
	keys := []string{}
	for k := range s.apps {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := []any{}
	for _, k := range keys {
		out = append(out, s.apps[k])
	}
	return out
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	// This service accepts only loopback traffic and uses synthetic demo credentials.
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && net.ParseIP(host) != nil && !net.ParseIP(host).IsLoopback() {
		w.WriteHeader(403)
		_ = json.NewEncoder(w).Encode(object{"code": 403})
		return
	}
	var body object
	if r.Body != nil && r.Method != "GET" {
		decoder := json.NewDecoder(io.LimitReader(r.Body, 4<<20))
		if e := decoder.Decode(&body); e != nil && e != io.EOF {
			w.WriteHeader(400)
			_ = json.NewEncoder(w).Encode(object{"code": 400})
			return
		}
	}
	credential, _ := url.QueryUnescape(r.Header.Get("Authorization"))
	s.mu.Lock()
	status, payload, mutation := s.route(r.Method, r.URL.Path, credential, body)
	payload = copyJSON(payload)
	delay := s.Delay
	fault := s.Fault
	s.mu.Unlock()
	if mutation && status >= 200 && status < 300 && fault == "timeout-after-write" {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-r.Context().Done():
			return
		}
	}
	w.WriteHeader(status)
	if status != 204 {
		_ = json.NewEncoder(w).Encode(payload)
	}
}

func (s *Server) route(method, path, credential string, body object) (int, any, bool) {
	respond := func(status int, payload any) (int, any, bool) { return status, payload, false }
	if path == "/api/auth/password" && method == "POST" {
		if body["user"] != "demo" || body["password"] != "demo" {
			a, b := bad(401, "Mock credentials are demo / demo.")
			return respond(a, b)
		}
		a, b := good(object{"token": s.token, "needInit": false})
		return respond(a, b)
	}
	if strings.HasPrefix(path, "/api/auth/") || path == "/api/desktop/getInstalledApps" || path == "/api/platform/getAppConfig" {
		if credential != s.token {
			a, b := bad(401, "Invalid mock session.")
			return respond(a, b)
		}
		switch method + " " + path {
		case "POST /api/auth/regionToken":
			a, b := good(object{"token": s.token, "appToken": "mock-app-token", "kubeconfig": s.credential})
			return respond(a, b)
		case "GET /api/auth/namespace/list":
			a, b := good(object{"namespaces": []any{object{"id": "ns-demo", "uid": "00000000-0000-4000-8000-000000000001", "teamName": "Demo Workspace", "role": 0}}})
			return respond(a, b)
		case "GET /api/auth/getKubeconfig":
			a, b := good(object{"kubeconfig": s.credential})
			return respond(a, b)
		case "GET /api/desktop/getInstalledApps":
			a, b := good([]any{object{"key": "system-applaunchpad", "data": object{"url": s.BaseURL}}, object{"key": "system-dbprovider", "data": object{"url": s.BaseURL}}})
			return respond(a, b)
		case "GET /api/platform/getAppConfig":
			a, b := good(object{"desktop": object{"layout": object{"kubeconfigEnabled": false}}, "simulation": true})
			return respond(a, b)
		}
		a, b := bad(404, "Mock route not implemented.")
		return respond(a, b)
	}
	if credential != s.credential {
		a, b := bad(401, "Invalid mock provider credential.")
		return respond(a, b)
	}
	if method == "GET" {
		switch path {
		case "/api/getApps":
			a, b := good(s.sortedApps())
			return respond(a, b)
		case "/api/platform/getInitData":
			a, b := good(object{"simulation": true})
			return respond(a, b)
		case "/api/getDBList":
			out := []any{}
			for _, db := range s.dbs {
				out = append(out, db)
			}
			a, b := good(out)
			return respond(a, b)
		case "/api/platform/getVersion":
			a, b := good(object{"postgresql": []any{object{"id": "postgresql-14.8.0"}}, "redis": []any{object{"id": "redis-7.0.6"}}})
			return respond(a, b)
		}
	}
	if method == "POST" && path == "/api/applyApp" {
		if body["mode"] != "create" {
			a, b := bad(400, "Only create mode is simulated.")
			return respond(a, b)
		}
		var work object
		for _, raw := range list(body["yamlList"]) {
			var resource object
			if json.Unmarshal([]byte(text(raw)), &resource) != nil {
				a, b := bad(400, "Simulator accepts generated JSON-as-YAML resources.")
				return respond(a, b)
			}
			kind := text(resource["kind"])
			if kind == "Deployment" || kind == "StatefulSet" {
				if work != nil {
					a, b := bad(400, "One workload per creation.")
					return respond(a, b)
				}
				work = resource
			} else if kind != "Service" {
				a, b := bad(400, "Unsupported mock resource.")
				return respond(a, b)
			}
		}
		n := text(lookup(work, "metadata", "name"))
		if n == "" || lookup(work, "metadata", "labels", "cloud.sealos.io/app-deploy-manager") != n {
			a, b := bad(400, "Missing Sealos manager label.")
			return respond(a, b)
		}
		if _, ok := s.apps[n]; ok {
			a, b := bad(409, "Already exists.")
			return respond(a, b)
		}
		s.markReady(work)
		s.apps[n] = work
		a, b := good([]any{work["kind"]})
		return a, b, true
	}
	if method == "POST" && path == "/api/updateApp" {
		if s.Fault == "conflict" {
			a, b := bad(409, "Simulated concurrent change.")
			return respond(a, b)
		}
		n := text(body["appName"])
		w, ok := s.apps[n]
		patches := list(body["patch"])
		if !ok || len(patches) != 1 {
			a, b := bad(400, "Invalid patch.")
			return respond(a, b)
		}
		item := objectOf(patches[0])
		patch := objectOf(item["value"])
		if item["type"] != "patch" || item["kind"] != w["kind"] || lookup(patch, "metadata", "name") != n || lookup(patch, "metadata", "namespace") != "ns-demo" {
			a, b := bad(400, "Invalid workload target.")
			return respond(a, b)
		}
		if lookup(patch, "metadata", "resourceVersion") != lookup(w, "metadata", "resourceVersion") {
			a, b := bad(409, "resourceVersion conflict.")
			return respond(a, b)
		}
		old, newContainers := list(lookup(w, "spec", "template", "spec", "containers")), list(lookup(patch, "spec", "template", "spec", "containers"))
		if len(old) != len(newContainers) {
			a, b := bad(400, "Container preservation failed.")
			return respond(a, b)
		}
		changed := 0
		for i, c := range old {
			before, after := objectOf(copyJSON(c)), objectOf(copyJSON(newContainers[i]))
			if before["image"] != after["image"] {
				changed++
			}
			delete(before, "image")
			delete(after, "image")
			if !reflect.DeepEqual(before, after) {
				a, b := bad(400, "Non-image container changes refused by simulator.")
				return respond(a, b)
			}
		}
		if changed != 1 || len(objectOf(lookup(patch, "spec"))) != 1 || len(objectOf(lookup(patch, "spec", "template"))) != 1 || len(objectOf(lookup(patch, "spec", "template", "spec"))) != 1 {
			a, b := bad(400, "Exactly one image change is required.")
			return respond(a, b)
		}
		objectOf(lookup(w, "spec", "template", "spec"))["containers"] = newContainers
		for k, v := range objectOf(lookup(patch, "metadata", "annotations")) {
			objectOf(lookup(w, "metadata", "annotations"))[k] = v
		}
		s.markReady(w)
		a, b := good(nil)
		return a, b, true
	}
	if method == "POST" && path == "/api/createDB" {
		if body["isEdit"] != false {
			a, b := bad(400, "Editing is not simulated.")
			return respond(a, b)
		}
		form := objectOf(body["dbForm"])
		n, kind, version := text(form["dbName"]), text(form["dbType"]), text(form["dbVersion"])
		cpu, memory, storage, replicas := number(form["cpu"]), number(form["memory"]), form["storage"], form["replicas"]
		if n == "" {
			a, b := bad(400, "Database name required.")
			return respond(a, b)
		}
		if _, ok := s.dbs[n]; ok {
			a, b := bad(409, "Already exists.")
			return respond(a, b)
		}
		s.dbs[n] = object{"name": n, "dbType": kind, "version": version, "cpu": cpu, "memory": memory, "storage": storage, "replicas": replicas, "status": object{"value": "Running"}}
		a, b := good(object{"name": n, "simulation": true})
		return a, b, true
	}
	a, b := bad(404, "Mock route not implemented.")
	return respond(a, b)
}

func Serve(listen, fault string, out io.Writer) error {
	host, _, err := net.SplitHostPort(listen)
	if err != nil || host != "127.0.0.1" {
		return errors.New("mock server must bind to 127.0.0.1:PORT")
	}
	if fault != "none" && fault != "conflict" && fault != "timeout-after-write" {
		return errors.New("fault must be none, conflict, or timeout-after-write")
	}
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return errors.New("cannot bind mock port")
	}
	defer listener.Close()
	base := "http://" + listener.Addr().String()
	handler := New(base, fault)
	_ = json.NewEncoder(out).Encode(object{"simulation": true, "url": base, "username": "demo", "password": "demo", "workspace": "ns-demo", "fault": fault, "note": "In-memory API simulation only; no containers or databases are started. Restart to reset."})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	return server.Serve(listener)
}
