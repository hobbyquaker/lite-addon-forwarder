package target

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hobbyquaker/lite-addon-forwarder/internal/config"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/record"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/tmpl"
)

// What the syslog and GELF targets share: a connection over UDP, TCP or TLS that is kept
// between batches and reopened after an error, the severity of a record, and the "*" wildcard
// of a template list that stands for every label of a metric or field of a log line.

// conn is one datagram or stream connection to host:port, dialled on first use.
type conn struct {
	network string // udp, tcp, tls
	addr    string
	tlsc    *tls.Config
	timeout time.Duration

	mu sync.Mutex
	c  net.Conn
}

func newConn(network, host string, port int, timeout time.Duration, tlsc *tls.Config) *conn {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &conn{network: network, addr: net.JoinHostPort(host, strconv.Itoa(port)), tlsc: tlsc, timeout: timeout}
}

// write sends the frames: over UDP each one as a datagram, over a stream all in one go. An
// error closes the connection, so the next call dials again, and is answered as retryable.
func (c *conn) write(ctx context.Context, frames [][]byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.c == nil {
		if err := c.dial(ctx); err != nil {
			return fmt.Errorf("%s: %w", c.addr, err)
		}
	}
	deadline := time.Now().Add(c.timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = c.c.SetWriteDeadline(deadline)
	if c.network == "udp" {
		for _, f := range frames {
			if _, err := c.c.Write(f); err != nil {
				c.closeLocked()
				return fmt.Errorf("%s: %w", c.addr, err)
			}
		}
		return nil
	}
	var all []byte
	for _, f := range frames {
		all = append(all, f...)
	}
	if _, err := c.c.Write(all); err != nil {
		c.closeLocked()
		return fmt.Errorf("%s: %w", c.addr, err)
	}
	return nil
}

func (c *conn) dial(ctx context.Context) error {
	d := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	switch c.network {
	case "udp":
		nc, err := d.DialContext(ctx, "udp", c.addr)
		if err != nil {
			return err
		}
		c.c = nc
	case "tcp":
		nc, err := d.DialContext(ctx, "tcp", c.addr)
		if err != nil {
			return err
		}
		c.c = nc
	case "tls":
		td := &tls.Dialer{NetDialer: d, Config: c.tlsc}
		nc, err := td.DialContext(ctx, "tcp", c.addr)
		if err != nil {
			return err
		}
		c.c = nc
	default:
		return fmt.Errorf("unknown transport %q", c.network)
	}
	return nil
}

func (c *conn) closeLocked() {
	if c.c != nil {
		_ = c.c.Close()
		c.c = nil
	}
}

// Close closes the connection.
func (c *conn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closeLocked()
	return nil
}

// tlsConfig builds a target's TLS configuration for a plain TLS connection to host.
func tlsConfig(t config.Target, host string) (*tls.Config, error) {
	tc := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}
	if t.TLS.Insecure {
		tc.InsecureSkipVerify = true //nolint:gosec // the user's explicit choice, shown as such in the UI
	}
	if strings.TrimSpace(t.TLS.CA) != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(t.TLS.CA)) {
			return nil, errors.New("the CA is not a PEM certificate")
		}
		tc.RootCAs = pool
	}
	if t.TLS.ServerName != "" {
		tc.ServerName = t.TLS.ServerName
	}
	return tc, nil
}

// Severity answers the syslog severity (0 emerg … 7 debug) of a record: the rendered template
// when there is one (a name or a number), else a log record's priority, else info.
func Severity(tpl *tmpl.Template, r *record.Record, ctx map[string]any) (int, error) {
	if tpl != nil {
		s, err := tpl.Render(ctx)
		if err != nil {
			return 0, fmt.Errorf("severity: %w", err)
		}
		if s = strings.TrimSpace(s); s != "" {
			n, ok := severityOf(s)
			if !ok {
				return 0, fmt.Errorf("severity: %q is not a syslog severity", s)
			}
			return n, nil
		}
	}
	if r.Kind == record.KindLog {
		if n, ok := severityOf(r.Priority); ok {
			return n, nil
		}
	}
	return 6, nil
}

var severityNames = map[string]int{"emerg": 0, "panic": 0, "alert": 1, "crit": 2, "critical": 2, "err": 3, "error": 3, "warning": 4, "warn": 4, "notice": 5, "info": 6, "informational": 6, "debug": 7}

func severityOf(s string) (int, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if n, ok := severityNames[s]; ok {
		return n, true
	}
	if n, err := strconv.Atoi(s); err == nil && n >= 0 && n <= 7 {
		return n, true
	}
	return 0, false
}

// namedTemplates is a compiled list of name/value templates.
type namedTemplates []namedTemplate

func compileNamed(l []config.NamedTemplate, what string) (namedTemplates, error) {
	out := make(namedTemplates, 0, len(l))
	for _, nt := range l {
		if strings.TrimSpace(nt.Name) == "" {
			continue
		}
		n, err := tmpl.Compile(strings.TrimSpace(nt.Name))
		if err != nil {
			return nil, fmt.Errorf("%s %q: %w", what, nt.Name, err)
		}
		v, err := tmpl.Compile(nt.Value)
		if err != nil {
			return nil, fmt.Errorf("%s %q: %w", what, nt.Name, err)
		}
		out = append(out, namedTemplate{n, v})
	}
	return out, nil
}

// render answers the rendered pairs in order: a name of "*" expands to the record's labels (a
// metric) or fields (a log line); an empty value is left out unless keepEmpty.
func (l namedTemplates) render(r *record.Record, ctx map[string]any, keepEmpty bool) ([][2]string, error) {
	var out [][2]string
	for _, nt := range l {
		n, err := nt.name.Render(ctx)
		if err != nil {
			return nil, fmt.Errorf("name: %w", err)
		}
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		if n == "*" {
			m := r.Labels
			if r.Kind == record.KindLog {
				m = r.Fields
			}
			for _, k := range sortedKeys(m) {
				if m[k] != "" || keepEmpty {
					out = append(out, [2]string{k, m[k]})
				}
			}
			continue
		}
		v, err := nt.value.Render(ctx)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", n, err)
		}
		if v == "" && !keepEmpty {
			continue
		}
		out = append(out, [2]string{n, v})
	}
	return out, nil
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sortStrings(out)
	return out
}

func sortStrings(l []string) {
	for i := 1; i < len(l); i++ {
		for j := i; j > 0 && l[j] < l[j-1]; j-- {
			l[j], l[j-1] = l[j-1], l[j]
		}
	}
}

// hostname is the system's host name for the headers, "homematic" when it cannot be read.
func hostname(override string) string {
	if override = strings.TrimSpace(override); override != "" {
		return override
	}
	if h, err := osHostname(); err == nil && h != "" {
		return h
	}
	return "homematic"
}
