package target

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/hobbyquaker/lite-addon-forwarder/internal/config"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/record"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/tmpl"
)

// Loki pushes log lines to Grafana Loki (/loki/api/v1/push, JSON): the stream labels, the line
// and the structured metadata are templates; a batch is grouped into streams by its rendered
// label sets, the entries of a stream sorted by time.
type Loki struct {
	cfg    config.Target
	client *http.Client
	url    string
	labels namedTemplates
	line   *tmpl.Template
	meta   namedTemplates
}

// NewLoki builds a Loki target.
func NewLoki(t config.Target) (*Loki, error) {
	if t.Loki == nil {
		return nil, errors.New("loki settings missing")
	}
	client, err := Client(t)
	if err != nil {
		return nil, err
	}
	l := &Loki{cfg: t, client: client, url: strings.TrimRight(strings.TrimSpace(t.Loki.URL), "/") + "/loki/api/v1/push"}
	if l.labels, err = compileNamed(t.Loki.Labels, "label"); err != nil {
		return nil, err
	}
	if l.line, err = tmpl.Compile(t.Loki.Line); err != nil {
		return nil, fmt.Errorf("line: %w", err)
	}
	if l.meta, err = compileNamed(t.Loki.Metadata, "metadata"); err != nil {
		return nil, err
	}
	return l, nil
}

// lokiEntry is one rendered record.
type lokiEntry struct {
	key    string
	labels map[string]string
	ts     int64
	line   string
	meta   map[string]string
}

// Entry renders one record.
func (l *Loki) Entry(r *record.Record) (*lokiEntry, error) {
	ctx := r.Context()
	pairs, err := l.labels.render(r, ctx, false)
	if err != nil {
		return nil, fmt.Errorf("labels: %w", err)
	}
	e := &lokiEntry{labels: map[string]string{}, ts: r.TS.UnixNano()}
	for _, p := range pairs {
		if n := LabelName(p[0]); n != "" {
			e.labels[n] = p[1]
		}
	}
	if len(e.labels) == 0 {
		return nil, errors.New("no label rendered (Loki needs at least one)")
	}
	line, err := l.line.Render(ctx)
	if err != nil {
		return nil, fmt.Errorf("line: %w", err)
	}
	if line = strings.TrimRight(line, "\r\n"); line == "" {
		return nil, errors.New("the line rendered empty")
	}
	e.line = line
	if len(l.meta) > 0 {
		mp, err := l.meta.render(r, ctx, false)
		if err != nil {
			return nil, fmt.Errorf("metadata: %w", err)
		}
		if len(mp) > 0 {
			e.meta = map[string]string{}
			for _, p := range mp {
				if n := LabelName(p[0]); n != "" {
					e.meta[n] = p[1]
				}
			}
		}
	}
	keys := make([]string, 0, len(e.labels))
	for k := range e.labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(strconv.Quote(e.labels[k]))
		b.WriteByte(',')
	}
	e.key = b.String()
	return e, nil
}

// push is the request body.
type push struct {
	Streams []lokiStream `json:"streams"`
}

type lokiStream struct {
	Stream map[string]string `json:"stream"`
	Values [][]any           `json:"values"`
}

// Encode implements Target: the batch grouped into streams.
func (l *Loki) Encode(batch []*record.Record) ([]Payload, []string) {
	var errs []string
	byKey := map[string]*lokiStream{}
	var order []string
	n := 0
	for _, r := range batch {
		e, err := l.Entry(r)
		if err != nil {
			errs = append(errs, r.Summary()+": "+err.Error())
			continue
		}
		s := byKey[e.key]
		if s == nil {
			s = &lokiStream{Stream: e.labels}
			byKey[e.key] = s
			order = append(order, e.key)
		}
		v := []any{strconv.FormatInt(e.ts, 10), e.line}
		if e.meta != nil {
			v = append(v, e.meta)
		}
		s.Values = append(s.Values, v)
		n++
	}
	if n == 0 {
		return nil, errs
	}
	var payloads []Payload
	var cur push
	size, count := 0, 0
	flush := func() {
		if count > 0 {
			b, _ := json.Marshal(cur)
			payloads = append(payloads, Payload{Body: b, Records: count, ContentType: "application/json"})
		}
		cur = push{}
		size, count = 0, 0
	}
	for _, k := range order {
		s := byKey[k]
		sort.SliceStable(s.Values, func(i, j int) bool { return s.Values[i][0].(string) < s.Values[j][0].(string) })
		approx := 0
		for _, v := range s.Values {
			approx += len(v[1].(string)) + 40
		}
		if size > 0 && size+approx > l.cfg.Batch.MaxBytes {
			flush()
		}
		cur.Streams = append(cur.Streams, *s)
		size += approx
		count += len(s.Values)
	}
	flush()
	return payloads, errs
}

func (l *Loki) request(ctx context.Context) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "lite-addon-forwarder")
	if l.cfg.Loki.TenantID != "" {
		req.Header.Set("X-Scope-OrgID", l.cfg.Loki.TenantID)
	}
	switch l.cfg.Loki.Auth {
	case "basic":
		req.SetBasicAuth(l.cfg.Loki.User, l.cfg.Secrets["password"])
	case "bearer":
		req.Header.Set("Authorization", "Bearer "+l.cfg.Secrets["token"])
	}
	return req, nil
}

// Send implements Target: 204 is success, a 400 (a malformed push, an entry too old) is
// permanent, 429 and the server errors retry.
func (l *Loki) Send(ctx context.Context, p Payload) error {
	req, err := l.request(ctx)
	if err != nil {
		return Permanent("%v", err)
	}
	_, _, err = Do(l.client, req, l.cfg.Loki.Gzip, p.Body)
	return err
}

// Test implements Target.
func (l *Loki) Test(ctx context.Context, sample *record.Record) (string, error) {
	payloads, errs := l.Encode([]*record.Record{sample})
	if len(payloads) == 0 {
		return "", errors.New(strings.Join(errs, "; "))
	}
	req, err := l.request(ctx)
	if err != nil {
		return "", err
	}
	status, answer, err := Do(l.client, req, l.cfg.Loki.Gzip, payloads[0].Body)
	if err != nil {
		return "", err
	}
	msg := fmt.Sprintf("HTTP %d", status)
	if answer != "" {
		msg += ": " + answer
	}
	return msg + "\n" + string(payloads[0].Body), nil
}

// Preview implements Target.
func (l *Loki) Preview(batch []*record.Record) (string, []string) {
	payloads, errs := l.Encode(batch)
	var b strings.Builder
	for _, p := range payloads {
		var out bytes.Buffer
		if json.Indent(&out, p.Body, "", "  ") == nil {
			b.Write(out.Bytes())
		} else {
			b.Write(p.Body)
		}
		b.WriteByte('\n')
	}
	return b.String(), errs
}
