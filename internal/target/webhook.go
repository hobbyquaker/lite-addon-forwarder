package target

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/hobbyquaker/lite-addon-forwarder/internal/config"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/record"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/tmpl"
)

// Webhook posts batches to an HTTP endpoint: JSON (an array of the rendered bodies) or NDJSON
// (one body per line). The URL is a template too; a batch is grouped by the rendered URL.
type Webhook struct {
	cfg    config.Target
	client *http.Client
	url    *tmpl.Template
	body   *tmpl.Template
}

// NewWebhook builds a webhook target.
func NewWebhook(t config.Target) (*Webhook, error) {
	if t.Webhook == nil {
		return nil, errors.New("webhook settings missing")
	}
	client, err := Client(t)
	if err != nil {
		return nil, err
	}
	w := &Webhook{cfg: t, client: client}
	if w.url, err = tmpl.Compile(strings.TrimSpace(t.Webhook.URL)); err != nil {
		return nil, fmt.Errorf("URL: %w", err)
	}
	if w.body, err = tmpl.Compile(t.Webhook.Body); err != nil {
		return nil, fmt.Errorf("body: %w", err)
	}
	return w, nil
}

func (w *Webhook) contentType() string {
	if w.cfg.Webhook.ContentType != "" {
		return w.cfg.Webhook.ContentType
	}
	if w.cfg.Webhook.Format == "ndjson" {
		return "application/x-ndjson"
	}
	return "application/json"
}

// render answers a record's URL and body.
func (w *Webhook) render(r *record.Record) (string, string, error) {
	ctx := r.Context()
	u, err := w.url.Render(ctx)
	if err != nil {
		return "", "", fmt.Errorf("URL: %w", err)
	}
	if pu, err := url.Parse(u); err != nil || (pu.Scheme != "http" && pu.Scheme != "https") || pu.Host == "" {
		return "", "", fmt.Errorf("the URL rendered %q, not an http(s) URL", u)
	}
	body, err := w.body.Render(ctx)
	if err != nil {
		return "", "", fmt.Errorf("body: %w", err)
	}
	if body == "" {
		return "", "", errors.New("the body rendered empty")
	}
	if w.cfg.Webhook.Format == "json" && !json.Valid([]byte(body)) {
		return "", "", fmt.Errorf("the body is not JSON: %s", Line(body))
	}
	return u, body, nil
}

// Encode implements Target.
func (w *Webhook) Encode(batch []*record.Record) ([]Payload, []string) {
	var payloads []Payload
	var errs []string
	byURL := map[string]*strings.Builder{}
	counts := map[string]int{}
	var order []string
	for _, r := range batch {
		u, body, err := w.render(r)
		if err != nil {
			errs = append(errs, r.Summary()+": "+err.Error())
			continue
		}
		b := byURL[u]
		if b == nil {
			b = &strings.Builder{}
			byURL[u] = b
			order = append(order, u)
		}
		if w.cfg.Webhook.Format == "ndjson" {
			b.WriteString(strings.ReplaceAll(body, "\n", " "))
			b.WriteByte('\n')
		} else {
			if counts[u] > 0 {
				b.WriteByte(',')
			}
			b.WriteString(body)
		}
		counts[u]++
		if b.Len() >= w.cfg.Batch.MaxBytes {
			payloads = append(payloads, w.payload(u, b.String(), counts[u]))
			delete(byURL, u)
			delete(counts, u)
			order = remove(order, u)
		}
	}
	for _, u := range order {
		payloads = append(payloads, w.payload(u, byURL[u].String(), counts[u]))
	}
	return payloads, errs
}

func remove(l []string, s string) []string {
	out := l[:0]
	for _, v := range l {
		if v != s {
			out = append(out, v)
		}
	}
	return out
}

func (w *Webhook) payload(u, body string, n int) Payload {
	if w.cfg.Webhook.Format == "json" {
		body = "[" + body + "]"
	}
	return Payload{Body: []byte(body), Records: n, URL: u, ContentType: w.contentType()}
}

// Send implements Target.
func (w *Webhook) Send(ctx context.Context, p Payload) error {
	req, err := http.NewRequestWithContext(ctx, w.cfg.Webhook.Method, p.URL, nil)
	if err != nil {
		return Permanent("%v", err)
	}
	req.Header.Set("Content-Type", p.ContentType)
	req.Header.Set("User-Agent", "lite-addon-forwarder")
	for k, v := range w.cfg.Webhook.Headers {
		req.Header.Set(k, v)
	}
	switch w.cfg.Webhook.Auth {
	case "basic":
		req.SetBasicAuth(w.cfg.Webhook.User, w.cfg.Secrets["password"])
	case "bearer":
		req.Header.Set("Authorization", "Bearer "+w.cfg.Secrets["token"])
	}
	_, _, err = Do(w.client, req, w.cfg.Webhook.Gzip, p.Body)
	return err
}

// Test implements Target: it posts the sample.
func (w *Webhook) Test(ctx context.Context, sample *record.Record) (string, error) {
	payloads, errs := w.Encode([]*record.Record{sample})
	if len(payloads) == 0 {
		return "", errors.New(strings.Join(errs, "; "))
	}
	p := payloads[0]
	req, err := http.NewRequestWithContext(ctx, w.cfg.Webhook.Method, p.URL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", p.ContentType)
	for k, v := range w.cfg.Webhook.Headers {
		req.Header.Set(k, v)
	}
	switch w.cfg.Webhook.Auth {
	case "basic":
		req.SetBasicAuth(w.cfg.Webhook.User, w.cfg.Secrets["password"])
	case "bearer":
		req.Header.Set("Authorization", "Bearer "+w.cfg.Secrets["token"])
	}
	status, answer, err := Do(w.client, req, w.cfg.Webhook.Gzip, p.Body)
	if err != nil {
		return "", err
	}
	msg := fmt.Sprintf("HTTP %d", status)
	if answer != "" {
		msg += ": " + answer
	}
	return msg + "\n" + w.cfg.Webhook.Method + " " + p.URL + "\n" + string(p.Body), nil
}

// Preview implements Target.
func (w *Webhook) Preview(batch []*record.Record) (string, []string) {
	payloads, errs := w.Encode(batch)
	var b strings.Builder
	for _, p := range payloads {
		fmt.Fprintf(&b, "%s %s (%d)\n%s\n", w.cfg.Webhook.Method, p.URL, p.Records, p.Body)
	}
	return b.String(), errs
}
