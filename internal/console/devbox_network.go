package console

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
)

func devboxPortSummary(network any) Object {
	return selectFields(network, "port", "portName", "protocol", "openPublicDomain", "publicDomain", "domain", "customDomain", "networkName")
}

func (s *Session) devboxNetworkEnv() (Object, error) {
	v, err := s.devboxAPI("GET", "/api/getEnv", "", nil)
	if err != nil {
		return nil, err
	}
	if ns := str(at(v, "namespace")); ns != "" && ns != s.Namespace {
		return nil, fail("PROTOCOL", "DevBox network settings returned a different workspace.")
	}
	return obj(v), nil
}

func devboxHostname(network, env Object) string {
	host := normalizeDomain(str(network["customDomain"]))
	if host == "" {
		host = normalizeDomain(str(network["publicDomain"]))
		domain := normalizeDomain(str(network["domain"]))
		if domain == "" {
			domain = normalizeDomain(str(env["ingressDomain"]))
		}
		if host != "" && !strings.Contains(host, ".") && domain != "" {
			host += "." + domain
		}
	}
	if dnsName(host) && strings.Contains(host, ".") {
		return host
	}
	return ""
}

func (s *Session) devboxPorts(name string) (Object, error) {
	box, err := s.devboxGet(name)
	if err != nil {
		return nil, err
	}
	networks, err := devboxRows(box["networks"])
	if err != nil {
		return nil, err
	}
	items := make([]Object, 0, len(networks))
	var env Object
	for _, row := range networks {
		item := devboxPortSummary(row)
		if item["openPublicDomain"] == true {
			host := devboxHostname(item, env)
			if host == "" && env == nil {
				env, err = s.devboxNetworkEnv()
				if err != nil {
					return nil, err
				}
				host = devboxHostname(item, env)
			}
			if host == "" {
				return nil, fail("PROTOCOL", "Cannot resolve the public hostname for DevBox port %v.", item["port"])
			}
			item["publicURL"] = "https://" + host + "/"
		}
		items = append(items, item)
	}
	return Object{"namespace": s.Namespace, "name": name, "uid": devboxID(box), "items": items}, nil
}

func devboxPort(box Object, port int) (Object, error) {
	networks, err := devboxRows(box["networks"])
	if err != nil {
		return nil, err
	}
	var found Object
	for _, row := range networks {
		if num(at(row, "port")) != float64(port) {
			continue
		}
		if found != nil {
			return nil, fail("CONFLICT", "DevBox port %d is ambiguous.", port)
		}
		found = devboxPortSummary(row)
	}
	if found == nil {
		return nil, fail("NOT_FOUND", "DevBox port %d is not configured.", port)
	}
	if _, ok := found["openPublicDomain"].(bool); !ok {
		return nil, fail("PROTOCOL", "DevBox port %d is missing its public access state.", port)
	}
	return found, nil
}

func devboxHTTPIngress(name, networkName, host, label, secret string, port int) Object {
	return Object{
		"apiVersion": "networking.k8s.io/v1",
		"kind":       "Ingress",
		"metadata": Object{
			"name":   networkName,
			"labels": Object{"cloud.sealos.io/devbox-manager": name, publicDomainLabel: label},
			"annotations": Object{
				"kubernetes.io/ingress.class":                         "nginx",
				"nginx.ingress.kubernetes.io/ssl-redirect":            "false",
				"nginx.ingress.kubernetes.io/backend-protocol":        "HTTP",
				"nginx.ingress.kubernetes.io/proxy-body-size":         "32m",
				"nginx.ingress.kubernetes.io/client-body-buffer-size": "64k",
				"nginx.ingress.kubernetes.io/proxy-buffer-size":       "64k",
				"nginx.ingress.kubernetes.io/proxy-send-timeout":      "300",
				"nginx.ingress.kubernetes.io/proxy-read-timeout":      "300",
				"nginx.ingress.kubernetes.io/server-snippet":          "client_header_buffer_size 64k;\nlarge_client_header_buffers 4 128k;\n",
			},
		},
		"spec": Object{
			"rules": []Object{{"host": host, "http": Object{"paths": []Object{{
				"path": "/", "pathType": "Prefix",
				"backend": Object{"service": Object{"name": name, "port": Object{"number": port}}},
			}}}}},
			"tls": []Object{{"hosts": []string{host}, "secretName": secret}},
		},
	}
}

func (s *Session) setDevboxPublic(name string, port int, enabled, execute bool) (Object, error) {
	if port < 1 || port > 65535 {
		return nil, fail("INPUT", "Specify --port between 1 and 65535.")
	}
	box, err := s.devboxGet(name)
	if err != nil {
		return nil, err
	}
	current, err := devboxPort(box, port)
	if err != nil {
		return nil, err
	}
	uid := devboxID(box)
	change := current["openPublicDomain"] != enabled
	plan := Object{"operation": "devbox public", "namespace": s.Namespace, "name": name, "uid": uid,
		"port": port, "before": current, "public": enabled, "changeRequired": change, "executed": false}
	if !change {
		if host := devboxHostname(current, nil); enabled && host != "" {
			plan["publicURL"] = "https://" + host + "/"
		}
		return plan, nil
	}
	if protocol := str(current["protocol"]); protocol != "" && protocol != "HTTP" {
		return nil, fail("INPUT", "Only HTTP DevBox ports support this toggle.")
	}
	if strings.HasPrefix(str(current["portName"]), "webide-") || str(current["customDomain"]) != "" {
		return nil, fail("INPUT", "Manage WebIDE ports and custom-domain routes in the DevBox Console.")
	}

	// Match the Console's updateDevbox contract. Only submit the selected
	// port's ingress change. Its Service and all other resources stay intact.
	networkName := str(current["networkName"])
	patch := Object{"kind": "Ingress", "type": "delete", "name": networkName}
	var host string
	var env Object
	if enabled {
		if networkName != "" {
			return nil, fail("CONFLICT", "This port already has an ingress. Inspect it in the DevBox Console.")
		}
		env, err = s.devboxNetworkEnv()
		if err != nil {
			return nil, err
		}
		domain := normalizeDomain(str(env["ingressDomain"]))
		secret := str(env["ingressSecret"])
		if !dnsName(domain) || !strings.Contains(domain, ".") || !dnsName(secret) {
			return nil, fail("PROTOCOL", "DevBox ingress domain or certificate reference is unavailable.")
		}
		// A stable, instance-specific ingress name makes simultaneous enable
		// requests contend for one ingress and restores the same preview URL.
		id := sha256.Sum256([]byte(uid + ":" + strconv.Itoa(port)))
		networkName = "cli-" + hex.EncodeToString(id[:12])
		host = networkName + "." + domain
		label := host
		if env["customPublicDomainPrefixEnabled"] == "true" || env["customPublicDomainPrefixEnabled"] == true {
			label = networkName
		}
		if !dnsName(host) || len(label) > 63 {
			return nil, fail("PROTOCOL", "Generated DevBox hostname does not fit the platform's ingress labels.")
		}
		plan["publicURL"] = "https://" + host + "/"
		if !execute {
			return plan, nil
		}
		// JSON is a YAML-compatible serialization of this Console manifest.
		manifest, err := json.Marshal(devboxHTTPIngress(name, networkName, host, label, secret, port))
		if err != nil {
			return nil, err
		}
		patch = Object{"kind": "Ingress", "type": "create", "value": string(manifest)}
	} else if !dnsName(networkName) {
		return nil, fail("PROTOCOL", "Cannot identify the public ingress for DevBox port %d.", port)
	}
	if !execute {
		return plan, nil
	}
	if _, err := s.devboxAPI("POST", "/api/updateDevbox", "", Object{"devboxName": name, "patch": []Object{patch}}); err != nil {
		return nil, err
	}
	updated, err := s.devboxGet(name)
	if err != nil {
		return nil, fail("VERIFY", "DevBox network update was accepted but read-back failed. Inspect its ports before retrying. %v", err)
	}
	after, err := devboxPort(updated, port)
	if err != nil || devboxID(updated) != uid || after["openPublicDomain"] != enabled ||
		enabled && (after["networkName"] != networkName || devboxHostname(after, env) != host) ||
		!enabled && (str(after["networkName"]) != "" || str(after["customDomain"]) != "") {
		return nil, fail("VERIFY", "DevBox network update was accepted but the expected port state could not be verified. Inspect its ports before retrying.")
	}
	plan["executed"], plan["after"] = true, after
	return plan, nil
}
