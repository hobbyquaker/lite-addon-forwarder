package target

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/hobbyquaker/lite-addon-forwarder/internal/config"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/record"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/tmpl"
)

// Influx writes the line protocol to InfluxDB 1 (/write) or 2 (/api/v2/write; InfluxDB 3,
// VictoriaMetrics, QuestDB and Telegraf's http_listener_v2 speak one of the two).
type Influx struct {
	cfg      config.Target
	client   *http.Client
	writeURL string
	auth     string
	kinds    map[string]*lineTemplates
}

type lineTemplates struct {
	measurement *tmpl.Template
	tags        []namedTemplate
	fields      []fieldTemplate
}

type namedTemplate struct {
	name, value *tmpl.Template
}

type fieldTemplate struct {
	name, value *tmpl.Template
	typ         string
	// native is set for the value template "{{ value }}": the record's own value is written in
	// its own type, without a round trip through text.
	native bool
}

// NewInflux builds an Influx target.
func NewInflux(t config.Target) (*Influx, error) {
	if t.Influx == nil {
		return nil, errors.New("influx settings missing")
	}
	client, err := Client(t)
	if err != nil {
		return nil, err
	}
	in := &Influx{cfg: t, client: client, kinds: map[string]*lineTemplates{}}
	for kind, tpls := range map[string]config.InfluxTemplates{record.KindEvent: t.Influx.Event, record.KindMetric: t.Influx.Metric, record.KindLog: t.Influx.Log} {
		if tpls.Measurement == "" {
			continue
		}
		lt, err := compileLine(tpls)
		if err != nil {
			return nil, fmt.Errorf("%s templates: %w", kind, err)
		}
		in.kinds[kind] = lt
	}
	base := strings.TrimRight(strings.TrimSpace(t.Influx.URL), "/")
	q := url.Values{"precision": {"ns"}}
	switch t.Influx.Version {
	case "1":
		q.Set("db", t.Influx.Database)
		if t.Influx.RetentionPolicy != "" {
			q.Set("rp", t.Influx.RetentionPolicy)
		}
		in.writeURL = base + "/write?" + q.Encode()
	default:
		q.Set("org", t.Influx.Org)
		q.Set("bucket", t.Influx.Bucket)
		in.writeURL = base + "/api/v2/write?" + q.Encode()
		if tok := t.Secrets["token"]; tok != "" {
			in.auth = "Token " + tok
		}
	}
	return in, nil
}

func compileLine(tpls config.InfluxTemplates) (*lineTemplates, error) {
	lt := &lineTemplates{}
	var err error
	if lt.measurement, err = tmpl.Compile(tpls.Measurement); err != nil {
		return nil, fmt.Errorf("measurement: %w", err)
	}
	for _, tag := range tpls.Tags {
		n, err := tmpl.Compile(tag.Name)
		if err != nil {
			return nil, fmt.Errorf("tag %q: %w", tag.Name, err)
		}
		v, err := tmpl.Compile(tag.Value)
		if err != nil {
			return nil, fmt.Errorf("tag %q: %w", tag.Name, err)
		}
		lt.tags = append(lt.tags, namedTemplate{n, v})
	}
	for _, f := range tpls.Fields {
		n, err := tmpl.Compile(f.Name)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", f.Name, err)
		}
		v, err := tmpl.Compile(f.Value)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", f.Name, err)
		}
		typ := f.Type
		if typ == "" {
			typ = "auto"
		}
		src := strings.TrimSpace(f.Value)
		native := typ == "auto" && (src == "{{ value }}" || src == "{{value}}")
		lt.fields = append(lt.fields, fieldTemplate{n, v, typ, native})
	}
	return lt, nil
}

// Line renders one record as a line of the protocol; "" with an error when the record has no
// measurement or no field.
func (in *Influx) Line(r *record.Record) (string, error) {
	lt := in.kinds[r.Kind]
	if lt == nil {
		return "", fmt.Errorf("no templates for kind %s", r.Kind)
	}
	ctx := r.Context()
	m, err := lt.measurement.Render(ctx)
	if err != nil {
		return "", fmt.Errorf("measurement: %w", err)
	}
	if m == "" {
		return "", errors.New("the measurement rendered empty")
	}
	var b strings.Builder
	b.WriteString(escape(m, measurementEsc))
	for _, tag := range lt.tags {
		name, err := tag.name.Render(ctx)
		if err != nil {
			return "", fmt.Errorf("tag: %w", err)
		}
		value, err := tag.value.Render(ctx)
		if err != nil {
			return "", fmt.Errorf("tag %s: %w", name, err)
		}
		if name == "" || value == "" {
			continue
		}
		b.WriteByte(',')
		b.WriteString(escape(name, tagEsc))
		b.WriteByte('=')
		b.WriteString(escape(value, tagEsc))
	}
	b.WriteByte(' ')
	n := 0
	var fieldErr error
	for _, f := range lt.fields {
		name, err := f.name.Render(ctx)
		if err != nil {
			return "", fmt.Errorf("field: %w", err)
		}
		if name == "" {
			continue
		}
		var out string
		if f.native {
			out = nativeField(r.Value)
		} else {
			text, err := f.value.Render(ctx)
			if err != nil {
				return "", fmt.Errorf("field %s: %w", name, err)
			}
			out, err = typedField(text, f.typ)
			if err != nil && fieldErr == nil {
				fieldErr = fmt.Errorf("field %s: %w", name, err)
			}
		}
		if out == "" {
			continue
		}
		if n > 0 {
			b.WriteByte(',')
		}
		b.WriteString(escape(name, tagEsc))
		b.WriteByte('=')
		b.WriteString(out)
		n++
	}
	if n == 0 {
		if fieldErr != nil {
			return "", fieldErr
		}
		return "", errors.New("no field rendered a value")
	}
	b.WriteByte(' ')
	b.WriteString(strconv.FormatInt(r.TS.UnixNano(), 10))
	return b.String(), nil
}

// nativeField writes a record's own value: numbers as floats (one type per field whatever the
// datapoint's TYPE says), bools as bools, strings quoted, nil as nothing.
func nativeField(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case bool:
		if x {
			return "true"
		}
		return "false"
	case float64:
		return floatField(x)
	case int64:
		return floatField(float64(x))
	case int:
		return floatField(float64(x))
	case string:
		return quote(x)
	default:
		return quote(fmt.Sprint(x))
	}
}

func floatField(f float64) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return ""
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// typedField converts a rendered text to a field of the type; "" is no field.
func typedField(text, typ string) (string, error) {
	if text == "" {
		return "", nil
	}
	switch typ {
	case "float":
		switch strings.ToLower(text) {
		case "true":
			return "1", nil
		case "false":
			return "0", nil
		}
		f, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return "", fmt.Errorf("%q is not a number", text)
		}
		return floatField(f), nil
	case "integer":
		switch strings.ToLower(text) {
		case "true":
			return "1i", nil
		case "false":
			return "0i", nil
		}
		f, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return "", fmt.Errorf("%q is not a number", text)
		}
		return strconv.FormatInt(int64(f), 10) + "i", nil
	case "boolean":
		switch strings.ToLower(text) {
		case "true", "t", "1", "on", "yes":
			return "true", nil
		case "false", "f", "0", "off", "no":
			return "false", nil
		}
		return "", fmt.Errorf("%q is not a boolean", text)
	case "string":
		return quote(text), nil
	default: // auto
		switch strings.ToLower(text) {
		case "true":
			return "true", nil
		case "false":
			return "false", nil
		}
		if f, err := strconv.ParseFloat(text, 64); err == nil && !strings.HasPrefix(text, "0x") {
			return floatField(f), nil
		}
		return quote(text), nil
	}
}

var (
	measurementEsc = strings.NewReplacer(",", `\,`, " ", `\ `, "\n", " ", "\r", "")
	tagEsc         = strings.NewReplacer(",", `\,`, "=", `\=`, " ", `\ `, "\n", " ", "\r", "")
	stringEsc      = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", "")
)

func escape(s string, r *strings.Replacer) string { return r.Replace(s) }

func quote(s string) string { return `"` + stringEsc.Replace(s) + `"` }

// Encode implements Target: the lines, split into payloads of at most Batch.MaxBytes.
func (in *Influx) Encode(batch []*record.Record) ([]Payload, []string) {
	var payloads []Payload
	var errs []string
	var b strings.Builder
	n := 0
	flush := func() {
		if n > 0 {
			payloads = append(payloads, Payload{Body: []byte(b.String()), Records: n, ContentType: "text/plain; charset=utf-8"})
		}
		b.Reset()
		n = 0
	}
	for _, r := range batch {
		line, err := in.Line(r)
		if err != nil {
			errs = append(errs, r.Summary()+": "+err.Error())
			continue
		}
		if b.Len() > 0 && b.Len()+len(line)+1 > in.cfg.Batch.MaxBytes {
			flush()
		}
		b.WriteString(line)
		b.WriteByte('\n')
		n++
	}
	flush()
	return payloads, errs
}

// Send implements Target.
func (in *Influx) Send(ctx context.Context, p Payload) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, in.writeURL, nil)
	if err != nil {
		return Permanent("%v", err)
	}
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	req.Header.Set("User-Agent", "lite-addon-forwarder")
	if in.auth != "" {
		req.Header.Set("Authorization", in.auth)
	} else if in.cfg.Influx.Version == "1" && in.cfg.Influx.User != "" {
		req.SetBasicAuth(in.cfg.Influx.User, in.cfg.Secrets["password"])
	}
	_, _, err = Do(in.client, req, in.cfg.Influx.Gzip, p.Body)
	return err
}

// Test implements Target: it writes the sample.
func (in *Influx) Test(ctx context.Context, sample *record.Record) (string, error) {
	line, err := in.Line(sample)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, in.writeURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	if in.auth != "" {
		req.Header.Set("Authorization", in.auth)
	} else if in.cfg.Influx.Version == "1" && in.cfg.Influx.User != "" {
		req.SetBasicAuth(in.cfg.Influx.User, in.cfg.Secrets["password"])
	}
	status, answer, err := Do(in.client, req, in.cfg.Influx.Gzip, []byte(line+"\n"))
	if err != nil {
		return "", err
	}
	msg := fmt.Sprintf("HTTP %d", status)
	if answer != "" {
		msg += ": " + answer
	}
	return msg + "\n" + line, nil
}

// Preview implements Target.
func (in *Influx) Preview(batch []*record.Record) (string, []string) {
	var b strings.Builder
	var errs []string
	for _, r := range batch {
		line, err := in.Line(r)
		if err != nil {
			errs = append(errs, r.Summary()+": "+err.Error())
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String(), errs
}
