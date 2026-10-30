package simulator

import (
	"errors"
	"fmt"
	"strings"
)

// Independently interpret generated resources, so the mock detail endpoint can
// verify that the CLI created an associated Service and a usable routing graph.
func (s *Server) creationNetworks(app string, service object, ingresses []object) ([]any, error) {
	out := []any{}
	invalid := errors.New("invalid network resources")
	if service == nil {
		if len(ingresses) > 0 {
			return nil, invalid
		}
		return out, nil
	}
	name := text(lookup(service, "metadata", "name"))
	if name == "" || lookup(service, "metadata", "labels", "cloud.sealos.io/app-deploy-manager") != app || lookup(service, "spec", "selector", "app") != app {
		return nil, invalid
	}
	matched := 0
	hosts := map[string]bool{}
	for _, p := range list(lookup(service, "spec", "ports")) {
		port := number(lookup(p, "port"))
		if port < 1 || lookup(p, "protocol") != "TCP" || number(lookup(p, "targetPort")) != port {
			return nil, invalid
		}
		n := object{"serviceName": name, "port": port, "protocol": "TCP", "appProtocol": "", "openNodePort": false, "openPublicDomain": false, "networkName": "", "domain": "apps.example.test", "publicDomain": "", "customDomain": "", "routes": []any{}}
		for _, ingress := range ingresses {
			rules := list(lookup(ingress, "spec", "rules"))
			if len(rules) != 1 {
				return nil, invalid
			}
			paths := list(lookup(rules[0], "http", "paths"))
			if len(paths) != 1 {
				return nil, invalid
			}
			path := paths[0]
			if number(lookup(path, "backend", "service", "port", "number")) != port {
				continue
			}
			if n["openPublicDomain"] == true {
				return nil, invalid
			}
			prefix := text(lookup(ingress, "metadata", "labels", "cloud.sealos.io/app-deploy-manager-domain"))
			host := text(lookup(rules[0], "host"))
			parts := strings.SplitN(host, ".", 2)
			if len(parts) != 2 || parts[0] != prefix || prefix == "" || hosts[host] {
				return nil, invalid
			}
			domain := parts[1]
			secret := "wildcard-cert"
			switch domain {
			case "apps.example.test":
			case "alt.example.test":
				secret = "alternate-cert"
			default:
				return nil, invalid
			}
			if ingress["apiVersion"] != "networking.k8s.io/v1" || lookup(ingress, "metadata", "labels", "cloud.sealos.io/app-deploy-manager") != app || lookup(ingress, "metadata", "labels", "cloud.sealos.io/app-deploy-manager-port") != fmt.Sprint(port) || lookup(ingress, "metadata", "annotations", "kubernetes.io/ingress.class") != "nginx" || lookup(ingress, "metadata", "annotations", "nginx.ingress.kubernetes.io/backend-protocol") != "HTTP" || lookup(p, "appProtocol") != "http" {
				return nil, invalid
			}
			if lookup(path, "path") != "/" || lookup(path, "pathType") != "Prefix" || lookup(path, "backend", "service", "name") != name {
				return nil, invalid
			}
			tls := list(lookup(ingress, "spec", "tls"))
			if s.DisableHTTPS {
				if len(tls) > 0 {
					return nil, invalid
				}
			} else {
				if len(tls) != 1 || lookup(tls[0], "secretName") != secret {
					return nil, invalid
				}
				names := list(lookup(tls[0], "hosts"))
				if len(names) != 1 || names[0] != host {
					return nil, invalid
				}
			}
			for _, existing := range s.networks {
				for _, network := range existing {
					if lookup(network, "publicDomain") == prefix && lookup(network, "domain") == domain {
						return nil, invalid
					}
				}
			}
			matched++
			hosts[host] = true
			n["networkName"] = lookup(ingress, "metadata", "name")
			n["publicDomain"] = prefix
			n["domain"] = domain
			n["appProtocol"] = "HTTP"
			n["openPublicDomain"] = true
			n["routes"] = []any{object{"path": "/", "pathType": "Prefix", "serviceName": name, "servicePort": port}}
		}
		out = append(out, n)
	}
	if matched != len(ingresses) {
		return nil, invalid
	}
	return out, nil
}
