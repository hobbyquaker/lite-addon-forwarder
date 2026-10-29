package roundtrip

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hobbyquaker/lite-addon-forwarder/internal/config"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/record"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/target"
)

// TestRoundTripStores proves the 0.2.0 targets against OpenSearch 2, Elasticsearch 8 and
// VictoriaMetrics in docker. FORWARDER_DOCKER=1 runs it; FORWARDER_DOCKER_STORES narrows it
// (a comma list of opensearch, elasticsearch, victoriametrics).
func TestRoundTripStores(t *testing.T) {
	if os.Getenv("FORWARDER_DOCKER") == "" {
		t.Skip("FORWARDER_DOCKER not set")
	}
	only := os.Getenv("FORWARDER_DOCKER_STORES")
	run := func(name string, f func(*testing.T)) {
		if only == "" || strings.Contains(","+only+",", ","+name+",") {
			t.Run(name, f)
		}
	}
	run("opensearch", func(t *testing.T) {
		port := freePort(t)
		base := container(t, "forwarder-rt-opensearch", "opensearchproject/opensearch:2.19.0", port, 9200, "discovery.type=single-node", "DISABLE_SECURITY_PLUGIN=true", "OPENSEARCH_JAVA_OPTS=-Xms512m -Xmx512m")
		testElastic(t, base, "")
	})
	run("elasticsearch", func(t *testing.T) {
		port := freePort(t)
		base := container(t, "forwarder-rt-elastic", "docker.elastic.co/elasticsearch/elasticsearch:8.17.0", port, 9200, "discovery.type=single-node", "xpack.security.enabled=false", "ES_JAVA_OPTS=-Xms512m -Xmx512m")
		testElastic(t, base, "")
	})
	run("victoriametrics", testVictoriaMetrics)
}

func metricSample(ts time.Time) *record.Record {
	r := record.Metric(ts, "cpu", "load", 0.75, map[string]string{"period": "1", "host": "lab"})
	r.Unit = ""
	return r
}

func testElastic(t *testing.T, base, auth string) {
	waitHTTP(t, base+"/_cluster/health", func(r *http.Response) bool { return r.StatusCode == 200 })

	cfg := config.DefaultTarget("elastic")
	cfg.ID, cfg.Name = "rt", "rt-es"
	cfg.Elastic.URL = base
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	tgt, err := target.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ts := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	// Test first: the template, one document
	if msg, err := tgt.Test(ctx, target.Sample()); err != nil || !strings.Contains(msg, "1 document indexed") || !strings.Contains(msg, "index template installed") {
		t.Fatalf("test: %q %v", msg, err)
	}
	// a batch with a number, a bool and a metric: the mapping takes them all
	payloads, errs := tgt.Encode([]*record.Record{sample(ts), boolSample(ts.Add(time.Second)), metricSample(ts)})
	if len(errs) > 0 || len(payloads) != 1 || payloads[0].Records != 3 {
		t.Fatalf("encode: %v %d", errs, len(payloads))
	}
	if err := tgt.Send(ctx, payloads[0]); err != nil {
		t.Fatal(err)
	}
	// a text value into the same index: value is a keyword, so it is taken too
	textRec := sample(ts.Add(2 * time.Second))
	textRec.Key = "ACTUAL_TEMPERATURE_STATUS"
	textRec.SetValue(json.RawMessage(`"NORMAL"`), nil)
	textRec.Type = "STRING"
	payloads, _ = tgt.Encode([]*record.Record{textRec})
	if err := tgt.Send(ctx, payloads[0]); err != nil {
		t.Fatalf("text value: %v", err)
	}

	get := func(path string) (int, []byte) {
		req, _ := http.NewRequestWithContext(ctx, "GET", base+path, nil)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, b
	}
	// the template is there with the mapping
	code, b := get("/_index_template/lite-addon-forwarder-rt")
	if code != 200 || !strings.Contains(string(b), `"value_num":{"type":"double"}`) {
		t.Fatalf("template: %d %s", code, b)
	}
	// refresh, then read back by query
	req, _ := http.NewRequestWithContext(ctx, "POST", base+"/homematic-*/_refresh", nil)
	if res, err := http.DefaultClient.Do(req); err != nil || res.StatusCode != 200 {
		t.Fatalf("refresh: %v", err)
	}
	q := url.Values{"q": {"key:ACTUAL_TEMPERATURE AND device_name:\"Heizung Bad\""}}
	code, b = get("/homematic-event-2026.10.30/_search?" + q.Encode())
	var res struct {
		Hits struct {
			Hits []struct {
				Source map[string]any `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(b, &res); err != nil || code != 200 || len(res.Hits.Hits) != 1 {
		t.Fatalf("search: %d %s %v", code, b, err)
	}
	doc := res.Hits.Hits[0].Source
	t.Logf("%s answered %v", base, doc)
	if doc["value"] != 21.4 || doc["value_num"] != 21.4 || doc["@timestamp"] != "2026-10-30T12:00:00Z" || fmt.Sprint(doc["rooms"]) != "[Bad]" || doc["unit"] != "°C" {
		t.Errorf("doc: %v", doc)
	}
	// the mapping made value a keyword, value_num a double, ts a date
	code, b = get("/homematic-event-2026.10.30/_mapping")
	if code != 200 || !strings.Contains(string(b), `"value":{"ignore_above":1024,"type":"keyword"}`) || !strings.Contains(string(b), `"value_num":{"type":"double"}`) || !strings.Contains(string(b), `"ts":{"type":"date"}`) {
		t.Errorf("mapping: %s", b)
	}
	// the bool and the text are there, the metric in its own index
	code, b = get("/homematic-event-2026.10.30/_search?q=key:STATE")
	if code != 200 || !strings.Contains(string(b), `"value":true`) || !strings.Contains(string(b), `"value_num":1`) {
		t.Errorf("bool: %s", b)
	}
	code, b = get("/homematic-event-2026.10.30/_search?q=key:ACTUAL_TEMPERATURE_STATUS")
	if code != 200 || !strings.Contains(string(b), `"value":"NORMAL"`) {
		t.Errorf("text: %s", b)
	}
	code, b = get("/homematic-metric-2026.10.30/_search?q=group:cpu")
	if code != 200 || !strings.Contains(string(b), `"name":"load"`) || !strings.Contains(string(b), `"period":"1"`) {
		t.Errorf("metric: %s", b)
	}
	// an index name the store refuses is permanent; a closed port is retried
	cfg.Elastic.Index = "-bad"
	bad, _ := target.New(cfg)
	if _, errs := bad.Encode([]*record.Record{sample(ts)}); len(errs) != 1 {
		t.Errorf("bad index encoded: %v", errs)
	}
	cfg.Elastic.Index = config.ElasticPresets["record"].Index
	cfg.Elastic.URL = "http://127.0.0.1:9"
	down, _ := target.New(cfg)
	if err := down.Send(ctx, payloads[0]); err == nil || target.IsPermanent(err) {
		t.Errorf("down: %v", err)
	}
}

func testVictoriaMetrics(t *testing.T) {
	port := freePort(t)
	base := container(t, "forwarder-rt-vm", "victoriametrics/victoria-metrics:v1.115.0", port, 8428)
	waitHTTP(t, base+"/health", func(r *http.Response) bool { return r.StatusCode == 200 })

	cfg := config.DefaultTarget("prometheus")
	cfg.Name = "rt-vm"
	cfg.Prometheus.Mode = "remote_write"
	cfg.Prometheus.URL = base + "/api/v1/write"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	tgt, err := target.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ts := time.Now().Add(-time.Minute).Truncate(time.Second)
	later := sample(ts.Add(30 * time.Second))
	later.SetValue(json.RawMessage(`21.9`), nil)
	payloads, errs := tgt.Encode([]*record.Record{sample(ts), later, boolSample(ts), metricSample(ts)})
	if len(errs) > 0 || len(payloads) != 1 || payloads[0].Records != 4 {
		t.Fatalf("encode: %v %d", errs, len(payloads))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := tgt.Send(ctx, payloads[0]); err != nil {
		t.Fatal(err)
	}
	if msg, err := tgt.Test(ctx, target.Sample()); err != nil || !strings.HasPrefix(msg, "HTTP 204") {
		t.Fatalf("test: %q %v", msg, err)
	}
	// VictoriaMetrics flushes to its storage within a second or so; export reads it raw
	export := func(match string) string {
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			q := url.Values{"match[]": {match}, "start": {fmt.Sprint(ts.Add(-time.Hour).Unix())}, "end": {fmt.Sprint(time.Now().Add(time.Hour).Unix())}}
			res, err := http.Get(base + "/api/v1/export?" + q.Encode())
			if err == nil {
				b, _ := io.ReadAll(res.Body)
				res.Body.Close()
				if strings.TrimSpace(string(b)) != "" {
					return string(b)
				}
			}
			time.Sleep(time.Second)
		}
		return ""
	}
	out := export(`homematic_actual_temperature{device_name="Heizung Bad"}`)
	t.Logf("victoriametrics answered: %s", strings.TrimSpace(out))
	if !strings.Contains(out, `"room":"Bad"`) || !strings.Contains(out, `"values":[21.4,21.9]`) || !strings.Contains(out, fmt.Sprintf(`"timestamps":[%d000,%d000]`, ts.Unix(), ts.Unix()+30)) {
		t.Errorf("series: %s", out)
	}
	if out := export(`homematic_state`); !strings.Contains(out, `"values":[1]`) {
		t.Errorf("bool series: %s", out)
	}
	if out := export(`cpu_load{period="1"}`); !strings.Contains(out, `"values":[0.75]`) || !strings.Contains(out, `"host":"lab"`) {
		t.Errorf("metric series: %s", out)
	}
	// a closed port is retried
	cfg.Prometheus.URL = "http://127.0.0.1:9/api/v1/write"
	down, _ := target.New(cfg)
	if err := down.Send(ctx, payloads[0]); err == nil || target.IsPermanent(err) {
		t.Errorf("down: %v", err)
	}
}
