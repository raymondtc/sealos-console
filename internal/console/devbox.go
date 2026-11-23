package console

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const devboxTemplates = "/api/templateRepository"

var devboxTag = regexp.MustCompile("^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$")

func (s *Session) devboxAPI(method, path, bearer string, body any) (any, error) {
	if s.kubeconfig == "" {
		return nil, fail("CONFIG", "Bind a workspace before DevBox operations.")
	}
	providers, err := s.Providers()
	if err != nil {
		return nil, err
	}
	if providers["devbox"] == "" {
		return nil, fail("CONFIG", "The DevBox provider is unavailable.")
	}
	resp, err := s.HTTP.request(s.HTTP.Client, providers["devbox"], method, path, s.kubeconfig, bearer, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return decodeResponse(resp, method, path, true)
}

func (s *Session) connectDevbox(execute bool) (Object, error) {
	plan := Object{"operation": "devbox connect", "namespace": s.Namespace, "executed": false,
		"initialization": "may initialize a DevBox user and organization"}
	if !execute {
		return plan, nil
	}
	// Desktop signs the provider appToken separately from its regional token.
	// An existing CLI profile may only contain the regional token.
	appToken := s.appToken
	if appToken == "" {
		switched, err := s.Desktop("POST", "/api/auth/namespace/switch", Object{"ns_uid": s.workspaceUID})
		if err != nil {
			return nil, err
		}
		appToken = str(at(switched, "appToken"))
	}
	if appToken == "" || tokenWorkspace(appToken) != s.Namespace {
		return nil, fail("PROTOCOL", "Desktop did not return an app token for the selected workspace.")
	}
	v, err := s.devboxAPI("POST", "/api/auth/init", appToken, Object{})
	if err != nil {
		return nil, err
	}
	token := str(v)
	if token == "" || tokenWorkspace(token) != s.Namespace {
		return nil, fail("PROTOCOL", "DevBox initialization did not return a token for the selected workspace.")
	}
	if s.Profile.Devbox == nil {
		s.Profile.Devbox = map[string]string{}
	}
	s.Profile.Devbox[s.Namespace] = token
	plan["executed"] = true
	return plan, nil
}

func (s *Session) devboxToken() (string, error) {
	token := s.Profile.Devbox[s.Namespace]
	if token == "" || tokenWorkspace(token) != s.Namespace {
		return "", fail("DEVBOX_LOGIN_REQUIRED", "Run devbox connect --execute for this workspace to initialize a DevBox session.")
	}
	return token, nil
}

func (s *Session) devboxTemplateAPI(method, path string, body any) (any, error) {
	token, err := s.devboxToken()
	if err != nil {
		return nil, err
	}
	return s.devboxAPI(method, path, token, body)
}

func selectFields(v any, keys ...string) Object {
	out := Object{}
	for _, k := range keys {
		if value, ok := obj(v)[k]; ok {
			out[k] = value
		}
	}
	return out
}

func devboxRows(v any) ([]any, error) {
	rows, ok := v.([]any)
	if !ok {
		return nil, fail("PROTOCOL", "Unexpected DevBox list response.")
	}
	return rows, nil
}

func (s *Session) devboxTemplateList(scope, search string, page int) (Object, error) {
	var v any
	var err error
	if page < 1 {
		return nil, fail("INPUT", "Page must be at least 1.")
	}
	q := url.Values{"page": {strconv.Itoa(page)}, "pageSize": {"50"}, "search": {search}}
	switch scope {
	case "official":
		v, err = s.devboxAPI("GET", devboxTemplates+"/listOfficial", "", nil)
	case "public":
		v, err = s.devboxTemplateAPI("GET", devboxTemplates+"/list?"+q.Encode(), nil)
	case "private":
		v, err = s.devboxTemplateAPI("GET", devboxTemplates+"/listPrivate?"+q.Encode(), nil)
	default:
		return nil, fail("INPUT", "Template scope must be official, public, or private.")
	}
	if err != nil {
		return nil, err
	}
	rows, err := devboxRows(at(v, "templateRepositoryList"))
	if err != nil {
		return nil, err
	}
	items := []Object{}
	for _, row := range rows {
		if scope == "official" && !strings.Contains(strings.ToLower(str(at(row, "name"))), strings.ToLower(search)) {
			continue
		}
		items = append(items, selectFields(row, "uid", "name", "kind", "description", "isPublic"))
	}
	return Object{"scope": scope, "items": items, "page": selectFields(at(v, "page"), "page", "pageSize", "totalItems", "totalPage")}, nil
}

func (s *Session) devboxTemplateVersions(repo string) ([]any, error) {
	v, err := s.devboxTemplateAPI("GET", devboxTemplates+"/template/list?"+url.Values{"templateRepositoryUid": {repo}}.Encode(), nil)
	if err != nil {
		return nil, err
	}
	return devboxRows(at(v, "templateList"))
}

func (s *Session) devboxList() ([]any, error) {
	v, err := s.devboxAPI("GET", "/api/getDevboxList", "", nil)
	if err != nil {
		return nil, err
	}
	return devboxRows(v)
}

func (s *Session) devboxGet(name string) (Object, error) {
	if err := validName(name); err != nil {
		return nil, err
	}
	v, err := s.devboxAPI("GET", "/api/getDevboxByName?"+url.Values{"devboxName": {name}}.Encode(), "", nil)
	if err != nil {
		return nil, err
	}
	if at(v, "name") != name || devboxID(v) == "" {
		return nil, fail("PROTOCOL", "DevBox detail did not identify the requested instance.")
	}
	return obj(v), nil
}

func devboxID(v any) string {
	if id := str(at(v, "id")); id != "" {
		return id
	}
	return str(at(v, "uid"))
}

func devboxState(v any) string {
	if status, ok := at(v, "status").(string); ok {
		return status
	}
	return str(at(v, "status", "value"))
}

func devboxSummary(v any) Object {
	out := selectFields(v, "name", "templateUid", "templateName", "templateRepositoryName", "templateRepositoryUid", "image", "cpu", "memory", "storage", "createdAt", "createTime", "sshPort", "lastTerminatedReason")
	out["uid"], out["status"] = devboxID(v), devboxState(v)
	if t := at(v, "template"); t != nil {
		out["template"] = selectFields(t, "uid", "name")
	}
	networks := []Object{}
	for _, network := range arr(at(v, "networks")) {
		networks = append(networks, devboxPortSummary(network))
	}
	out["networks"] = networks
	return out
}

func (s *Session) devboxWait(name, id string, states []string, wait time.Duration) (Object, error) {
	end := time.Now().Add(wait)
	for {
		box, err := s.devboxGet(name)
		if err != nil {
			return nil, err
		}
		if id != "" && devboxID(box) != id {
			return nil, fail("CONFLICT", "DevBox UID changed while waiting.")
		}
		if id == "" {
			id = devboxID(box)
		}
		state := devboxState(box)
		for _, desired := range states {
			if strings.EqualFold(state, desired) {
				return devboxSummary(box), nil
			}
		}
		if strings.EqualFold(state, "Failed") || strings.EqualFold(state, "Error") {
			return nil, fail("DEVBOX_FAILED", "DevBox entered %s; inspect it with devbox get.", state)
		}
		if time.Now().After(end) {
			return nil, fail("WAIT_TIMEOUT", "DevBox %s last reported %q. Inspect it before submitting another write.", name, state)
		}
		sleepUntil(end)
	}
}

type devboxCreateOptions struct {
	name, repo, version        string
	cpu, memory, storage, port int
	public, execute            bool
	wait                       time.Duration
}

func (s *Session) createDevbox(o devboxCreateOptions) (Object, error) {
	if err := validName(o.name); err != nil {
		return nil, err
	}
	if o.repo == "" || o.version == "" || o.cpu < 1 || o.memory < 1 || o.storage < 1 || o.port < 0 || o.port > 65535 || (o.public && o.port == 0) {
		return nil, fail("INPUT", "Specify --template and --template-version, positive CPU/memory/storage, and --port for --public.")
	}
	boxes, err := s.devboxList()
	if err != nil {
		return nil, err
	}
	for _, box := range boxes {
		if at(box, "name") == o.name {
			return nil, fail("ALREADY_EXISTS", "DevBox already exists.")
		}
	}
	versions, err := s.devboxTemplateVersions(o.repo)
	if err != nil {
		return nil, err
	}
	var selected Object
	for _, v := range versions {
		if at(v, "name") == o.version || at(v, "uid") == o.version {
			if selected != nil {
				return nil, fail("INPUT", "Template version is ambiguous; use its UID.")
			}
			selected = obj(v)
		}
	}
	if selected == nil {
		return nil, fail("NOT_FOUND", "Template version was not found in the selected repository.")
	}
	uid, config, image := str(selected["uid"]), str(selected["config"]), str(selected["image"])
	var runtime Object
	if uid == "" || image == "" || json.Unmarshal([]byte(config), &runtime) != nil || runtime == nil {
		return nil, fail("PROTOCOL", "Template version is missing its UID, image, or runtime configuration.")
	}
	networks := []Object{}
	if o.port != 0 {
		network := Object{"port": o.port, "protocol": "HTTP", "openPublicDomain": o.public}
		if o.public {
			env, err := s.devboxAPI("GET", "/api/getEnv", "", nil)
			if err != nil {
				return nil, err
			}
			domain := str(at(env, "ingressDomain"))
			if !dnsName(domain) || !strings.Contains(domain, ".") {
				return nil, fail("PROTOCOL", "DevBox public ingress domain is unavailable.")
			}
			prefix, err := devboxNonce()
			if err != nil {
				return nil, err
			}
			network["publicDomain"] = prefix + "." + domain
		}
		networks = append(networks, network)
	}
	plan := Object{"operation": "devbox create", "namespace": s.Namespace, "name": o.name,
		"template": o.repo, "templateVersion": selected["name"], "templateVersionUID": uid,
		"image": image, "cpuMilli": o.cpu, "memoryMi": o.memory, "storageGi": o.storage,
		"port": o.port, "public": o.public, "executed": false}
	if !o.execute {
		return plan, nil
	}
	body := Object{"name": o.name, "templateUid": uid, "templateRepositoryUid": o.repo, "templateConfig": config,
		"image": image, "cpu": o.cpu, "memory": o.memory, "storage": o.storage, "networks": networks}
	if _, err := s.devboxAPI("POST", "/api/createDevbox", "", body); err != nil {
		return nil, err
	}
	plan["executed"] = true
	if o.wait > 0 {
		box, err := s.devboxWait(o.name, "", []string{"Running"}, o.wait)
		if err != nil {
			return nil, err
		}
		plan["devbox"] = box
	}
	return plan, nil
}

func (s *Session) devboxPower(name, mode string, execute bool, wait time.Duration) (Object, error) {
	box, err := s.devboxGet(name)
	if err != nil {
		return nil, err
	}
	path := "/api/startDevbox"
	body := Object{"devboxName": name}
	states := []string{"Running"}
	if mode != "start" {
		path = "/api/shutdownDevbox"
		body["shutdownMode"] = mode
		states = []string{mode}
	}
	plan := Object{"operation": path, "namespace": s.Namespace, "name": name, "uid": devboxID(box), "mode": mode, "status": devboxState(box), "executed": false}
	if !execute {
		return plan, nil
	}
	if _, err := s.devboxAPI("POST", path, "", body); err != nil {
		return nil, err
	}
	plan["executed"] = true
	if wait > 0 {
		box, err = s.devboxWait(name, devboxID(box), states, wait)
		if err != nil {
			return nil, err
		}
		plan["devbox"] = box
	}
	return plan, nil
}
