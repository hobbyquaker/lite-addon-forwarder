package target

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hobbyquaker/lite-addon-forwarder/internal/config"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/record"
)

// fakeES answers _bulk and _index_template like Elasticsearch, with a per-item verdict.
type fakeES struct {
	mu        sync.Mutex
	templates map[string]json.RawMessage
	bulks     []string
	auths     []string
	// itemStatus answers the status of the n-th item of the next bulk (default 201)
	itemStatus map[int]int
	httpStatus int
}

func (f *fakeES) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auths = append(f.auths, r.Header.Get("Authorization"))
	if f.httpStatus != 0 {
		http.Error(w, "down", f.httpStatus)
		return
	}
	switch {
	case strings.HasPrefix(r.URL.Path, "/_index_template/") && r.Method == http.MethodPut:
		var body json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&body)
		if f.templates == nil {
			f.templates = map[string]json.RawMessage{}
		}
		f.templates[strings.TrimPrefix(r.URL.Path, "/_index_template/")] = body
		_, _ = w.Write([]byte(`{"acknowledged":true}`))
	case r.URL.Path == "/_bulk" && r.Method == http.MethodPost:
		b, _ := readAll(r.Body, r.Header.Get("Content-Encoding") == "gzip")
		f.bulks = append(f.bulks, string(b))
		lines := strings.Split(strings.TrimSpace(string(b)), "\n")
		var items []map[string]any
		errs := false
		for i := 0; i+1 < len(lines); i += 2 {
			var action map[string]map[string]string
			_ = json.Unmarshal([]byte(lines[i]), &action)
			status := 201
			if s, ok := f.itemStatus[i/2]; ok {
				status = s
			}
			item := map[string]any{"status": status, "_index": action["index"]["_index"]}
			if status >= 400 {
				errs = true
				item["error"] = map[string]any{"type": "mapper_parsing_exception", "reason": "failed to parse field [value]"}
			}
			for op := range action {
				items = append(items, map[string]any{op: item})
			}
		}
		f.itemStatus = nil
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"took": 3, "errors": errs, "items": items})
	default:
		http.NotFound(w, r)
	}
}

// readAll reads a body, gunzipped when gz.
func readAll(r io.Reader, gz bool) ([]byte, error) {
	if gz {
		zr, err := gzip.NewReader(r)
		if err != nil {
			return nil, err
		}
		r = zr
	}
	return io.ReadAll(r)
}

func TestElasticLines(t *testing.T) {
	cfg := config.DefaultTarget("elastic")
	cfg.Name = "es"
	cfg.Elastic.URL = "http://x:9200"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	e, err := NewElastic(cfg)
	if err != nil {
		t.Fatal(err)
	}
	action, doc, err := e.Lines(sample())
	if err != nil {
		t.Fatal(err)
	}
	if action != `{"index":{"_index":"homematic-event-2026.10.29"}}` {
		t.Errorf("action %s", action)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(doc), &m); err != nil {
		t.Fatalf("doc not JSON: %v: %s", err, doc)
	}
	if m["@timestamp"] != "2026-10-29T14:03:12.481Z" || m["key"] != "ACTUAL_TEMPERATURE" || m["value"] != 21.4 || m["device_name"] != "Heizung Bad" {
		t.Errorf("doc %s", doc)
	}
	// the flat preset, a metric, a data stream (create)
	cfg.Elastic.Document = config.ElasticPresets["flat"].Document
	cfg.Elastic.Index = "metrics-{{ group }}"
	cfg.Elastic.Action = "create"
	e, _ = NewElastic(cfg)
	mr := record.Metric(time.Unix(1700000000, 0), "cpu", "load", 0.5, map[string]string{"period": "1"})
	action, doc, err = e.Lines(mr)
	if err != nil || action != `{"create":{"_index":"metrics-cpu"}}` {
		t.Fatalf("metric: %s %v", action, err)
	}
	_ = json.Unmarshal([]byte(doc), &m)
	if m["metric"] != "load" || m["value_num"] != 0.5 || m["labels"].(map[string]any)["period"] != "1" || m["@timestamp"] == nil {
		t.Errorf("flat doc %s", doc)
	}
	// errors: an upper-case index is lowered, a bad one refused, a non-object document refused
	cfg.Elastic.Index = "{{ device_name }}"
	e, _ = NewElastic(cfg)
	action, _, err = e.Lines(sample())
	if err == nil || !strings.Contains(err.Error(), "not a valid index name") {
		t.Errorf("space in the index: %s %v", action, err)
	}
	cfg.Elastic.Index = "{{ key }}"
	e, _ = NewElastic(cfg)
	action, _, _ = e.Lines(sample())
	if action != `{"create":{"_index":"actual_temperature"}}` {
		t.Errorf("lower: %s", action)
	}
	cfg.Elastic.Document = "{{ value }}"
	e, _ = NewElastic(cfg)
	if _, _, err := e.Lines(sample()); err == nil || !strings.Contains(err.Error(), "not a JSON object") {
		t.Errorf("non-object: %v", err)
	}
	cfg.Elastic.Document = "{}"
	cfg.Elastic.Timestamp = true
	e, _ = NewElastic(cfg)
	if _, doc, _ := e.Lines(sample()); doc != `{"@timestamp":"2026-10-29T14:03:12.481Z"}` {
		t.Errorf("empty object: %s", doc)
	}
	cfg.Elastic.Timestamp = false
	e, _ = NewElastic(cfg)
	if _, doc, _ := e.Lines(sample()); doc != `{}` {
		t.Errorf("no timestamp: %s", doc)
	}
}

func TestElasticSend(t *testing.T) {
	f := &fakeES{}
	srv := httptest.NewServer(f)
	defer srv.Close()
	cfg := config.DefaultTarget("elastic")
	cfg.ID, cfg.Name = "abc", "es"
	cfg.Elastic.URL = srv.URL
	cfg.Elastic.Auth = "apikey"
	cfg.Elastic.Pipeline = "p1"
	cfg.Secrets = map[string]string{"api_key": "k"}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	e, err := NewElastic(cfg)
	if err != nil {
		t.Fatal(err)
	}
	payloads, errs := e.Encode([]*record.Record{sample(), boolRec()})
	if len(payloads) != 1 || payloads[0].Records != 2 || len(errs) != 0 || strings.Count(string(payloads[0].Body), "\n") != 4 {
		t.Fatalf("encode: %+v %v", payloads, errs)
	}
	if err := e.Send(context.Background(), payloads[0]); err != nil {
		t.Fatal(err)
	}
	// the template went first, then the bulk, both with the key
	if tpl, ok := f.templates["lite-addon-forwarder-abc"]; !ok || !strings.Contains(string(tpl), `"homematic-*"`) || !strings.Contains(string(tpl), `"value_num":{"type":"double"}`) {
		t.Errorf("template: %s", tpl)
	}
	if len(f.auths) != 2 || f.auths[0] != "ApiKey k" || f.auths[1] != "ApiKey k" {
		t.Errorf("auth %v", f.auths)
	}
	if len(f.bulks) != 1 || !strings.Contains(f.bulks[0], `"_index":"homematic-event-2026.10.29"`) {
		t.Errorf("bulk %v", f.bulks)
	}
	// the second send does not repeat the template
	if err := e.Send(context.Background(), payloads[0]); err != nil || len(f.auths) != 3 {
		t.Errorf("second send: %v %d", err, len(f.auths))
	}
	// a partial refusal: item 1 refused -> PartialError with 1 dropped
	f.itemStatus = map[int]int{1: 400}
	err = e.Send(context.Background(), payloads[0])
	var pe *PartialError
	if !errors.As(err, &pe) || pe.Dropped != 1 || !strings.Contains(err.Error(), "mapper_parsing_exception") {
		t.Errorf("partial: %v", err)
	}
	// every item refused -> permanent
	f.itemStatus = map[int]int{0: 400, 1: 400}
	if err := e.Send(context.Background(), payloads[0]); !IsPermanent(err) {
		t.Errorf("all refused: %v", err)
	}
	// a 429 item -> retry
	f.itemStatus = map[int]int{0: 429}
	if err := e.Send(context.Background(), payloads[0]); err == nil || IsPermanent(err) || errors.As(err, &pe) {
		t.Errorf("429 item: %v", err)
	}
	// the store down: retry, and the template is tried again next time
	e2, _ := NewElastic(cfg)
	f.httpStatus = 503
	if err := e2.Send(context.Background(), payloads[0]); err == nil || IsPermanent(err) || !strings.Contains(err.Error(), "index template") {
		t.Errorf("down: %v", err)
	}
	f.httpStatus = 0
	// Test: the template again, one document, the answer text
	msg, err := e.Test(context.Background(), Sample())
	if err != nil || !strings.Contains(msg, "1 document indexed") || !strings.Contains(msg, "index template installed") || !strings.Contains(msg, "FORWARDER_TEST") {
		t.Errorf("test: %q %v", msg, err)
	}
	// without the template: a basic auth bulk only
	cfg.Elastic.InstallTemplate = false
	cfg.Elastic.Auth = "basic"
	cfg.Elastic.User = "u"
	cfg.Secrets = map[string]string{"password": "p"}
	e3, _ := NewElastic(cfg)
	before := len(f.auths)
	if err := e3.Send(context.Background(), payloads[0]); err != nil || len(f.auths) != before+1 || !strings.HasPrefix(f.auths[before], "Basic ") {
		t.Errorf("basic: %v %v", err, f.auths)
	}
	// preview
	text, errs := e.Preview([]*record.Record{sample()})
	if len(errs) != 0 || !strings.HasPrefix(text, `{"index":`) {
		t.Errorf("preview %q %v", text, errs)
	}
}
