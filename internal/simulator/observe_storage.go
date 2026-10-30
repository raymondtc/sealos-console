package simulator

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

type storedObject struct {
	Data     []byte
	Checksum string
}

func (s *Server) observeAndStorage(method, path string, body object) (int, any, bool, bool) {
	u, _ := url.Parse(path)
	q := u.Query()
	ok := func(v any) (int, any, bool, bool) { return 200, object{"code": 200, "data": v}, false, true }
	if method == "POST" && u.Path == "/api/bucket/list" {
		rows := []any{}
		for _, b := range s.buckets {
			rows = append(rows, b)
		}
		return ok(object{"list": rows})
	}
	if method == "GET" && u.Path == "/api/user/init" {
		a, b, _, _ := ok(object{"secret": object{"external": s.BaseURL, "CONSOLE_ACCESS_KEY": "mock-access", "CONSOLE_SECRET_KEY": "mock-secret", "specVersion": 0, "version": 0}})
		return a, b, true, true
	}
	if method == "POST" && u.Path == "/api/bucket/create" {
		name := text(body["bucketName"])
		if name == "" || body["bucketPolicy"] != "private" {
			return 400, object{"code": 400}, false, true
		}
		s.buckets[name] = object{"name": "demo-" + name, "crName": name, "policy": "private", "isComplete": true}
		if s.objects["demo-"+name] == nil {
			s.objects["demo-"+name] = map[string]storedObject{}
		}
		return 200, object{"code": 200, "data": nil}, true, true
	}
	switch u.Path {
	case "/api/getAppPodsByAppName", "/api/pod/getPodsByDBName":
		if method != "GET" {
			break
		}
		name := q.Get("name")
		w := s.apps[name]
		if u.Path == "/api/pod/getPodsByDBName" {
			if db, exists := s.dbs[name]; exists {
				w = object{"spec": object{"template": object{"spec": object{"containers": []any{object{"name": db["dbType"], "image": "example:1"}}}}}}
			}
		}
		if w == nil {
			return ok([]any{})
		}
		return ok([]any{object{"metadata": object{"name": name + "-pod", "namespace": "ns-demo", "creationTimestamp": "2026-01-01T00:00:00Z"}, "podName": name + "-pod", "spec": lookup(w, "spec", "template", "spec"), "status": object{"value": "running"}, "restarts": 0}})
	case "/api/getPodEvents", "/api/pod/getPodEvents":
		if method == "GET" {
			return ok(object{"items": []any{object{"type": "Normal", "reason": "Started", "message": "Simulated container started", "count": 1}}})
		}
	case "/api/getPodLogs", "/api/pod/getPodLogs":
		if method == "POST" {
			if body["stream"] == true {
				return 200, "simulated log line\n", false, true
			}
			return ok("simulated log line\n")
		}
	case "/api/getPodsMetrics":
		if method == "POST" {
			rows := []any{}
			for _, name := range list(body["podsName"]) {
				app := strings.TrimSuffix(text(name), "-pod")
				cs := []any{}
				for _, c := range list(lookup(s.apps[app], "spec", "template", "spec", "containers")) {
					cs = append(cs, object{"name": lookup(c, "name"), "usage": object{"cpu": "12000000n", "memory": "32768Ki"}})
				}
				rows = append(rows, object{"metadata": object{"namespace": "ns-demo", "name": name}, "timestamp": "2026-01-01T00:00:00Z", "window": "30s", "containers": cs})
			}
			return ok(rows)
		}
	case "/api/monitor/getMonitorData":
		if method == "GET" {
			start, _ := strconv.ParseInt(q.Get("start"), 10, 64)
			if q.Get("dbName") != "" {
				if start < 1000000000 || start > 10000000000 {
					return 400, object{"code": 400}, false, true
				}
				return ok(object{"result": object{"xData": []any{1767225600, 1767225660}, "yData": []any{object{"name": q.Get("dbName") + "-pod", "data": []any{1.1, 1.2}}}}})
			}
			if start < 1000000000000 {
				return 400, object{"code": 400}, false, true
			}
			return ok([]any{object{"name": q.Get("queryName") + "-pod", "xData": []any{1767225600, 1767225660}, "yData": []any{"1.1", "1.2"}}})
		}
	}
	return 0, nil, false, false
}
func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func sign(key []byte, value string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(value))
	return h.Sum(nil)
}
func verifySignature(r *http.Request, payload []byte) bool {
	auth := r.Header.Get("Authorization")
	prefix := "AWS4-HMAC-SHA256 Credential=mock-access/"
	if !strings.HasPrefix(auth, prefix) {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(auth, prefix), ", ")
	if len(parts) != 3 {
		return false
	}
	scope := parts[0]
	keys := strings.TrimPrefix(parts[1], "SignedHeaders=")
	signature := strings.TrimPrefix(parts[2], "Signature=")
	headers := ""
	for _, k := range strings.Split(keys, ";") {
		v := r.Header.Get(k)
		if k == "host" {
			v = r.Host
		}
		headers += k + ":" + strings.Join(strings.Fields(v), " ") + "\n"
	}
	hash := digest(payload)
	if r.Header.Get("X-Amz-Content-Sha256") != hash {
		return false
	}
	canonical := strings.Join([]string{r.Method, r.URL.EscapedPath(), r.URL.RawQuery, headers, keys, hash}, "\n")
	date := strings.Split(scope, "/")[0]
	key := sign(sign(sign(sign([]byte("AWS4mock-secret"), date), "us-east-1"), "s3"), "aws4_request")
	expected := hex.EncodeToString(sign(key, "AWS4-HMAC-SHA256\n"+r.Header.Get("X-Amz-Date")+"\n"+scope+"\n"+digest([]byte(canonical))))
	return hmac.Equal([]byte(signature), []byte(expected))
}
func (s *Server) serveS3(w http.ResponseWriter, r *http.Request) {
	host, _, e := net.SplitHostPort(r.RemoteAddr)
	if e != nil || !net.ParseIP(host).IsLoopback() {
		w.WriteHeader(403)
		return
	}
	body, e := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if e != nil || !verifySignature(r, body) {
		w.WriteHeader(403)
		return
	}
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)
	bucket := parts[0]
	key := ""
	if len(parts) == 2 {
		key = parts[1]
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	objects, exists := s.objects[bucket]
	if !exists {
		w.WriteHeader(404)
		return
	}
	if r.Method == "GET" && r.URL.Query().Get("list-type") == "2" {
		type item struct {
			Key          string
			Size         int
			ETag         string
			LastModified string
		}
		result := struct {
			XMLName               xml.Name `xml:"ListBucketResult"`
			EncodingType          string
			IsTruncated           bool
			NextContinuationToken string
			Contents              []item
		}{EncodingType: "url", Contents: []item{}}
		keys := []string{}
		for k := range objects {
			if strings.HasPrefix(k, r.URL.Query().Get("prefix")) {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		start := 0
		if token := r.URL.Query().Get("continuation-token"); token != "" {
			fmt.Sscan(token, &start)
		}
		limit := 100
		fmt.Sscan(r.URL.Query().Get("max-keys"), &limit)
		if start < 0 || start > len(keys) || limit < 1 {
			w.WriteHeader(400)
			return
		}
		end := start + limit
		if end > len(keys) {
			end = len(keys)
		}
		if end < len(keys) {
			result.IsTruncated = true
			result.NextContinuationToken = fmt.Sprint(end)
		}
		for _, k := range keys[start:end] {
			result.Contents = append(result.Contents, item{Key: url.PathEscape(k), Size: len(objects[k].Data), ETag: "mock-etag", LastModified: "2026-01-01T00:00:00Z"})
		}
		w.Header().Set("Content-Type", "application/xml")
		xml.NewEncoder(w).Encode(result)
		return
	}
	obj, exists := objects[key]
	switch r.Method {
	case "PUT":
		if key == "" {
			w.WriteHeader(400)
			return
		}
		if r.Header.Get("If-None-Match") != "*" {
			w.WriteHeader(400)
			return
		}
		if exists {
			w.WriteHeader(412)
			return
		}
		objects[key] = storedObject{Data: body, Checksum: r.Header.Get("X-Amz-Meta-Sealos-Console-Sha256")}
		w.Header().Set("ETag", "mock-etag")
		if s.Fault == "timeout-after-write" {
			time.Sleep(s.Delay)
		}
		w.WriteHeader(200)
	case "HEAD", "GET":
		if !exists {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(obj.Data)))
		w.Header().Set("ETag", "mock-etag")
		if obj.Checksum != "" {
			w.Header().Set("X-Amz-Meta-Sealos-Console-Sha256", obj.Checksum)
		}
		if r.Method == "GET" {
			w.Write(obj.Data)
		}
	default:
		w.WriteHeader(405)
	}
}
