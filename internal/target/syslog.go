package target

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hobbyquaker/lite-addon-forwarder/internal/config"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/record"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/tmpl"
)

var osHostname = os.Hostname

// Syslog sends RFC 5424 messages over UDP, TCP or TLS: the header's APP-NAME and MSGID, a
// structured data element and the MSG are templates; the severity is a log line's priority
// (or a template), the facility a setting. Over a stream the frames are octet-counted (RFC
// 6587) or newline-terminated.
type Syslog struct {
	cfg      config.Target
	c        *conn
	host     string
	severity *tmpl.Template
	app      *tmpl.Template
	msgid    *tmpl.Template
	msg      *tmpl.Template
	sd       namedTemplates
}

// NewSyslog builds a syslog target.
func NewSyslog(t config.Target) (*Syslog, error) {
	if t.Syslog == nil {
		return nil, errors.New("syslog settings missing")
	}
	y := t.Syslog
	s := &Syslog{cfg: t, host: hostname(y.Hostname)}
	var err error
	if strings.TrimSpace(y.Severity) != "" {
		if s.severity, err = tmpl.Compile(y.Severity); err != nil {
			return nil, fmt.Errorf("severity: %w", err)
		}
	}
	if s.app, err = tmpl.Compile(y.AppName); err != nil {
		return nil, fmt.Errorf("app name: %w", err)
	}
	if s.msgid, err = tmpl.Compile(y.MsgID); err != nil {
		return nil, fmt.Errorf("msgid: %w", err)
	}
	if s.msg, err = tmpl.Compile(y.Message); err != nil {
		return nil, fmt.Errorf("message: %w", err)
	}
	if s.sd, err = compileNamed(y.SDParams, "parameter"); err != nil {
		return nil, err
	}
	var tlsc *tls.Config
	if y.Transport == "tls" {
		tc, err := tlsConfig(t, y.Host)
		if err != nil {
			return nil, err
		}
		tlsc = tc
	}
	s.c = newConn(y.Transport, y.Host, y.Port, time.Duration(t.TimeoutS)*time.Second, tlsc)
	return s, nil
}

// Message renders one record as an RFC 5424 message (without framing).
func (s *Syslog) Message(r *record.Record) ([]byte, error) {
	ctx := r.Context()
	sev, err := Severity(s.severity, r, ctx)
	if err != nil {
		return nil, err
	}
	app, err := s.app.Render(ctx)
	if err != nil {
		return nil, fmt.Errorf("app name: %w", err)
	}
	msgid, err := s.msgid.Render(ctx)
	if err != nil {
		return nil, fmt.Errorf("msgid: %w", err)
	}
	msg, err := s.msg.Render(ctx)
	if err != nil {
		return nil, fmt.Errorf("message: %w", err)
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "<%d>1 %s %s %s - %s ", s.cfg.Syslog.Facility*8+sev, r.TS.UTC().Format("2006-01-02T15:04:05.000000Z07:00"), sdName(s.host, 255), sdName(app, 48), sdName(msgid, 32))
	if s.cfg.Syslog.SDID != "" {
		params, err := s.sd.render(r, ctx, false)
		if err != nil {
			return nil, fmt.Errorf("structured data: %w", err)
		}
		b.WriteByte('[')
		b.WriteString(s.cfg.Syslog.SDID)
		for _, p := range params {
			name := sdName(p[0], 32)
			if name == "-" {
				continue
			}
			b.WriteByte(' ')
			b.WriteString(name)
			b.WriteString(`="`)
			b.WriteString(sdValue(p[1]))
			b.WriteByte('"')
		}
		b.WriteByte(']')
	} else {
		b.WriteByte('-')
	}
	if msg = strings.TrimRight(msg, "\r\n"); msg != "" {
		b.WriteByte(' ')
		b.WriteString(strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(msg))
	}
	return b.Bytes(), nil
}

// sdName cuts a header field or SD-PARAM name to printable US-ASCII without spaces, at most n
// characters; "-" when nothing is left.
func sdName(s string, n int) string {
	var b strings.Builder
	for _, c := range s {
		if c > 32 && c < 127 && c != '=' && c != ']' && c != '"' {
			b.WriteRune(c)
		} else {
			b.WriteByte('_')
		}
		if b.Len() >= n {
			break
		}
	}
	if b.Len() == 0 {
		return "-"
	}
	return b.String()
}

// sdValue escapes an SD-PARAM value: ", \ and ] get a backslash.
func sdValue(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, `]`, `\]`).Replace(s)
}

// frame wraps a message for the transport: octet-counted over a stream by default, a newline
// for framing newline, bare over UDP.
func (s *Syslog) frame(msg []byte) []byte {
	if s.cfg.Syslog.Transport == "udp" {
		return msg
	}
	if s.cfg.Syslog.Framing == "newline" {
		return append(msg, '\n')
	}
	return append([]byte(strconv.Itoa(len(msg))+" "), msg...)
}

// Encode implements Target: the payload is the octet-counted frames of the batch (the
// transport's own framing is applied at Send).
func (s *Syslog) Encode(batch []*record.Record) ([]Payload, []string) {
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
		msg, err := s.Message(r)
		if err != nil {
			errs = append(errs, r.Summary()+": "+err.Error())
			continue
		}
		if b.Len() > 0 && b.Len()+len(msg)+8 > s.cfg.Batch.MaxBytes {
			flush()
		}
		b.WriteString(strconv.Itoa(len(msg)))
		b.WriteByte(' ')
		b.Write(msg)
		n++
	}
	flush()
	return payloads, errs
}

// unframe splits octet-counted frames.
func unframe(body []byte) ([][]byte, error) {
	var out [][]byte
	for len(body) > 0 {
		sp := bytes.IndexByte(body, ' ')
		if sp < 0 {
			return nil, errors.New("bad frame")
		}
		n, err := strconv.Atoi(string(body[:sp]))
		if err != nil || n < 0 || sp+1+n > len(body) {
			return nil, errors.New("bad frame length")
		}
		out = append(out, body[sp+1:sp+1+n])
		body = body[sp+1+n:]
	}
	return out, nil
}

// Send implements Target.
func (s *Syslog) Send(ctx context.Context, p Payload) error {
	msgs, err := unframe(p.Body)
	if err != nil {
		return Permanent("%v", err)
	}
	frames := make([][]byte, 0, len(msgs))
	for _, m := range msgs {
		frames = append(frames, s.frame(m))
	}
	return s.c.write(ctx, frames)
}

// Test implements Target: it sends the sample (UDP cannot confirm the receipt; a stream at
// least confirms the connection).
func (s *Syslog) Test(ctx context.Context, sample *record.Record) (string, error) {
	msg, err := s.Message(sample)
	if err != nil {
		return "", err
	}
	if err := s.c.write(ctx, [][]byte{s.frame(msg)}); err != nil {
		return "", err
	}
	note := "sent over " + strings.ToUpper(s.cfg.Syslog.Transport)
	if s.cfg.Syslog.Transport == "udp" {
		note += " (a datagram is not confirmed; check the receiver)"
	}
	return note + "\n" + string(msg), nil
}

// Preview implements Target.
func (s *Syslog) Preview(batch []*record.Record) (string, []string) {
	var b strings.Builder
	var errs []string
	for _, r := range batch {
		msg, err := s.Message(r)
		if err != nil {
			errs = append(errs, r.Summary()+": "+err.Error())
			continue
		}
		b.Write(msg)
		b.WriteByte('\n')
	}
	return b.String(), errs
}

// Close closes the connection.
func (s *Syslog) Close() error { return s.c.Close() }
