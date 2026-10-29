package target

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang/snappy"

	"github.com/hobbyquaker/lite-addon-forwarder/internal/config"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/record"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/tmpl"
)

// Prometheus turns records into samples of a time series - the metric name and the labels from
// templates, the value the record's number - and either keeps the newest promSample per series for
// a scrape endpoint the daemon serves (mode scrape; the system's web server proxies it, a token
// protects it) or pushes them with remote_write (mode remote_write: protobuf in snappy, what
// Prometheus, VictoriaMetrics, Mimir, Thanos and Grafana Cloud take).
type Prometheus struct {
	cfg    config.Target
	client *http.Client
	name   *tmpl.Template
	labels []namedTemplate

	mu     sync.Mutex
	series map[string]*promSample // scrape mode: the newest promSample per series
}

// promSample is one value of one series.
type promSample struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels"`
	Value  float64           `json:"value"`
	TS     int64             `json:"ts"` // ms
}

// NewPrometheus builds a Prometheus target.
func NewPrometheus(t config.Target) (*Prometheus, error) {
	if t.Prometheus == nil {
		return nil, errors.New("prometheus settings missing")
	}
	p := &Prometheus{cfg: t, series: map[string]*promSample{}}
	var err error
	if p.name, err = tmpl.Compile(t.Prometheus.Name); err != nil {
		return nil, fmt.Errorf("name: %w", err)
	}
	for _, l := range t.Prometheus.Labels {
		n, err := tmpl.Compile(l.Name)
		if err != nil {
			return nil, fmt.Errorf("label %q: %w", l.Name, err)
		}
		v, err := tmpl.Compile(l.Value)
		if err != nil {
			return nil, fmt.Errorf("label %q: %w", l.Name, err)
		}
		p.labels = append(p.labels, namedTemplate{n, v})
	}
	if t.Prometheus.Mode == "remote_write" {
		if p.client, err = Client(t); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// Sample renders one record as a promSample.
func (p *Prometheus) Sample(r *record.Record) (*promSample, error) {
	if r.ValueNum == nil {
		return nil, errors.New("no numeric value")
	}
	if math.IsNaN(*r.ValueNum) || math.IsInf(*r.ValueNum, 0) {
		return nil, errors.New("the value is not a finite number")
	}
	ctx := r.Context()
	name, err := p.name.Render(ctx)
	if err != nil {
		return nil, fmt.Errorf("name: %w", err)
	}
	name = MetricName(name)
	if name == "" {
		return nil, errors.New("the metric name rendered empty")
	}
	s := &promSample{Name: name, Labels: map[string]string{}, Value: *r.ValueNum, TS: r.TS.UnixMilli()}
	for _, l := range p.labels {
		ln, err := l.name.Render(ctx)
		if err != nil {
			return nil, fmt.Errorf("label: %w", err)
		}
		if ln == "*" {
			for k, v := range r.Labels {
				if k = LabelName(k); k != "" && v != "" {
					s.Labels[k] = v
				}
			}
			continue
		}
		ln = LabelName(ln)
		if ln == "" {
			continue
		}
		v, err := l.value.Render(ctx)
		if err != nil {
			return nil, fmt.Errorf("label %s: %w", ln, err)
		}
		if v == "" {
			continue
		}
		s.Labels[ln] = v
	}
	return s, nil
}

// MetricName makes a valid metric name of a text: every other character becomes "_", a
// leading digit gets one in front.
func MetricName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var b strings.Builder
	for i, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_', c == ':':
			b.WriteRune(c)
		case c >= '0' && c <= '9':
			if i == 0 {
				b.WriteByte('_')
			}
			b.WriteRune(c)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// LabelName makes a valid label name; a reserved "__" name is refused ("").
func LabelName(s string) string {
	s = strings.ReplaceAll(MetricName(s), ":", "_")
	if strings.HasPrefix(s, "__") {
		return ""
	}
	return s
}

// key is the series identity: the name and the sorted labels.
func (s *promSample) key() string {
	keys := make([]string, 0, len(s.Labels))
	for k := range s.Labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(s.Name)
	for _, k := range keys {
		b.WriteByte(0)
		b.WriteString(k)
		b.WriteByte(1)
		b.WriteString(s.Labels[k])
	}
	return b.String()
}

// Encode implements Target: scrape mode carries the samples as JSON (Send stores them),
// remote_write mode a snappy-compressed WriteRequest.
func (p *Prometheus) Encode(batch []*record.Record) ([]Payload, []string) {
	var errs []string
	samples := make([]*promSample, 0, len(batch))
	for _, r := range batch {
		s, err := p.Sample(r)
		if err != nil {
			errs = append(errs, r.Summary()+": "+err.Error())
			continue
		}
		samples = append(samples, s)
	}
	if len(samples) == 0 {
		return nil, errs
	}
	if p.cfg.Prometheus.Mode != "remote_write" {
		b, _ := json.Marshal(samples)
		return []Payload{{Body: b, Records: len(samples), ContentType: "application/json"}}, errs
	}
	return []Payload{{Body: snappy.Encode(nil, WriteRequest(samples)), Records: len(samples), ContentType: "application/x-protobuf"}}, errs
}

// WriteRequest encodes samples as a prometheus.WriteRequest (protobuf): the series grouped by
// identity, each series' samples in time order, the labels sorted by name as the protocol wants.
func WriteRequest(samples []*promSample) []byte {
	groups := map[string][]*promSample{}
	var order []string
	for _, s := range samples {
		k := s.key()
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], s)
	}
	var out []byte
	for _, k := range order {
		ss := groups[k]
		sort.SliceStable(ss, func(i, j int) bool { return ss[i].TS < ss[j].TS })
		var ts []byte
		ts = appendLabel(ts, "__name__", ss[0].Name)
		names := make([]string, 0, len(ss[0].Labels))
		for n := range ss[0].Labels {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			ts = appendLabel(ts, n, ss[0].Labels[n])
		}
		for _, s := range ss {
			var sm []byte
			sm = append(sm, 0x09) // field 1, 64-bit
			sm = binary.LittleEndian.AppendUint64(sm, math.Float64bits(s.Value))
			sm = append(sm, 0x10) // field 2, varint (an int64 is a plain varint, not zigzag)
			sm = binary.AppendUvarint(sm, uint64(s.TS))
			ts = appendBytes(ts, 2, sm)
		}
		out = appendBytes(out, 1, ts)
	}
	return out
}

func appendLabel(b []byte, name, value string) []byte {
	var l []byte
	l = appendBytes(l, 1, []byte(name))
	l = appendBytes(l, 2, []byte(value))
	return appendBytes(b, 1, l)
}

// appendBytes appends a length-delimited field.
func appendBytes(b []byte, field int, v []byte) []byte {
	b = binary.AppendUvarint(b, uint64(field)<<3|2)
	b = binary.AppendUvarint(b, uint64(len(v)))
	return append(b, v...)
}

// Send implements Target.
func (p *Prometheus) Send(ctx context.Context, pl Payload) error {
	if p.cfg.Prometheus.Mode != "remote_write" {
		var samples []*promSample
		if err := json.Unmarshal(pl.Body, &samples); err != nil {
			return Permanent("%v", err)
		}
		p.mu.Lock()
		for _, s := range samples {
			k := s.key()
			if old := p.series[k]; old == nil || old.TS <= s.TS {
				p.series[k] = s
			}
		}
		p.mu.Unlock()
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSpace(p.cfg.Prometheus.URL), nil)
	if err != nil {
		return Permanent("%v", err)
	}
	p.headers(req)
	_, _, err = Do(p.client, req, false, pl.Body)
	return err
}

func (p *Prometheus) headers(req *http.Request) {
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("Content-Encoding", "snappy")
	req.Header.Set("X-Prometheus-Remote-Write-Version", "0.1.0")
	req.Header.Set("User-Agent", "lite-addon-forwarder")
	switch p.cfg.Prometheus.Auth {
	case "basic":
		req.SetBasicAuth(p.cfg.Prometheus.User, p.cfg.Secrets["password"])
	case "bearer":
		req.Header.Set("Authorization", "Bearer "+p.cfg.Secrets["token"])
	}
}

// Test implements Target: scrape mode renders the promSample and says where the endpoint is;
// remote_write pushes it.
func (p *Prometheus) Test(ctx context.Context, sample *record.Record) (string, error) {
	s, err := p.Sample(sample)
	if err != nil {
		return "", err
	}
	line := exposeLine(s)
	if p.cfg.Prometheus.Mode != "remote_write" {
		if p.cfg.Secrets["scrape_token"] == "" {
			return "", errors.New("the scrape token is not set")
		}
		return "the endpoint answers the newest value of every series to a scrape with the token\n" + line, nil
	}
	payloads, _ := p.Encode([]*record.Record{sample})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSpace(p.cfg.Prometheus.URL), nil)
	if err != nil {
		return "", err
	}
	p.headers(req)
	status, answer, err := Do(p.client, req, false, payloads[0].Body)
	if err != nil {
		return "", err
	}
	msg := fmt.Sprintf("HTTP %d", status)
	if answer != "" {
		msg += ": " + answer
	}
	return msg + "\n" + line, nil
}

// Preview implements Target: the samples in the exposition format.
func (p *Prometheus) Preview(batch []*record.Record) (string, []string) {
	var b strings.Builder
	var errs []string
	for _, r := range batch {
		s, err := p.Sample(r)
		if err != nil {
			errs = append(errs, r.Summary()+": "+err.Error())
			continue
		}
		b.WriteString(exposeLine(s))
		b.WriteByte('\n')
	}
	return b.String(), errs
}

// Authorize implements Exposer.
func (p *Prometheus) Authorize(token string) bool {
	want := p.cfg.Secrets["scrape_token"]
	return want != "" && token == want
}

// Expose implements Exposer: every series' newest promSample, grouped by name with a TYPE line
// (counter for a name ending in _total, gauge otherwise), stale ones left out.
func (p *Prometheus) Expose(w io.Writer, now time.Time) {
	p.mu.Lock()
	all := make([]*promSample, 0, len(p.series))
	stale := time.Duration(p.cfg.Prometheus.StaleS) * time.Second
	for k, s := range p.series {
		if stale > 0 && now.Sub(time.UnixMilli(s.TS)) > stale {
			delete(p.series, k)
			continue
		}
		all = append(all, s)
	}
	p.mu.Unlock()
	sort.Slice(all, func(i, j int) bool {
		if all[i].Name != all[j].Name {
			return all[i].Name < all[j].Name
		}
		return all[i].key() < all[j].key()
	})
	last := ""
	for _, s := range all {
		if s.Name != last {
			typ := "gauge"
			if strings.HasSuffix(s.Name, "_total") {
				typ = "counter"
			}
			fmt.Fprintf(w, "# TYPE %s %s\n", s.Name, typ)
			last = s.Name
		}
		fmt.Fprintf(w, "%s %d\n", exposeLine(s), s.TS)
	}
}

// exposeLine is one promSample in the exposition format, without the timestamp.
func exposeLine(s *promSample) string {
	var b strings.Builder
	b.WriteString(s.Name)
	if len(s.Labels) > 0 {
		keys := make([]string, 0, len(s.Labels))
		for k := range s.Labels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(k)
			b.WriteString(`="`)
			b.WriteString(labelEsc.Replace(s.Labels[k]))
			b.WriteByte('"')
		}
		b.WriteByte('}')
	}
	b.WriteByte(' ')
	b.WriteString(strconv.FormatFloat(s.Value, 'g', -1, 64))
	return b.String()
}

var labelEsc = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
