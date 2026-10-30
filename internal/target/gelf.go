package target

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/hobbyquaker/lite-addon-forwarder/internal/config"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/record"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/tmpl"
)

// GELF sends Graylog Extended Log Format 1.1 messages: over UDP as datagrams (gzip or zlib
// compressed, chunked above 8 KB), over TCP or TLS null-delimited and uncompressed. The short
// and the full message are templates, the level a log line's priority (or a template), the
// additional fields a template list - or every field of the record.
type GELF struct {
	cfg   config.Target
	c     *conn
	host  string
	short *tmpl.Template
	full  *tmpl.Template
	level *tmpl.Template
	flds  namedTemplates
}

// NewGELF builds a GELF target.
func NewGELF(t config.Target) (*GELF, error) {
	if t.GELF == nil {
		return nil, errors.New("gelf settings missing")
	}
	g := t.GELF
	out := &GELF{cfg: t, host: hostname("")}
	var err error
	if out.short, err = tmpl.Compile(g.ShortMessage); err != nil {
		return nil, fmt.Errorf("short message: %w", err)
	}
	if strings.TrimSpace(g.FullMessage) != "" {
		if out.full, err = tmpl.Compile(g.FullMessage); err != nil {
			return nil, fmt.Errorf("full message: %w", err)
		}
	}
	if strings.TrimSpace(g.Level) != "" {
		if out.level, err = tmpl.Compile(g.Level); err != nil {
			return nil, fmt.Errorf("level: %w", err)
		}
	}
	if out.flds, err = compileNamed(g.Fields, "field"); err != nil {
		return nil, err
	}
	var tlsc *tls.Config
	if g.Transport == "tls" {
		if tlsc, err = tlsConfig(t, g.Host); err != nil {
			return nil, err
		}
	}
	out.c = newConn(g.Transport, g.Host, g.Port, time.Duration(t.TimeoutS)*time.Second, tlsc)
	return out, nil
}

var gelfFieldName = regexp.MustCompile(`[^\w.\-]`)

// Message renders one record as a GELF document (uncompressed JSON).
func (g *GELF) Message(r *record.Record) ([]byte, error) {
	ctx := r.Context()
	short, err := g.short.Render(ctx)
	if err != nil {
		return nil, fmt.Errorf("short message: %w", err)
	}
	if short = strings.TrimSpace(short); short == "" {
		return nil, errors.New("the short message rendered empty")
	}
	level, err := Severity(g.level, r, ctx)
	if err != nil {
		return nil, strings2err("level", err)
	}
	doc := map[string]any{
		"version":       "1.1",
		"host":          g.host,
		"short_message": short,
		"timestamp":     float64(r.TS.UnixNano()) / 1e9,
		"level":         level,
	}
	if g.full != nil {
		full, err := g.full.Render(ctx)
		if err != nil {
			return nil, fmt.Errorf("full message: %w", err)
		}
		if full != "" {
			doc["full_message"] = full
		}
	}
	if g.cfg.GELF.AllFields {
		for k, v := range r.Map() {
			switch k {
			case "ts", "rooms", "functions", "labels", "fields":
				switch x := v.(type) {
				case []any:
					parts := make([]string, 0, len(x))
					for _, p := range x {
						parts = append(parts, fmt.Sprint(p))
					}
					if len(parts) > 0 {
						doc["_"+k] = strings.Join(parts, ",")
					}
				case map[string]any:
					for kk, vv := range x {
						if n := gelfName(kk); n != "" {
							doc["_"+n] = vv
						}
					}
				case string:
					doc["_"+k] = x
				}
			case "value":
				if r.Kind != record.KindLog {
					doc["_value"] = v
				}
			case "value_num", "value_text", "type":
				if r.Kind != record.KindLog && v != nil && v != "" {
					doc["_"+k] = v
				}
			default:
				if v != nil && v != "" {
					doc["_"+gelfName(k)] = v
				}
			}
		}
	}
	pairs, err := g.flds.render(r, ctx, false)
	if err != nil {
		return nil, fmt.Errorf("fields: %w", err)
	}
	for _, p := range pairs {
		if n := gelfName(p[0]); n != "" {
			doc["_"+n] = p[1]
		}
	}
	delete(doc, "_id")
	return json.Marshal(doc)
}

func strings2err(prefix string, err error) error { return fmt.Errorf("%s: %w", prefix, err) }

// gelfName makes a valid additional field name: letters, digits, _, - and . only.
func gelfName(s string) string {
	s = gelfFieldName.ReplaceAllString(strings.TrimSpace(s), "_")
	if s == "id" {
		return ""
	}
	return s
}

// Encode implements Target: one document per record, null-delimited in the payload.
func (g *GELF) Encode(batch []*record.Record) ([]Payload, []string) {
	var payloads []Payload
	var errs []string
	var b bytes.Buffer
	n := 0
	flush := func() {
		if n > 0 {
			payloads = append(payloads, Payload{Body: append([]byte(nil), b.Bytes()...), Records: n})
		}
		b.Reset()
		n = 0
	}
	for _, r := range batch {
		doc, err := g.Message(r)
		if err != nil {
			errs = append(errs, r.Summary()+": "+err.Error())
			continue
		}
		if b.Len() > 0 && b.Len()+len(doc)+1 > g.cfg.Batch.MaxBytes {
			flush()
		}
		b.Write(doc)
		b.WriteByte(0)
		n++
	}
	flush()
	return payloads, errs
}

// gelfChunkSize is the datagram size of a chunk: 1420 bytes, below the usual MTU, what the
// GELF libraries use (a larger datagram is fragmented by IP, and fragments are dropped more
// often than not).
const gelfChunkSize = 1420
const gelfChunkHeader = 12

// datagrams turns one document into its UDP datagrams: compressed as configured, chunked when
// larger than one datagram (at most 128 chunks; a larger document is refused).
func (g *GELF) datagrams(doc []byte) ([][]byte, error) {
	body := doc
	switch g.cfg.GELF.Compression {
	case "gzip":
		var buf bytes.Buffer
		w := gzip.NewWriter(&buf)
		_, _ = w.Write(doc)
		_ = w.Close()
		body = buf.Bytes()
	case "zlib":
		var buf bytes.Buffer
		w := zlib.NewWriter(&buf)
		_, _ = w.Write(doc)
		_ = w.Close()
		body = buf.Bytes()
	}
	if len(body) <= gelfChunkSize {
		return [][]byte{body}, nil
	}
	size := gelfChunkSize - gelfChunkHeader
	count := (len(body) + size - 1) / size
	if count > 128 {
		return nil, fmt.Errorf("the message is too large for GELF over UDP (%d bytes, %d chunks)", len(body), count)
	}
	var id [8]byte
	_, _ = rand.Read(id[:])
	out := make([][]byte, 0, count)
	for i := 0; i < count; i++ {
		end := (i + 1) * size
		if end > len(body) {
			end = len(body)
		}
		chunk := make([]byte, 0, gelfChunkHeader+end-i*size)
		chunk = append(chunk, 0x1e, 0x0f)
		chunk = append(chunk, id[:]...)
		chunk = append(chunk, byte(i), byte(count))
		chunk = append(chunk, body[i*size:end]...)
		out = append(out, chunk)
	}
	return out, nil
}

// frames turns the documents of a payload into the transport's frames.
func (g *GELF) frames(body []byte) ([][]byte, error) {
	docs := bytes.Split(bytes.TrimSuffix(body, []byte{0}), []byte{0})
	var frames [][]byte
	for _, d := range docs {
		if len(d) == 0 {
			continue
		}
		if g.cfg.GELF.Transport == "udp" {
			dg, err := g.datagrams(d)
			if err != nil {
				return nil, Permanent("%v", err)
			}
			frames = append(frames, dg...)
			continue
		}
		frames = append(frames, append(append([]byte(nil), d...), 0))
	}
	return frames, nil
}

// Send implements Target.
func (g *GELF) Send(ctx context.Context, p Payload) error {
	frames, err := g.frames(p.Body)
	if err != nil {
		return err
	}
	return g.c.write(ctx, frames)
}

// Test implements Target.
func (g *GELF) Test(ctx context.Context, sample *record.Record) (string, error) {
	doc, err := g.Message(sample)
	if err != nil {
		return "", err
	}
	frames, err := g.frames(append(doc, 0))
	if err != nil {
		return "", err
	}
	if err := g.c.write(ctx, frames); err != nil {
		return "", err
	}
	note := "sent over " + strings.ToUpper(g.cfg.GELF.Transport)
	if g.cfg.GELF.Transport == "udp" {
		note += fmt.Sprintf(" (%s, %d datagram(s); not confirmed - check the receiver)", g.cfg.GELF.Compression, len(frames))
	}
	return note + "\n" + string(doc), nil
}

// Preview implements Target.
func (g *GELF) Preview(batch []*record.Record) (string, []string) {
	var b strings.Builder
	var errs []string
	for _, r := range batch {
		doc, err := g.Message(r)
		if err != nil {
			errs = append(errs, r.Summary()+": "+err.Error())
			continue
		}
		b.Write(doc)
		b.WriteByte('\n')
	}
	return b.String(), errs
}

// Close closes the connection.
func (g *GELF) Close() error { return g.c.Close() }
