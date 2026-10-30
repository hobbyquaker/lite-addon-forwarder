// Package target is the seam between the pipeline and a store: a Target encodes a batch of
// records into the store's format and sends it. An error that retrying cannot mend (bad data,
// a 400) is a *PermanentError, and the batch is dropped; every other error is retried.
package target

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/hobbyquaker/lite-addon-forwarder/internal/config"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/record"
)

// Target is one store.
type Target interface {
	// Encode renders a batch. Records a template refuses are left out and reported in errs
	// (one message per record); a payload with no record at all is answered as nil.
	Encode(batch []*record.Record) (payloads []Payload, errs []string)
	// Send delivers one payload.
	Send(ctx context.Context, p Payload) error
	// Test checks the connection with one test record and answers what the store said.
	Test(ctx context.Context, sample *record.Record) (string, error)
	// Preview answers the payloads as text for the UI.
	Preview(batch []*record.Record) (string, []string)
}

// Payload is one request's worth of encoded records.
type Payload struct {
	Body []byte
	// Records is how many records the body holds.
	Records int
	// URL and ContentType are per payload where a target groups by them (the webhook's URL).
	URL         string
	ContentType string
}

// PartialError says a store took the payload but refused Dropped of its records for their
// content: the pipeline counts them as dropped and the rest as sent, and does not retry.
type PartialError struct {
	Dropped int
	Err     error
}

func (e *PartialError) Error() string { return e.Err.Error() }
func (e *PartialError) Unwrap() error { return e.Err }

// Exposer is a target that serves its values itself (the Prometheus scrape endpoint).
type Exposer interface {
	// Expose writes the exposition.
	Expose(w io.Writer, now time.Time)
}

// PermanentError is an error retrying cannot mend.
type PermanentError struct{ Err error }

func (e *PermanentError) Error() string { return e.Err.Error() }
func (e *PermanentError) Unwrap() error { return e.Err }

// Permanent wraps an error as permanent.
func Permanent(format string, args ...any) error {
	return &PermanentError{Err: fmt.Errorf(format, args...)}
}

// IsPermanent tells whether an error is permanent.
func IsPermanent(err error) bool {
	var p *PermanentError
	return errors.As(err, &p)
}

// New builds the target of a configuration (with its secrets filled).
func New(t config.Target) (Target, error) {
	switch t.Type {
	case "influx":
		return NewInflux(t)
	case "webhook":
		return NewWebhook(t)
	case "elastic":
		return NewElastic(t)
	case "prometheus":
		return NewPrometheus(t)
	}
	return nil, fmt.Errorf("unknown target type %q", t.Type)
}

// Client builds the HTTP client of a target: its timeout, its TLS settings, no proxy from the
// environment surprises, and no redirects (a redirect of a write is a misconfiguration).
func Client(t config.Target) (*http.Client, error) {
	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns:          4,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
		ForceAttemptHTTP2:     true,
	}
	tc := &tls.Config{MinVersion: tls.VersionTLS12}
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
	tr.TLSClientConfig = tc
	timeout := time.Duration(t.TimeoutS) * time.Second
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &http.Client{Transport: tr, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

// Do sends a request and classifies the answer: 2xx is success; 400, 413 and 422 are permanent
// (the data is the problem); everything else - 401 and 403 (the credentials), 404 (the bucket),
// 429, 5xx, a network error - is retried, so a misconfiguration does not drop data while the
// user fixes it. The answer's body is in the error, cut to a line.
func Do(client *http.Client, req *http.Request, gz bool, body []byte) (status int, answer string, err error) {
	status, raw, err := doRaw(client, req, gz, body)
	return status, Line(string(raw)), err
}

// doRaw is Do with the answer's body whole (up to 1 MB), for a store whose answer is JSON.
func doRaw(client *http.Client, req *http.Request, gz bool, body []byte) (status int, answer []byte, err error) {
	if gz {
		var buf bytes.Buffer
		w := gzip.NewWriter(&buf)
		_, _ = w.Write(body)
		_ = w.Close()
		body = buf.Bytes()
		req.Header.Set("Content-Encoding", "gzip")
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	res, err := client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("%s: %w", req.URL.Host, unwrapURL(err))
	}
	defer res.Body.Close()
	answer, _ = io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		return res.StatusCode, answer, nil
	}
	msg := fmt.Sprintf("%s: HTTP %d", req.URL.Host, res.StatusCode)
	if line := Line(string(answer)); line != "" {
		msg += ": " + line
	}
	switch res.StatusCode {
	case 400, 413, 422:
		return res.StatusCode, answer, Permanent("%s", msg)
	}
	return res.StatusCode, answer, errors.New(msg)
}

func unwrapURL(err error) error {
	var u interface{ Unwrap() error }
	if errors.As(err, &u) && strings.HasPrefix(err.Error(), "Post ") {
		return u.Unwrap()
	}
	return err
}

// Line cuts a store's answer to one line of at most 200 characters.
func Line(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// Sample is the test record a Test sends: an event of a test datapoint, marked as such.
func Sample() *record.Record {
	v := 1.0
	return &record.Record{TS: time.Now(), Kind: record.KindEvent, Interface: "forwarder", Device: "TEST", Channel: 0, Address: "TEST:0", Key: "FORWARDER_TEST", Value: 1.0, ValueNum: &v, Type: "FLOAT", DeviceType: "forwarder-test", ChannelType: "TEST", DeviceName: "forwarder test", ChannelName: "forwarder test:0", Rooms: []string{}, Functions: []string{}, Source: "test"}
}
