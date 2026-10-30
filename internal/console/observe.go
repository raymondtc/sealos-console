package console

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func observationRange(raw string) (time.Duration, error) {
	d, e := time.ParseDuration(raw)
	if e != nil || d < time.Second || d > 24*time.Hour {
		return 0, fail("INPUT", "Use a duration between 1s and 24h.")
	}
	return d, nil
}
func (o Operations) pods(kind, name string) ([]any, error) {
	if e := validName(name); e != nil {
		return nil, e
	}
	service, path := "applaunchpad", "/api/getAppPodsByAppName?name="
	if kind == "db" {
		service, path = "dbprovider", "/api/pod/getPodsByDBName?name="
		if _, e := o.DB(name); e != nil {
			return nil, e
		}
	} else if _, e := o.Workload(name); e != nil {
		return nil, e
	}
	v, e := o.call(service, "GET", path+url.QueryEscape(name), nil, true)
	if e != nil {
		return nil, e
	}
	rows, ok := v.([]any)
	if !ok {
		return nil, fail("PROTOCOL", "Unexpected Pod list.")
	}
	for _, p := range rows {
		if at(p, "metadata", "namespace") != o.Namespace {
			return nil, fail("PROTOCOL", "Pod belongs to another namespace.")
		}
	}
	return rows, nil
}
func podName(p any) string {
	n := str(at(p, "podName"))
	if n == "" {
		n = str(at(p, "metadata", "name"))
	}
	return n
}
func (o Operations) Pods(kind, name string) (Object, error) {
	rows, e := o.pods(kind, name)
	if e != nil {
		return nil, e
	}
	out := []any{}
	for _, p := range rows {
		cs := []any{}
		for _, c := range arr(at(p, "spec", "containers")) {
			cs = append(cs, Object{"name": at(c, "name"), "image": at(c, "image"), "resources": at(c, "resources")})
		}
		out = append(out, Object{"name": podName(p), "containers": cs, "primaryContainerStatus": at(p, "status"), "primaryContainerRestarts": at(p, "restarts"), "createdAt": at(p, "metadata", "creationTimestamp")})
	}
	return Object{"namespace": o.Namespace, "items": out, "note": "Status/restarts are the console's primary-container summary, not an all-container health assessment."}, nil
}
func (o Operations) selectedPod(kind, name, selected string) (Object, error) {
	rows, e := o.pods(kind, name)
	if e != nil {
		return nil, e
	}
	if selected == "" {
		if len(rows) != 1 {
			return nil, fail("INPUT", "Specify --pod from the Pod list when there is not exactly one Pod.")
		}
		return obj(rows[0]), nil
	}
	for _, p := range rows {
		if podName(p) == selected {
			return obj(p), nil
		}
	}
	return nil, fail("INPUT", "Pod is not in this resource's Pod list.")
}
func (o Operations) Events(kind, name, selected string) (Object, error) {
	rows, e := o.pods(kind, name)
	if e != nil {
		return nil, e
	}
	out := []any{}
	found := selected == ""
	for _, p := range rows {
		n := podName(p)
		if selected != "" && selected != n {
			continue
		}
		found = true
		service, path := "applaunchpad", "/api/getPodEvents?podName="
		if kind == "db" {
			service, path = "dbprovider", "/api/pod/getPodEvents?podName="
		}
		v, e := o.call(service, "GET", path+url.QueryEscape(n), nil, true)
		if e != nil {
			return nil, e
		}
		items, ok := at(v, "items").([]any)
		if !ok {
			return nil, fail("PROTOCOL", "Unexpected event list.")
		}
		for _, item := range items {
			out = append(out, Object{"pod": n, "type": at(item, "type"), "reason": at(item, "reason"), "message": at(item, "message"), "count": at(item, "count"), "firstTimestamp": at(item, "firstTimestamp"), "lastTimestamp": at(item, "lastTimestamp")})
		}
	}
	if !found {
		return nil, fail("INPUT", "Pod is not in this resource's Pod list.")
	}
	return Object{"items": out}, nil
}

type LogStream struct {
	Body           io.ReadCloser
	cancel         context.CancelFunc
	Pod, Container string
	Simulation     bool
}

func (l *LogStream) Close() { l.Body.Close(); l.cancel() }
func (o Operations) Logs(kind, name, pod, container, since string, tail int, previous, follow bool, timeout time.Duration) (any, error) {
	if tail < 1 || tail > 1000 {
		return nil, fail("INPUT", "--tail must be 1–1000.")
	}
	duration, e := observationRange(since)
	if e != nil {
		return nil, e
	}
	p, e := o.selectedPod(kind, name, pod)
	if e != nil {
		return nil, e
	}
	pn := podName(p)
	cs := arr(at(p, "spec", "containers"))
	service, path := "applaunchpad", "/api/getPodLogs"
	body := Object{"podName": pn, "stream": follow, "logSize": tail, "previous": previous}
	if !previous {
		body["sinceTime"] = time.Now().Add(-duration).UnixMilli()
	}
	if kind == "db" {
		if container != "" {
			return nil, fail("UNSUPPORTED", "Database logs use the console-selected primary container; --container is supported for app logs.")
		}
		db, e := o.DB(name)
		if e != nil {
			return nil, e
		}
		body["dbType"] = db["dbType"]
		service, path = "dbprovider", "/api/pod/getPodLogs"
	} else {
		if container == "" {
			if len(cs) != 1 {
				return nil, fail("INPUT", "Multiple containers: specify --container.")
			}
			container = str(at(cs[0], "name"))
		}
		found := false
		for _, c := range cs {
			found = found || at(c, "name") == container
		}
		if !found {
			return nil, fail("INPUT", "Container is absent from the selected Pod.")
		}
		body["appName"] = container
	}
	if follow {
		s, ok := o.Session.(*Session)
		if !ok {
			return nil, fail("UNSUPPORTED", "Streaming requires an HTTP session.")
		}
		if timeout <= 0 {
			return nil, fail("INPUT", "Streaming --timeout must be 1–3600 seconds.")
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		r, e := s.providerLogStream(ctx, service, path, body)
		if e != nil {
			cancel()
			return nil, e
		}
		return &LogStream{Body: r, cancel: cancel, Pod: pn, Container: container, Simulation: s.Profile.LocalTest}, nil
	}
	v, e := o.call(service, "POST", path, body, true)
	if e != nil {
		return nil, e
	}
	logs, ok := v.(string)
	if v == nil {
		logs, ok = "", true
	}
	if !ok {
		return nil, fail("PROTOCOL", "Unexpected log response.")
	}
	state := "data"
	if logs == "" {
		state = "no-data"
	}
	return Object{"pod": pn, "container": container, "logs": logs, "previous": previous, "state": state}, nil
}
func (s *Session) providerLogStream(ctx context.Context, service, path string, body Object) (io.ReadCloser, error) {
	providers, e := s.Providers()
	if e != nil {
		return nil, e
	}
	base, e := originFor(providers[service], s.Profile.LocalTest)
	if e != nil {
		return nil, e
	}
	if s.kubeconfig == "" {
		return nil, fail("CONFIG", "Workspace credential missing.")
	}
	b, _ := json.Marshal(body)
	r, e := http.NewRequestWithContext(ctx, "POST", base+path, strings.NewReader(string(b)))
	if e != nil {
		return nil, fail("CONFIG", "Invalid log request.")
	}
	r.Header.Set("Authorization", strings.ReplaceAll(url.QueryEscape(s.kubeconfig), "+", "%20"))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "text/event-stream")
	client := *s.HTTP.Client
	client.Timeout = 0
	res, e := client.Do(r)
	if e != nil {
		return nil, fail("TRANSPORT", "Log stream could not be opened.")
	}
	if res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		res.Body.Close()
		return nil, fail("API_ERROR", "Log stream endpoint did not return a successful event stream.")
	}
	return res.Body, nil
}
func (o Operations) Metrics(kind, name, metric, span string) (Object, error) {
	duration, e := observationRange(span)
	if e != nil {
		return nil, e
	}
	if metric != "cpu" && metric != "memory" {
		return nil, fail("INPUT", "--metric must be cpu or memory.")
	}
	service, path := "applaunchpad", "/api/monitor/getMonitorData"
	q := url.Values{"queryName": {name}, "queryKey": {metric}, "start": {timeMillis(time.Now().Add(-duration))}, "end": {timeMillis(time.Now())}, "step": {"1m"}}
	if kind == "db" {
		db, e := o.DB(name)
		if e != nil {
			return nil, e
		}
		service = "dbprovider"
		q.Del("queryName")
		q.Set("start", timeSeconds(time.Now().Add(-duration)))
		q.Set("end", timeSeconds(time.Now()))
		q.Set("dbName", name)
		q.Set("dbType", str(db["dbType"]))
	} else if _, e := o.Workload(name); e != nil {
		return nil, e
	}
	v, e := o.call(service, "GET", path+"?"+q.Encode(), nil, true)
	if e != nil {
		return nil, e
	}
	series := []any{}
	if kind == "db" {
		r := at(v, "result")
		ys, ok := at(r, "yData").([]any)
		if !ok {
			return nil, fail("PROTOCOL", "Unexpected database metric series.")
		}
		if _, ok := at(r, "xData").([]any); !ok {
			return nil, fail("PROTOCOL", "Unexpected database metric timestamps.")
		}
		for _, s := range ys {
			series = append(series, Object{"name": at(s, "name"), "xData": at(r, "xData"), "yData": at(s, "data")})
		}
	} else {
		a, ok := v.([]any)
		if !ok {
			return nil, fail("PROTOCOL", "Unexpected metric series.")
		}
		series = a
	}
	points := 0
	for _, s := range series {
		xs, xok := at(s, "xData").([]any)
		ys, yok := at(s, "yData").([]any)
		if !xok || !yok || len(xs) != len(ys) {
			return nil, fail("PROTOCOL", "Metric timestamps and values do not align.")
		}
		for _, value := range ys {
			if value != nil {
				points++
			}
		}
	}
	state := "data"
	if points == 0 {
		state = "no-data"
	}
	return Object{"metric": metric, "state": state, "series": series, "note": "Values and series granularity follow the platform; missing series are not zero. Historical series may include replaced Pods."}, nil
}
func timeSeconds(t time.Time) string { b, _ := json.Marshal(t.Unix()); return string(b) }
func timeMillis(t time.Time) string  { b, _ := json.Marshal(t.UnixMilli()); return string(b) }
func (o Operations) Stats(name string) (Object, error) {
	rows, e := o.pods("app", name)
	if e != nil {
		return nil, e
	}
	names := []any{}
	wanted := map[string]bool{}
	for _, p := range rows {
		names = append(names, podName(p))
		wanted[podName(p)] = true
	}
	if len(names) == 0 {
		return Object{"items": []any{}, "missingPods": []any{}, "state": "no-pods"}, nil
	}
	v, e := o.call("applaunchpad", "POST", "/api/getPodsMetrics", Object{"podsName": names}, true)
	if e != nil {
		return nil, e
	}
	metrics, ok := v.([]any)
	if !ok {
		return nil, fail("PROTOCOL", "Unexpected Pod metrics.")
	}
	out := []any{}
	seen := map[string]bool{}
	for _, m := range metrics {
		n := str(at(m, "metadata", "name"))
		if at(m, "metadata", "namespace") != o.Namespace || !wanted[n] || seen[n] {
			return nil, fail("PROTOCOL", "Metrics namespace mismatch.")
		}
		seen[n] = true
		containers := []any{}
		for _, c := range arr(at(m, "containers")) {
			containers = append(containers, Object{"name": at(c, "name"), "cpu": at(c, "usage", "cpu"), "memory": at(c, "usage", "memory")})
		}
		out = append(out, Object{"pod": n, "timestamp": at(m, "timestamp"), "window": at(m, "window"), "containers": containers})
	}
	missing := []any{}
	for _, n := range names {
		if !seen[str(n)] {
			missing = append(missing, n)
		}
	}
	return Object{"items": out, "missingPods": missing, "units": "Kubernetes resource quantities; all returned containers retained"}, nil
}
