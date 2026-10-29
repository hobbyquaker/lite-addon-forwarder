package target

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/hobbyquaker/lite-addon-forwarder/internal/config"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/record"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/tmpl"
)

// Elastic writes to Elasticsearch or OpenSearch through the _bulk API: one action line and
// one document per record, the index name and the document rendered from templates. On the
// first connection it can put an index template that maps the record's fields, so a value that
// is a number on one datapoint and a text on the next cannot break the mapping.
type Elastic struct {
	cfg     config.Target
	client  *http.Client
	base    string
	bulkURL string
	index   *tmpl.Template
	doc     *tmpl.Template

	mu           sync.Mutex
	templateDone bool
}

// NewElastic builds an Elastic target.
func NewElastic(t config.Target) (*Elastic, error) {
	if t.Elastic == nil {
		return nil, errors.New("elastic settings missing")
	}
	client, err := Client(t)
	if err != nil {
		return nil, err
	}
	e := &Elastic{cfg: t, client: client}
	if e.index, err = tmpl.Compile(strings.TrimSpace(t.Elastic.Index)); err != nil {
		return nil, fmt.Errorf("index: %w", err)
	}
	if e.doc, err = tmpl.Compile(t.Elastic.Document); err != nil {
		return nil, fmt.Errorf("document: %w", err)
	}
	e.base = strings.TrimRight(strings.TrimSpace(t.Elastic.URL), "/")
	q := url.Values{}
	if t.Elastic.Pipeline != "" {
		q.Set("pipeline", t.Elastic.Pipeline)
	}
	e.bulkURL = e.base + "/_bulk"
	if len(q) > 0 {
		e.bulkURL += "?" + q.Encode()
	}
	return e, nil
}

// Lines renders one record as its action line and its document.
func (e *Elastic) Lines(r *record.Record) (action, doc string, err error) {
	ctx := r.Context()
	idx, err := e.index.Render(ctx)
	if err != nil {
		return "", "", fmt.Errorf("index: %w", err)
	}
	idx = strings.ToLower(strings.TrimSpace(idx))
	if idx == "" {
		return "", "", errors.New("the index rendered empty")
	}
	if strings.ContainsAny(idx, " \"*\\<|,>/?#:") || strings.HasPrefix(idx, "-") || strings.HasPrefix(idx, "_") || strings.HasPrefix(idx, "+") || idx == "." || idx == ".." {
		return "", "", fmt.Errorf("the index rendered %q, not a valid index name", idx)
	}
	body, err := e.doc.Render(ctx)
	if err != nil {
		return "", "", fmt.Errorf("document: %w", err)
	}
	body = strings.TrimSpace(body)
	if !strings.HasPrefix(body, "{") || !json.Valid([]byte(body)) {
		return "", "", fmt.Errorf("the document is not a JSON object: %s", Line(body))
	}
	if e.cfg.Elastic.Timestamp {
		ts, _ := json.Marshal(r.TS.UTC().Format(time.RFC3339Nano))
		inner := strings.TrimSpace(body[1:])
		if inner == "}" {
			body = `{"@timestamp":` + string(ts) + "}"
		} else {
			body = `{"@timestamp":` + string(ts) + "," + body[1:]
		}
	}
	a, _ := json.Marshal(map[string]map[string]string{e.cfg.Elastic.Action: {"_index": idx}})
	return string(a), strings.ReplaceAll(body, "\n", " "), nil
}

// Encode implements Target.
func (e *Elastic) Encode(batch []*record.Record) ([]Payload, []string) {
	var payloads []Payload
	var errs []string
	var b bytes.Buffer
	n := 0
	flush := func() {
		if n > 0 {
			payloads = append(payloads, Payload{Body: append([]byte(nil), b.Bytes()...), Records: n, ContentType: "application/x-ndjson"})
		}
		b.Reset()
		n = 0
	}
	for _, r := range batch {
		action, doc, err := e.Lines(r)
		if err != nil {
			errs = append(errs, r.Summary()+": "+err.Error())
			continue
		}
		if b.Len() > 0 && b.Len()+len(action)+len(doc)+2 > e.cfg.Batch.MaxBytes {
			flush()
		}
		b.WriteString(action)
		b.WriteByte('\n')
		b.WriteString(doc)
		b.WriteByte('\n')
		n++
	}
	flush()
	return payloads, errs
}

func (e *Elastic) auth(req *http.Request) {
	switch e.cfg.Elastic.Auth {
	case "basic":
		req.SetBasicAuth(e.cfg.Elastic.User, e.cfg.Secrets["password"])
	case "apikey":
		req.Header.Set("Authorization", "ApiKey "+e.cfg.Secrets["api_key"])
	case "bearer":
		req.Header.Set("Authorization", "Bearer "+e.cfg.Secrets["token"])
	}
	req.Header.Set("User-Agent", "lite-addon-forwarder")
}

// bulkResponse is what _bulk answers.
type bulkResponse struct {
	Errors bool `json:"errors"`
	Items  []map[string]struct {
		Status int `json:"status"`
		Error  *struct {
			Type   string `json:"type"`
			Reason string `json:"reason"`
		} `json:"error"`
	} `json:"items"`
}

// Send implements Target: the template first (once), then the bulk. An item the store refused
// for its content (a 4xx) is dropped and counted (a PartialError); an item it could not take for
// the moment (429, 5xx) makes the whole payload retry.
func (e *Elastic) Send(ctx context.Context, p Payload) error {
	if err := e.ensureTemplate(ctx); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.bulkURL, nil)
	if err != nil {
		return Permanent("%v", err)
	}
	req.Header.Set("Content-Type", "application/x-ndjson")
	e.auth(req)
	_, answer, err := doRaw(e.client, req, e.cfg.Elastic.Gzip, p.Body)
	if err != nil {
		return err
	}
	return e.classify(answer, p.Records)
}

// classify reads a bulk answer.
func (e *Elastic) classify(answer []byte, records int) error {
	var res bulkResponse
	if err := json.Unmarshal(answer, &res); err != nil {
		return fmt.Errorf("the bulk answer is not JSON: %s", Line(string(answer)))
	}
	if !res.Errors {
		return nil
	}
	dropped, retry := 0, 0
	var reason string
	for _, item := range res.Items {
		for _, v := range item {
			if v.Status < 400 {
				continue
			}
			msg := fmt.Sprintf("HTTP %d", v.Status)
			if v.Error != nil {
				msg += ": " + v.Error.Type + ": " + v.Error.Reason
			}
			if v.Status == 429 || v.Status >= 500 {
				retry++
			} else {
				dropped++
			}
			if reason == "" {
				reason = Line(msg)
			}
		}
	}
	if retry > 0 {
		return fmt.Errorf("%d of %d items not taken: %s", retry+dropped, records, reason)
	}
	if dropped == 0 {
		return nil
	}
	if dropped >= records {
		return Permanent("%d items refused: %s", dropped, reason)
	}
	return &PartialError{Dropped: dropped, Err: fmt.Errorf("%d of %d items refused: %s", dropped, records, reason)}
}

// ensureTemplate puts the index template once per connection (and again after a failure).
func (e *Elastic) ensureTemplate(ctx context.Context) error {
	if !e.cfg.Elastic.InstallTemplate {
		return nil
	}
	e.mu.Lock()
	done := e.templateDone
	e.mu.Unlock()
	if done {
		return nil
	}
	name := "lite-addon-forwarder-" + strings.ToLower(e.cfg.ID)
	if e.cfg.ID == "" {
		name = "lite-addon-forwarder"
	}
	body, _ := json.Marshal(IndexTemplate(e.cfg.Elastic.TemplatePattern))
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, e.base+"/_index_template/"+name, nil)
	if err != nil {
		return Permanent("%v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	e.auth(req)
	if _, _, err := Do(e.client, req, false, body); err != nil {
		return fmt.Errorf("index template: %w", err)
	}
	e.mu.Lock()
	e.templateDone = true
	e.mu.Unlock()
	return nil
}

// IndexTemplate is the index template the target installs: the record's fields with fixed
// types, every other string a keyword.
func IndexTemplate(pattern string) map[string]any {
	kw := map[string]any{"type": "keyword", "ignore_above": 1024}
	return map[string]any{
		"index_patterns": []string{pattern},
		"priority":       50,
		"_meta":          map[string]any{"description": "lite-addon-forwarder: the record's fields"},
		"template": map[string]any{
			"mappings": map[string]any{
				"dynamic_templates": []map[string]any{{"strings_as_keywords": map[string]any{"match_mapping_type": "string", "mapping": kw}}},
				"properties": map[string]any{
					"@timestamp":   map[string]any{"type": "date"},
					"ts":           map[string]any{"type": "date"},
					"kind":         kw,
					"interface":    kw,
					"device":       kw,
					"channel":      map[string]any{"type": "integer"},
					"address":      kw,
					"key":          kw,
					"value":        kw,
					"value_num":    map[string]any{"type": "double"},
					"value_text":   kw,
					"value_bool":   map[string]any{"type": "boolean"},
					"type":         kw,
					"unit":         kw,
					"device_type":  kw,
					"channel_type": kw,
					"device_name":  kw,
					"channel_name": kw,
					"rooms":        kw,
					"functions":    kw,
					"source":       kw,
					"group":        kw,
					"name":         kw,
					"metric":       kw,
					"labels":       map[string]any{"type": "object", "dynamic": true},
					"stream":       kw,
					"log_unit":     kw,
					"priority":     kw,
					"message":      map[string]any{"type": "text"},
					"fields":       map[string]any{"type": "object", "dynamic": true},
				},
			},
		},
	}
}

// Test implements Target: the template, then the sample through _bulk.
func (e *Elastic) Test(ctx context.Context, sample *record.Record) (string, error) {
	payloads, errs := e.Encode([]*record.Record{sample})
	if len(payloads) == 0 {
		return "", errors.New(strings.Join(errs, "; "))
	}
	e.mu.Lock()
	e.templateDone = false
	e.mu.Unlock()
	if err := e.ensureTemplate(ctx); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.bulkURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-ndjson")
	e.auth(req)
	status, answer, err := doRaw(e.client, req, e.cfg.Elastic.Gzip, payloads[0].Body)
	if err != nil {
		return "", err
	}
	if err := e.classify(answer, 1); err != nil {
		return "", err
	}
	msg := fmt.Sprintf("HTTP %d: 1 document indexed", status)
	if e.cfg.Elastic.InstallTemplate {
		msg += "; index template installed"
	}
	return msg + "\n" + string(payloads[0].Body), nil
}

// Preview implements Target.
func (e *Elastic) Preview(batch []*record.Record) (string, []string) {
	payloads, errs := e.Encode(batch)
	var b strings.Builder
	for _, p := range payloads {
		b.Write(p.Body)
	}
	return b.String(), errs
}
