package console

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Saved only by an explicit storage connect --execute, and scoped by workspace.
// Unlike regional tokens, these S3 keys authorize file operations directly.
type StorageCredential struct {
	Endpoint string `json:"endpoint"`
	Access   string `json:"accessKey"`
	Secret   string `json:"secretKey"`
}

func (s *Session) storageEndpoint(raw string) (string, error) {
	base, e := originFor(raw, s.Profile.LocalTest)
	if e != nil {
		return "", e
	}
	u, _ := url.Parse(base)
	root, _ := url.Parse(s.Profile.URL)
	if s.Profile.LocalTest && base != s.Profile.URL {
		return "", fail("CONFIG", "Mock S3 endpoint must match the local origin.")
	}
	if u.Hostname() != root.Hostname() && !strings.HasSuffix(u.Hostname(), "."+root.Hostname()) {
		return "", fail("CONFIG", "S3 endpoint is outside the platform domain.")
	}
	return base, nil
}
func (o Operations) Buckets() ([]any, error) {
	data, e := o.call("objectstorage", "POST", "/api/bucket/list", Object{}, true)
	if e != nil {
		return nil, e
	}
	rows, ok := at(data, "list").([]any)
	if !ok {
		return nil, fail("PROTOCOL", "Unexpected bucket list.")
	}
	out := []any{}
	for _, r := range rows {
		name, cr := str(at(r, "name")), str(at(r, "crName"))
		if name != strings.TrimPrefix(o.Namespace, "ns-")+"-"+cr || !dnsName(cr) || strings.Contains(cr, ".") {
			return nil, fail("PROTOCOL", "Bucket workspace/name mismatch.")
		}
		out = append(out, Object{"name": name, "crName": cr, "policy": at(r, "policy"), "ready": at(r, "isComplete") == true})
	}
	return out, nil
}
func (o Operations) Bucket(name string) (Object, error) {
	rows, e := o.Buckets()
	if e != nil {
		return nil, e
	}
	for _, b := range rows {
		if at(b, "name") == name || at(b, "crName") == name {
			return obj(b), nil
		}
	}
	return nil, fail("NOT_FOUND", "Bucket is not managed by the selected workspace.")
}
func (o Operations) CreateBucket(name string, execute bool) (Object, error) {
	full := strings.TrimPrefix(o.Namespace, "ns-") + "-" + name
	if !dnsName(name) || strings.Contains(name, ".") || len(full) > 63 || len(full) < 3 {
		return nil, fail("INPUT", "Use a lowercase bucket label; the workspace-prefixed bucket name must be 3–63 characters.")
	}
	rows, e := o.Buckets()
	if e != nil {
		return nil, e
	}
	for _, b := range rows {
		if at(b, "crName") == name {
			return nil, fail("ALREADY_EXISTS", "Bucket already exists; this command does not edit policies.")
		}
	}
	plan := Object{"operation": "storage.bucket-create", "name": full, "crName": name, "policy": "private", "executed": false, "concurrency": "Server endpoint is an upsert; preflight cannot atomically prevent a concurrent creation. No automatic retry."}
	if !execute {
		return plan, nil
	}
	if _, e = o.call("objectstorage", "POST", "/api/bucket/create", Object{"bucketName": name, "bucketPolicy": "private"}, true); e != nil {
		return nil, e
	}
	b, e := o.Bucket(name)
	if e != nil || at(b, "policy") != "private" {
		return nil, fail("WRITE_UNVERIFIED", "Bucket submission was accepted but private policy read-back failed. Inspect before retrying.")
	}
	plan["executed"] = true
	plan["bucket"] = b
	return plan, nil
}
func (s *Session) ConnectStorage(execute bool) (Object, error) {
	plan := Object{"operation": "storage.connect", "namespace": s.Namespace, "executed": false, "effect": "Fetch S3 credentials; may initialize a storage user if missing. Save credentials in the private local profile for this workspace."}
	if !execute {
		return plan, nil
	}
	data, e := s.Provider("objectstorage", "GET", "/api/user/init", nil, true)
	if e != nil {
		return nil, e
	}
	secret := at(data, "secret")
	external := str(at(secret, "external"))
	if !strings.Contains(external, "://") {
		external = "https://" + external
	}
	endpoint, e := s.storageEndpoint(external)
	if e != nil {
		return nil, e
	}
	credential := StorageCredential{Endpoint: endpoint, Access: str(at(secret, "CONSOLE_ACCESS_KEY")), Secret: str(at(secret, "CONSOLE_SECRET_KEY"))}
	if credential.Access == "" || credential.Secret == "" || num(at(secret, "specVersion")) > num(at(secret, "version")) {
		return nil, fail("PROTOCOL", "S3 credentials are missing or still rotating; no retry sent.")
	}
	if s.Profile.Storage == nil {
		s.Profile.Storage = map[string]StorageCredential{}
	}
	s.Profile.Storage[s.Namespace] = credential
	plan["executed"] = true
	plan["endpoint"] = endpoint
	return plan, nil
}

type s3Client struct {
	credential StorageCredential
	http       *http.Client
}

func (s *Session) s3() (*s3Client, error) {
	c, ok := s.Profile.Storage[s.Namespace]
	if !ok || c.Access == "" || c.Secret == "" {
		return nil, fail("STORAGE_LOGIN_REQUIRED", "Run storage connect (preview) then storage connect --execute for this workspace. Ordinary queries never initialize storage users.")
	}
	base, e := s.storageEndpoint(c.Endpoint)
	if e != nil {
		return nil, e
	}
	c.Endpoint = base
	return &s3Client{credential: c, http: s.HTTP.Client}, nil
}
func sha(s string) string { v := sha256.Sum256([]byte(s)); return hex.EncodeToString(v[:]) }
func hmac256(key []byte, value string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(value))
	return h.Sum(nil)
}
func signS3(req *http.Request, c StorageCredential, payload string, now time.Time) {
	date, stamp := now.UTC().Format("20060102"), now.UTC().Format("20060102T150405Z")
	req.Header.Set("X-Amz-Date", stamp)
	req.Header.Set("X-Amz-Content-Sha256", payload)
	headers := map[string]string{"host": req.URL.Host}
	for k, v := range req.Header {
		lower := strings.ToLower(k)
		if strings.HasPrefix(lower, "x-amz-") || lower == "if-none-match" {
			headers[lower] = strings.Join(v, ",")
		}
	}
	names := []string{}
	for k := range headers {
		names = append(names, k)
	}
	sort.Strings(names)
	canonicalHeaders := ""
	for _, k := range names {
		canonicalHeaders += k + ":" + strings.Join(strings.Fields(headers[k]), " ") + "\n"
	}
	signed := strings.Join(names, ";")
	scope := date + "/us-east-1/s3/aws4_request"
	canonical := req.Method + "\n" + req.URL.EscapedPath() + "\n" + req.URL.RawQuery + "\n" + canonicalHeaders + "\n" + signed + "\n" + payload
	key := hmac256(hmac256(hmac256(hmac256([]byte("AWS4"+c.Secret), date), "us-east-1"), "s3"), "aws4_request")
	signature := hex.EncodeToString(hmac256(key, "AWS4-HMAC-SHA256\n"+stamp+"\n"+scope+"\n"+sha(canonical)))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+c.Access+"/"+scope+", SignedHeaders="+signed+", Signature="+signature)
}
func (s *s3Client) request(method, bucket, key string, q url.Values, body io.Reader, size int64, payload string, headers map[string]string) (*http.Response, error) {
	u, e := url.Parse(s.credential.Endpoint)
	if e != nil {
		return nil, fail("CONFIG", "Invalid S3 endpoint.")
	}
	u.Path = "/" + bucket
	if key != "" {
		u.Path += "/" + key
	}
	u.RawPath = s3Path(u.Path)
	u.RawQuery = strings.ReplaceAll(q.Encode(), "+", "%20")
	req, e := http.NewRequestWithContext(context.Background(), method, u.String(), body)
	if e != nil {
		return nil, fail("INPUT", "Invalid object request.")
	}
	if body != nil {
		req.ContentLength = size
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	signS3(req, s.credential, payload, time.Now())
	res, e := s.http.Do(req)
	if e != nil {
		return nil, fail("TRANSPORT", "S3 request failed; no automatic retry. Inspect the object before retrying an upload.")
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		status := res.StatusCode
		res.Body.Close()
		if status == 412 {
			return nil, fail("ALREADY_EXISTS", "Object already exists; overwriting is not enabled.")
		}
		return nil, &Failure{Code: "S3_ERROR", Status: status, Message: fmt.Sprintf("S3 %s failed: HTTP %d. Response body suppressed.", method, status)}
	}
	return res, nil
}
func validObjectKey(key string) error {
	if key == "" || len(key) > 1024 || strings.ContainsAny(key, "\x00\r\n") {
		return fail("INPUT", "Object key must contain 1–1024 bytes without NUL/CR/LF.")
	}
	return nil
}
func (s *Session) ListObjects(bucket, prefix, continuation string, limit int) (Object, error) {
	if limit < 1 || limit > 1000 {
		return nil, fail("INPUT", "--limit must be 1–1000.")
	}
	o := Operations{Session: s, Namespace: s.Namespace}
	b, e := o.Bucket(bucket)
	if e != nil {
		return nil, e
	}
	client, e := s.s3()
	if e != nil {
		return nil, e
	}
	q := url.Values{"list-type": {"2"}, "max-keys": {fmt.Sprint(limit)}, "encoding-type": {"url"}}
	if prefix != "" {
		q.Set("prefix", prefix)
	}
	if continuation != "" {
		q.Set("continuation-token", continuation)
	}
	res, e := client.request("GET", str(b["name"]), "", q, nil, 0, sha(""), nil)
	if e != nil {
		return nil, e
	}
	defer res.Body.Close()
	var v struct {
		XMLName               xml.Name `xml:"ListBucketResult"`
		EncodingType          string
		IsTruncated           bool
		NextContinuationToken string
		Contents              []struct {
			Key                string
			Size               int64
			ETag, LastModified string
		}
	}
	if e = xml.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(&v); e != nil {
		return nil, fail("PROTOCOL", "Invalid S3 object list.")
	}
	out := []any{}
	for _, item := range v.Contents {
		key := item.Key
		if v.EncodingType == "url" {
			key, e = url.PathUnescape(key)
			if e != nil {
				return nil, fail("PROTOCOL", "Invalid encoded object key.")
			}
		}
		out = append(out, Object{"key": key, "size": item.Size, "etag": item.ETag, "lastModified": item.LastModified})
	}
	if v.IsTruncated && v.NextContinuationToken == "" {
		return nil, fail("PROTOCOL", "Truncated listing has no continuation token.")
	}
	return Object{"bucket": b["name"], "items": out, "truncated": v.IsTruncated, "nextToken": v.NextContinuationToken}, nil
}
func (s *Session) Upload(bucket, key, path string, execute bool) (Object, error) {
	if e := validObjectKey(key); e != nil {
		return nil, e
	}
	f, e := os.Open(path)
	if e != nil {
		return nil, fail("INPUT", "Cannot open upload file.")
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() {
		return nil, fail("INPUT", "Upload requires a regular file.")
	}
	if info.Size() > 1<<30 {
		return nil, fail("INPUT", "Single-request upload is limited to 1 GiB; multipart upload is not implemented.")
	}
	h := sha256.New()
	if _, e = io.Copy(h, f); e != nil {
		return nil, fail("INPUT", "Cannot hash upload file.")
	}
	digest := hex.EncodeToString(h.Sum(nil))
	o := Operations{Session: s, Namespace: s.Namespace}
	b, e := o.Bucket(bucket)
	if e != nil {
		return nil, e
	}
	client, e := s.s3()
	if e != nil {
		return nil, e
	}
	plan := Object{"operation": "storage.upload", "bucket": b["name"], "key": key, "size": info.Size(), "sha256": digest, "overwrite": false, "executed": false}
	if !execute {
		return plan, nil
	}
	res, e := client.request("PUT", str(b["name"]), key, url.Values{}, io.NewSectionReader(f, 0, info.Size()), info.Size(), digest, map[string]string{"If-None-Match": "*", "X-Amz-Meta-Sealos-Console-Sha256": digest})
	if e != nil {
		return nil, e
	}
	res.Body.Close()
	verify, e := client.request("HEAD", str(b["name"]), key, url.Values{}, nil, 0, sha(""), nil)
	if e != nil {
		return nil, fail("WRITE_UNVERIFIED", "Upload accepted but object HEAD failed. Inspect before retrying.")
	}
	defer verify.Body.Close()
	if verify.ContentLength != info.Size() || verify.Header.Get("X-Amz-Meta-Sealos-Console-Sha256") != digest {
		return nil, fail("WRITE_UNVERIFIED", "Upload accepted but size/checksum metadata read-back differs. Inspect before retrying.")
	}
	plan["executed"] = true
	plan["etag"] = verify.Header.Get("ETag")
	return plan, nil
}
func (s *Session) Download(bucket, key, path string) (Object, error) {
	if e := validObjectKey(key); e != nil {
		return nil, e
	}
	if path == "" || path == "-" {
		return nil, fail("INPUT", "Provide --output with a new local file path.")
	}
	if _, e := os.Lstat(path); e == nil || !os.IsNotExist(e) {
		return nil, fail("ALREADY_EXISTS", "Download target already exists or is inaccessible.")
	}
	o := Operations{Session: s, Namespace: s.Namespace}
	b, e := o.Bucket(bucket)
	if e != nil {
		return nil, e
	}
	client, e := s.s3()
	if e != nil {
		return nil, e
	}
	res, e := client.request("GET", str(b["name"]), key, url.Values{}, nil, 0, sha(""), nil)
	if e != nil {
		return nil, e
	}
	defer res.Body.Close()
	f, e := os.CreateTemp(filepath.Dir(path), ".sealos-download-")
	if e != nil {
		return nil, fail("FILESYSTEM", "Cannot create download file.")
	}
	defer os.Remove(f.Name())
	defer f.Close()
	h := sha256.New()
	size, e := io.Copy(io.MultiWriter(f, h), res.Body)
	if e != nil {
		return nil, fail("TRANSPORT", "Download interrupted; incomplete local file discarded.")
	}
	if res.ContentLength >= 0 && size != res.ContentLength {
		return nil, fail("TRANSPORT", "Incomplete download discarded.")
	}
	digest := hex.EncodeToString(h.Sum(nil))
	if expected := res.Header.Get("X-Amz-Meta-Sealos-Console-Sha256"); expected != "" && digest != expected {
		return nil, fail("PROTOCOL", "Downloaded checksum differs from object metadata; file discarded.")
	}
	if e = f.Close(); e != nil {
		return nil, fail("FILESYSTEM", "Cannot finish download file.")
	}
	// Same-directory hard link commits without replacing a concurrently created file.
	if e = os.Link(f.Name(), path); e != nil {
		return nil, fail("FILESYSTEM", "Cannot commit download without overwriting; target may exist or filesystem may not support hard links.")
	}
	return Object{"bucket": b["name"], "key": key, "output": path, "size": size, "sha256": digest}, nil
}

func s3Path(path string) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	for _, c := range []byte(path) {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-_.~/", rune(c)) {
			b.WriteByte(c)
		} else {
			b.WriteByte('%')
			b.WriteByte(hexDigits[c>>4])
			b.WriteByte(hexDigits[c&15])
		}
	}
	return b.String()
}
