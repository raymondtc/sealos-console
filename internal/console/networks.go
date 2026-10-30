package console

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const publicDomainLabel = "cloud.sealos.io/app-deploy-manager-domain"
const publicPortLabel = "cloud.sealos.io/app-deploy-manager-port"

type publicSettings struct {
	domain       string
	certificates map[string]string
	https        bool
	port         string
}

func dnsName(s string) bool {
	if len(s) == 0 || len(s) > 253 {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if len(label) > 63 || !regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`).MatchString(label) {
			return false
		}
	}
	return true
}
func normalizeDomain(s string) string {
	return strings.TrimRight(strings.ToLower(strings.TrimSpace(s)), ".")
}

func readPublicSettings(raw Object) (publicSettings, error) {
	s := publicSettings{domain: normalizeDomain(str(raw["SEALOS_DOMAIN"])), certificates: map[string]string{}}
	disabled, ok := raw["DISABLE_HTTPS"].(bool)
	if !ok || !dnsName(s.domain) || !strings.Contains(s.domain, ".") {
		return s, fail("PROTOCOL", "Platform public-domain or HTTPS settings are missing or invalid.")
	}
	s.https = !disabled
	s.certificates[s.domain] = "wildcard-cert"
	if domains, exists := raw["SEALOS_USER_DOMAINS"]; exists && domains != nil {
		rows, ok := domains.([]any)
		if !ok {
			return s, fail("PROTOCOL", "Invalid platform domain list.")
		}
		seen := map[string]string{}
		for _, item := range rows {
			domain := normalizeDomain(str(at(item, "name")))
			if !dnsName(domain) || !strings.Contains(domain, ".") {
				return s, fail("PROTOCOL", "Invalid platform domain.")
			}
			secret := ""
			if value, exists := obj(item)["secretName"]; exists && value != nil {
				var ok bool
				secret, ok = value.(string)
				if !ok {
					return s, fail("PROTOCOL", "Invalid platform certificate reference.")
				}
			}
			if secret == "" {
				secret = "wildcard-cert"
			}
			if !dnsName(secret) {
				return s, fail("PROTOCOL", "Invalid platform certificate reference.")
			}
			if previous, exists := seen[domain]; exists && previous != secret {
				return s, fail("PROTOCOL", "Ambiguous platform certificate mapping.")
			}
			seen[domain] = secret
			s.certificates[domain] = secret
		}
	}
	key := "DOMAIN_PORT"
	if !s.https {
		key = "HTTP_PORT"
	}
	if v, exists := raw[key]; exists && v != nil {
		port, ok := v.(string)
		if !ok {
			return s, fail("PROTOCOL", "Invalid public URL port.")
		}
		if port != "" {
			n, e := strconv.Atoi(port)
			if e != nil || n < 1 || n > 65535 {
				return s, fail("PROTOCOL", "Invalid public URL port.")
			}
			if !(s.https && n == 443) && !(!s.https && n == 80) {
				s.port = strconv.Itoa(n)
			}
		}
	}
	return s, nil
}
func (s publicSettings) url(host string) string {
	scheme := "https"
	if !s.https {
		scheme = "http"
	}
	if s.port != "" {
		host += ":" + s.port
	}
	return (&url.URL{Scheme: scheme, Host: host, Path: "/"}).String()
}

// Match the console's HTTP ingress generator: platform-managed domains only,
// one prefix per public port, and platform certificate references. No custom
// domain, arbitrary annotation, NodePort, or OpenAPI fallback is accepted.
func appPublicResources(app Object, platform Object) ([]any, []any, error) {
	resources, endpoints := []any{}, []any{}
	hasPublic := false
	for _, p := range arr(app["ports"]) {
		hasPublic = hasPublic || at(p, "public") == true
	}
	if !hasPublic {
		return resources, endpoints, nil
	}
	settings, err := readPublicSettings(platform)
	if err != nil {
		return nil, nil, err
	}
	for _, p := range arr(app["ports"]) {
		if at(p, "public") != true {
			continue
		}
		domain := str(at(p, "domain"))
		if domain == "" {
			domain = settings.domain
		}
		secret, ok := settings.certificates[domain]
		if !ok {
			return nil, nil, fail("INPUT", "Requested domain is not advertised by this platform; use app domains.")
		}
		random := make([]byte, 10)
		if _, err := rand.Read(random); err != nil {
			return nil, nil, fail("CLIENT_ERROR", "Cannot generate public endpoint prefix.")
		}
		prefix := hex.EncodeToString(random)
		host := prefix + "." + domain
		if !dnsName(host) {
			return nil, nil, fail("INPUT", "Platform domain is too long for a generated endpoint.")
		}
		networkName := "network-" + prefix
		port := int(num(at(p, "number")))
		name := str(app["name"])
		annotations := Object{
			"kubernetes.io/ingress.class":                         "nginx",
			"nginx.ingress.kubernetes.io/proxy-body-size":         "32m",
			"nginx.ingress.kubernetes.io/backend-protocol":        "HTTP",
			"nginx.ingress.kubernetes.io/client-body-buffer-size": "64k",
			"nginx.ingress.kubernetes.io/proxy-buffer-size":       "64k",
			"nginx.ingress.kubernetes.io/proxy-send-timeout":      "300",
			"nginx.ingress.kubernetes.io/proxy-read-timeout":      "300",
			"nginx.ingress.kubernetes.io/server-snippet":          "client_header_buffer_size 64k;\nlarge_client_header_buffers 4 128k;\n",
		}
		spec := Object{"rules": []any{Object{"host": host, "http": Object{"paths": []any{Object{"path": "/", "pathType": "Prefix", "backend": Object{"service": Object{"name": name, "port": Object{"number": port}}}}}}}}}
		if settings.https {
			spec["tls"] = []any{Object{"hosts": []any{host}, "secretName": secret}}
		}
		resources = append(resources, Object{"apiVersion": "networking.k8s.io/v1", "kind": "Ingress", "metadata": Object{"name": networkName, "labels": Object{ManagerLabel: name, publicDomainLabel: prefix, publicPortLabel: fmt.Sprint(port)}, "annotations": annotations}, "spec": spec})
		endpoints = append(endpoints, Object{"networkName": networkName, "port": port, "domain": domain, "publicDomain": prefix, "host": host, "url": settings.url(host), "https": settings.https, "serviceName": name})
	}
	return resources, endpoints, nil
}

func (o Operations) Domains() (Object, error) {
	data, err := o.call("applaunchpad", "GET", "/api/platform/getInitData", nil, true)
	if err != nil {
		return nil, err
	}
	s, err := readPublicSettings(obj(data))
	if err != nil {
		return nil, err
	}
	domains := []string{}
	for domain := range s.certificates {
		domains = append(domains, domain)
	}
	sort.Strings(domains)
	return Object{"defaultDomain": s.domain, "domains": domains, "https": s.https, "urlPort": s.port}, nil
}

// The detail endpoint contains environment values and other private fields.
// Return only its network projection, never the complete response.
func (o Operations) Networks(name string) ([]any, error) {
	if err := validName(name); err != nil {
		return nil, err
	}
	data, err := o.call("applaunchpad", "GET", "/api/getAppByAppName?appName="+url.QueryEscape(name), nil, true)
	if err != nil {
		return nil, err
	}
	if at(data, "appName") != name {
		return nil, fail("PROTOCOL", "Network detail returned a different app.")
	}
	rows, ok := at(data, "networks").([]any)
	if !ok {
		return nil, fail("PROTOCOL", "Unexpected app network list.")
	}
	out := []any{}
	for _, row := range rows {
		n := Object{}
		for _, key := range []string{"networkName", "serviceName", "port", "protocol", "appProtocol", "openPublicDomain", "openNodePort", "publicDomain", "domain", "customDomain"} {
			if value, ok := obj(row)[key]; ok {
				n[key] = value
			}
		}
		routes := []any{}
		for _, route := range arr(at(row, "routes")) {
			r := Object{}
			for _, key := range []string{"path", "pathType", "serviceName", "servicePort"} {
				if value, ok := obj(route)[key]; ok {
					r[key] = value
				}
			}
			routes = append(routes, r)
		}
		n["routes"] = routes
		out = append(out, n)
	}
	return out, nil
}
func verifyPublicEndpoints(expected, networks []any) bool {
	for _, endpoint := range expected {
		matches := 0
		for _, n := range networks {
			if at(n, "networkName") != at(endpoint, "networkName") {
				continue
			}
			if at(n, "openPublicDomain") != true || at(n, "openNodePort") == true || str(at(n, "customDomain")) != "" || at(n, "protocol") != "TCP" || at(n, "appProtocol") != "HTTP" || at(n, "domain") != at(endpoint, "domain") || at(n, "publicDomain") != at(endpoint, "publicDomain") || num(at(n, "port")) != num(at(endpoint, "port")) || at(n, "serviceName") != at(endpoint, "serviceName") {
				return false
			}
			routes := arr(at(n, "routes"))
			if len(routes) != 1 {
				return false
			}
			r := routes[0]
			if at(r, "path") != "/" || at(r, "pathType") != "Prefix" || at(r, "serviceName") != at(endpoint, "serviceName") || num(at(r, "servicePort")) != num(at(endpoint, "port")) {
				return false
			}
			matches++
		}
		if matches != 1 {
			return false
		}
	}
	return true
}
