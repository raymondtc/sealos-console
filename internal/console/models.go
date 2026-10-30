package console

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"strings"
)

const ManagerLabel = "cloud.sealos.io/app-deploy-manager"

func checkFields(m Object, allowed, required []string) error {
	if m == nil {
		return fail("INPUT", "Configuration must be an object.")
	}
	for k := range m {
		found := false
		for _, a := range allowed {
			if k == a {
				found = true
			}
		}
		if !found {
			return fail("INPUT", "Unknown field: %s.", k)
		}
	}
	for _, k := range required {
		if _, ok := m[k]; !ok {
			return fail("INPUT", "Missing field: %s.", k)
		}
	}
	return nil
}
func validName(n string) error {
	if !regexp.MustCompile(`^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`).MatchString(n) {
		return fail("INPUT", "Name must be a lowercase DNS label starting with a letter (max 63 characters).")
	}
	return nil
}
func validImage(s string) error {
	if s == "" || len(s) > 512 || strings.ContainsAny(s, " \t\r\n?#") || strings.Contains(s, "://") {
		return fail("INPUT", "Invalid image reference.")
	}
	if strings.Contains(s, "@") && !regexp.MustCompile(`^[^@]+@sha256:[a-f0-9]{64}$`).MatchString(s) {
		return fail("INPUT", "Only sha256 image digests are supported.")
	}
	last := s[strings.LastIndex(s, "/")+1:]
	if !strings.Contains(last, ":") {
		return fail("INPUT", "Use an explicit image tag or sha256 digest.")
	}
	return nil
}
func numeric(v any, label string, low, high float64, integer bool) error {
	n, ok := v.(float64)
	if !ok {
		if x, k := v.(int); k {
			n = float64(x)
			ok = true
		}
	}
	if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n < low || n > high || (integer && math.Trunc(n) != n) {
		return fail("INPUT", "%s must be %s in [%v, %v].", label, map[bool]string{true: "an integer", false: "a number"}[integer], low, high)
	}
	return nil
}
func registry(s string) string {
	first := strings.Split(s, "/")[0]
	if strings.Contains(s, "/") && (strings.ContainsAny(first, ".:") || first == "localhost") {
		return strings.ToLower(first)
	}
	return "docker.io"
}

func normalizeApp(raw Object) (Object, error) {
	if err := checkFields(raw, []string{"name", "image", "cpu", "memoryMi", "replicas", "ports", "env", "command", "args", "storage", "imagePullSecret"}, []string{"name", "image", "cpu", "memoryMi"}); err != nil {
		return nil, err
	}
	s := obj(clone(raw))
	if err := validName(str(s["name"])); err != nil {
		return nil, err
	}
	if err := validImage(str(s["image"])); err != nil {
		return nil, err
	}
	if err := numeric(s["cpu"], "cpu cores", 0.1, 32, false); err != nil {
		return nil, err
	}
	if math.Abs(num(s["cpu"])*1000-math.Round(num(s["cpu"])*1000)) > 1e-8 {
		return nil, fail("INPUT", "CPU precision must be at least one millicore.")
	}
	if err := numeric(s["memoryMi"], "memoryMi", 128, 32768, true); err != nil {
		return nil, err
	}
	if _, ok := s["replicas"]; !ok {
		s["replicas"] = float64(1)
	}
	if err := numeric(s["replicas"], "replicas", 1, 20, true); err != nil {
		return nil, err
	}
	for _, k := range []string{"ports", "env", "command", "args", "storage"} {
		if _, ok := s[k]; !ok {
			s[k] = []any{}
		}
		if _, ok := s[k].([]any); !ok {
			return nil, fail("INPUT", "%s must be an array.", k)
		}
	}
	for _, k := range []string{"command", "args"} {
		for _, x := range arr(s[k]) {
			if _, ok := x.(string); !ok {
				return nil, fail("INPUT", "%s must contain strings.", k)
			}
		}
	}
	if v, ok := s["imagePullSecret"]; ok {
		if err := validName(str(v)); err != nil {
			return nil, err
		}
	}
	ports := map[int]bool{}
	for _, item := range arr(s["ports"]) {
		p, ok := item.(map[string]any)
		if !ok {
			return nil, fail("INPUT", "Each port must be an object.")
		}
		if e := checkFields(p, []string{"number", "public", "domain"}, []string{"number"}); e != nil {
			return nil, e
		}
		if e := numeric(p["number"], "port", 1, 65535, true); e != nil {
			return nil, e
		}
		n := int(num(p["number"]))
		if ports[n] {
			return nil, fail("INPUT", "Duplicate ports.")
		}
		ports[n] = true
		if _, ok := p["public"]; !ok {
			p["public"] = false
		}
		if _, ok := p["public"].(bool); !ok {
			return nil, fail("INPUT", "port.public must be boolean.")
		}
		if v, exists := p["domain"]; exists {
			domain, ok := v.(string)
			domain = normalizeDomain(domain)
			if !ok || p["public"] != true || !dnsName(domain) || !strings.Contains(domain, ".") {
				return nil, fail("INPUT", "port.domain requires a public port and a valid platform domain.")
			}
			p["domain"] = domain
		}
	}
	envNames := map[string]bool{}
	for _, item := range arr(s["env"]) {
		e, ok := item.(map[string]any)
		if !ok {
			return nil, fail("INPUT", "Each environment variable must be an object.")
		}
		if err := checkFields(e, []string{"name", "value", "valueFrom"}, []string{"name"}); err != nil {
			return nil, err
		}
		n := str(e["name"])
		if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(n) || envNames[n] {
			return nil, fail("INPUT", "Invalid or duplicate environment variable name.")
		}
		envNames[n] = true
		_, value := e["value"]
		ref, hasRef := e["valueFrom"]
		if value == hasRef {
			return nil, fail("INPUT", "Environment variable requires exactly one of value and valueFrom.")
		}
		if value {
			if _, ok := e["value"].(string); !ok {
				return nil, fail("INPUT", "Environment values must be strings.")
			}
		}
		if hasRef {
			r, ok := ref.(map[string]any)
			if !ok || len(r) != 1 {
				return nil, fail("INPUT", "valueFrom must contain one reference.")
			}
			for kind, item := range r {
				v, ok := item.(map[string]any)
				if !ok {
					return nil, fail("INPUT", "Reference must be an object.")
				}
				switch kind {
				case "fieldRef":
					if err := checkFields(v, []string{"fieldPath", "apiVersion"}, []string{"fieldPath"}); err != nil {
						return nil, err
					}
					if str(v["fieldPath"]) == "" {
						return nil, fail("INPUT", "fieldRef requires fieldPath.")
					}
				case "secretKeyRef", "configMapKeyRef":
					if err := checkFields(v, []string{"name", "key", "optional"}, []string{"name", "key"}); err != nil {
						return nil, err
					}
					if err := validName(str(v["name"])); err != nil {
						return nil, err
					}
					if str(v["key"]) == "" {
						return nil, fail("INPUT", "Reference requires a key.")
					}
					if optional, ok := v["optional"]; ok {
						if _, b := optional.(bool); !b {
							return nil, fail("INPUT", "Reference optional must be boolean.")
						}
					}
				default:
					return nil, fail("INPUT", "Unsupported creation reference type: %s.", kind)
				}
			}
		}
	}
	volumes, mounts := map[string]bool{}, map[string]bool{}
	for _, item := range arr(s["storage"]) {
		v, ok := item.(map[string]any)
		if !ok {
			return nil, fail("INPUT", "Storage must be an object.")
		}
		if e := checkFields(v, []string{"name", "path", "sizeGi"}, []string{"name", "path", "sizeGi"}); e != nil {
			return nil, e
		}
		if e := validName(str(v["name"])); e != nil {
			return nil, e
		}
		if e := numeric(v["sizeGi"], "sizeGi", 1, 300, true); e != nil {
			return nil, e
		}
		n, p := str(v["name"]), str(v["path"])
		if !strings.HasPrefix(p, "/") || volumes[n] || mounts[p] {
			return nil, fail("INPUT", "Storage names/paths must be unique and paths absolute.")
		}
		volumes[n] = true
		mounts[p] = true
	}
	return s, nil
}

// AppCreate builds the console's generated resource list.
// It accepts a constrained app schema, not arbitrary Kubernetes YAML.
func AppCreate(raw Object, platform Object) (Object, Object, error) {
	s, err := normalizeApp(raw)
	if err != nil {
		return nil, nil, err
	}
	n, image := str(s["name"]), str(s["image"])
	ports, env, storage := arr(s["ports"]), arr(s["env"]), arr(s["storage"])
	ingress, endpoints, err := appPublicResources(s, platform)
	if err != nil {
		return nil, nil, err
	}
	s["publicEndpoints"] = endpoints
	cpu, memory := int(math.Round(num(s["cpu"])*1000)), int(num(s["memoryMi"]))
	containerPorts, servicePorts, mounts, claims := []any{}, []any{}, []any{}, []any{}
	for _, p := range ports {
		port := int(num(at(p, "number")))
		containerPorts = append(containerPorts, Object{"name": fmt.Sprintf("tcp-%d", port), "containerPort": port})
		servicePort := Object{"name": fmt.Sprintf("tcp-%d", port), "port": port, "targetPort": port, "protocol": "TCP"}
		if at(p, "public") == true {
			servicePort["appProtocol"] = "http"
		}
		servicePorts = append(servicePorts, servicePort)
	}
	total := 0
	for _, v := range storage {
		size := int(num(at(v, "sizeGi")))
		total += size
		mounts = append(mounts, Object{"name": at(v, "name"), "mountPath": at(v, "path")})
		claims = append(claims, Object{"metadata": Object{"name": at(v, "name"), "annotations": Object{"path": at(v, "path"), "value": fmt.Sprint(size)}}, "spec": Object{"accessModes": []any{"ReadWriteOnce"}, "resources": Object{"requests": Object{"storage": fmt.Sprintf("%dGi", size)}}}})
	}
	c := Object{"name": n, "image": image, "imagePullPolicy": "Always", "env": env, "ports": containerPorts, "volumeMounts": mounts, "resources": Object{"requests": Object{"cpu": fmt.Sprintf("%dm", cpu/10), "memory": fmt.Sprintf("%dMi", memory/10)}, "limits": Object{"cpu": fmt.Sprintf("%dm", cpu), "memory": fmt.Sprintf("%dMi", memory)}}}
	for _, key := range []string{"command", "args"} {
		if len(arr(s[key])) > 0 {
			c[key] = s[key]
		}
	}
	pod := Object{"automountServiceAccountToken": false, "containers": []any{c}, "volumes": []any{}}
	if pull := str(s["imagePullSecret"]); pull != "" {
		pod["imagePullSecrets"] = []any{Object{"name": pull}}
	}
	spec := Object{"replicas": s["replicas"], "revisionHistoryLimit": 1, "selector": Object{"matchLabels": Object{"app": n}}, "template": Object{"metadata": Object{"labels": Object{"app": n}}, "spec": pod}}
	kind := "Deployment"
	if len(storage) > 0 {
		kind = "StatefulSet"
		spec["serviceName"] = n
		spec["minReadySeconds"] = 10
		spec["updateStrategy"] = Object{"type": "RollingUpdate"}
		spec["volumeClaimTemplates"] = claims
	} else {
		spec["strategy"] = Object{"type": "RollingUpdate", "rollingUpdate": Object{"maxUnavailable": 0, "maxSurge": 1}}
	}
	work := Object{"apiVersion": "apps/v1", "kind": kind, "metadata": Object{"name": n, "labels": Object{ManagerLabel: n, "app": n}, "annotations": Object{"originImageName": image, "deploy.cloud.sealos.io/minReplicas": fmt.Sprint(s["replicas"]), "deploy.cloud.sealos.io/maxReplicas": fmt.Sprint(s["replicas"]), "deploy.cloud.sealos.io/resize": fmt.Sprintf("%dGi", total), "cloud.sealos.io/owner-references": "ready"}}, "spec": spec}
	resources := []any{work}
	if len(ports) > 0 {
		resources = append(resources, Object{"apiVersion": "v1", "kind": "Service", "metadata": Object{"name": n, "labels": Object{ManagerLabel: n}}, "spec": Object{"selector": Object{"app": n}, "ports": servicePorts}})
	}
	resources = append(resources, ingress...)
	yamls := []any{}
	for _, r := range resources {
		b, _ := json.Marshal(r)
		yamls = append(yamls, string(b))
	}
	return s, Object{"yamlList": yamls, "mode": "create"}, nil
}

func DBCreate(raw Object) (Object, error) {
	if e := checkFields(raw, []string{"name", "type", "version", "cpu", "memoryMi", "storageGi", "replicas", "terminationPolicy", "backupEnabled"}, []string{"name", "type", "version", "cpu", "memoryMi", "storageGi", "replicas", "terminationPolicy"}); e != nil {
		return nil, e
	}
	if e := validName(str(raw["name"])); e != nil {
		return nil, e
	}
	if str(raw["type"]) == "" || str(raw["version"]) == "" {
		return nil, fail("INPUT", "Database type and version are required.")
	}
	for _, x := range []struct {
		k       string
		lo, hi  float64
		integer bool
	}{{"cpu", .5, 8, false}, {"memoryMi", 512, 32768, true}, {"storageGi", 1, 300, true}, {"replicas", 1, 20, true}} {
		if e := numeric(raw[x.k], x.k, x.lo, x.hi, x.integer); e != nil {
			return nil, e
		}
	}
	policy := str(raw["terminationPolicy"])
	if policy != "Delete" && policy != "WipeOut" {
		return nil, fail("INPUT", "Choose terminationPolicy Delete or WipeOut explicitly.")
	}
	backup := false
	if v, ok := raw["backupEnabled"]; ok {
		var valid bool
		backup, valid = v.(bool)
		if !valid {
			return nil, fail("INPUT", "backupEnabled must be boolean.")
		}
	}
	autoBackup := Object{"start": backup, "type": "day", "week": []any{}, "hour": "02", "minute": "00", "saveTime": 7, "saveType": "d"}
	return Object{"isEdit": false, "dbForm": Object{"dbName": raw["name"], "dbType": raw["type"], "dbVersion": raw["version"], "cpu": math.Round(num(raw["cpu"]) * 1000), "memory": raw["memoryMi"], "storage": raw["storageGi"], "replicas": raw["replicas"], "labels": Object{}, "terminationPolicy": policy, "autoBackup": autoBackup, "parameterConfig": Object{}}}, nil
}

func AppSummary(work Object) Object {
	containers := []any{}
	for _, item := range arr(at(work, "spec", "template", "spec", "containers")) {
		c := obj(item)
		names, refs := []any{}, []any{}
		for _, item := range arr(c["env"]) {
			e := obj(item)
			names = append(names, e["name"])
			for kind, v := range obj(e["valueFrom"]) {
				r := Object{"variable": e["name"], "kind": kind}
				if kind == "fieldRef" {
					r["fieldPath"] = at(v, "fieldPath")
				}
				refs = append(refs, r)
			}
		}
		containers = append(containers, Object{"name": c["name"], "image": c["image"], "resources": c["resources"], "environmentNames": names, "references": refs})
	}
	return Object{"name": at(work, "metadata", "name"), "namespace": at(work, "metadata", "namespace"), "kind": work["kind"], "resourceVersion": at(work, "metadata", "resourceVersion"), "replicas": at(work, "spec", "replicas"), "readyReplicas": num(at(work, "status", "readyReplicas")), "containers": containers, "multiContainer": len(containers) > 1}
}

func ImageUpdate(work Object, target, container string) (Object, Object, error) {
	if e := validImage(target); e != nil {
		return nil, nil, e
	}
	kind := str(work["kind"])
	if kind != "Deployment" && kind != "StatefulSet" {
		return nil, nil, fail("UNSUPPORTED", "Only Deployments and StatefulSets can be upgraded.")
	}
	meta := obj(work["metadata"])
	if str(at(meta, "labels", ManagerLabel)) != str(meta["name"]) {
		return nil, nil, fail("UNSUPPORTED", "Workload manager label must match its name.")
	}
	cs := arr(at(work, "spec", "template", "spec", "containers"))
	if len(cs) == 0 {
		return nil, nil, fail("PROTOCOL", "Workload has no containers.")
	}
	if container == "" {
		if len(cs) != 1 {
			return nil, nil, fail("INPUT", "Multiple containers: specify --container by name.")
		}
		container = str(at(cs[0], "name"))
	}
	idx, count := -1, 0
	for i, c := range cs {
		if str(at(c, "name")) == container {
			idx = i
			count++
		}
	}
	if count != 1 {
		return nil, nil, fail("INPUT", "Container name is absent or ambiguous.")
	}
	old := str(at(cs[idx], "image"))
	if len(arr(at(work, "spec", "template", "spec", "imagePullSecrets"))) > 0 && registry(old) != registry(target) {
		return nil, nil, fail("UNSUPPORTED", "Changing registries with pull secrets needs a separate credential workflow.")
	}
	diff := Object{"container": container, "from": old, "to": target}
	if old == target {
		return nil, diff, nil
	}
	rv := str(meta["resourceVersion"])
	if rv == "" {
		return nil, nil, fail("PROTOCOL", "No resourceVersion; refusing an unguarded array replacement.")
	}
	// JSON Merge Patch replaces the containers array. Preserve its entire original contents.
	updated := arr(clone(cs))
	obj(updated[idx])["image"] = target
	metadata := Object{"name": meta["name"], "namespace": meta["namespace"], "resourceVersion": rv}
	if idx == 0 {
		metadata["annotations"] = Object{"originImageName": target}
	}
	patch := Object{"metadata": metadata, "spec": Object{"template": Object{"spec": Object{"containers": updated}}}}
	return Object{"appName": meta["name"], "patch": []any{Object{"type": "patch", "kind": kind, "value": patch}}}, diff, nil
}

func sameJSON(a, b any) bool { return reflect.DeepEqual(clone(a), clone(b)) }
