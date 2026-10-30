// Package logsource follows the system's journal through occulited's log stream (GET
// /api/system/v1/log/stream, Server-Sent Events; the scope logs:read) and emits every line as a
// log record of the stream "journal". The lines occulited writes about a change of the system -
// who did what from where: they carry user= and remote= - are emitted once more as the stream
// "audit", with the line's attributes parsed into the record's fields. The stream is open only
// while a target's selection wants one of the two, and after a drop it resumes at the last
// cursor, so nothing is lost in between.
package logsource

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hobbyquaker/lite-addon-forwarder/internal/record"
)

// The streams.
const (
	StreamJournal = "journal"
	StreamAudit   = "audit"
)

// Options configure the follower.
type Options struct {
	// SystemURL is the system's URL (http://127.0.0.1); the log stream is below /api/system/v1.
	SystemURL string
	// HTTP carries the addon's token (addonkit.AuthTransport).
	HTTP *http.Client
	// Wants tells whether any target takes the stream; nil follows always.
	Wants func(stream string) bool
	// Emit receives every log record.
	Emit func(*record.Record)
	Log  func(format string, args ...any)
	Now  func() time.Time
}

// Follower is the running follower.
type Follower struct {
	o    Options
	kick chan struct{}

	mu     sync.Mutex
	stats  Stats
	units  map[string]bool
	cursor string
	cancel context.CancelFunc
}

// Stats is the follower's state for the UI.
type Stats struct {
	// Following is true while the stream is open; Streams names what is wanted (journal, audit).
	Following  bool      `json:"following"`
	Streams    []string  `json:"streams"`
	Lines      int64     `json:"lines"`
	Audit      int64     `json:"audit"`
	Reconnects int64     `json:"reconnects"`
	LastLine   time.Time `json:"last_line"`
	LastError  string    `json:"last_error"`
}

// New makes a follower.
func New(o Options) *Follower {
	if o.Log == nil {
		o.Log = func(string, ...any) {}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.HTTP == nil {
		o.HTTP = &http.Client{}
	}
	o.SystemURL = strings.TrimRight(o.SystemURL, "/")
	return &Follower{o: o, kick: make(chan struct{}, 1), stats: Stats{Streams: []string{}}, units: map[string]bool{}}
}

// Kick asks the follower to look at what is wanted now (a target changed).
func (f *Follower) Kick() {
	select {
	case f.kick <- struct{}{}:
	default:
	}
	f.mu.Lock()
	cancel := f.cancel
	f.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Stats answers the follower's state.
func (f *Follower) Stats() Stats {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.stats
	s.Streams = append([]string{}, f.stats.Streams...)
	return s
}

// Units answers the units seen so far, sorted (for the selection editor's suggestions).
func (f *Follower) Units() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.units))
	for u := range f.units {
		out = append(out, u)
	}
	sort.Strings(out)
	return out
}

func (f *Follower) wanted() []string {
	var out []string
	for _, s := range []string{StreamJournal, StreamAudit} {
		if f.o.Wants == nil || f.o.Wants(s) {
			out = append(out, s)
		}
	}
	return out
}

// Run follows until ctx ends.
func (f *Follower) Run(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		streams := f.wanted()
		f.mu.Lock()
		f.stats.Streams = streams
		if len(streams) == 0 {
			f.stats.Following = false
			f.cursor = ""
		}
		f.mu.Unlock()
		if len(streams) == 0 {
			select {
			case <-ctx.Done():
				return
			case <-f.kick:
			case <-time.After(30 * time.Second):
			}
			continue
		}
		sctx, cancel := context.WithCancel(ctx)
		f.mu.Lock()
		f.cancel = cancel
		f.mu.Unlock()
		err := f.follow(sctx, streams)
		cancel()
		f.mu.Lock()
		f.cancel = nil
		f.stats.Following = false
		if err != nil && ctx.Err() == nil {
			f.stats.LastError = err.Error()
			f.stats.Reconnects++
		}
		f.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			f.o.Log("log stream: %v", err)
		}
		if sctx.Err() != nil && ctx.Err() == nil && err == nil {
			// kicked: look again at once
			backoff = time.Second
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-f.kick:
			backoff = time.Second
		case <-time.After(backoff):
			if backoff < 30*time.Second {
				backoff *= 2
			}
		}
	}
}

// follow opens the stream and reads it until it ends.
func (f *Follower) follow(ctx context.Context, streams []string) error {
	q := url.Values{}
	q.Set("limit", "0")
	journal := false
	for _, s := range streams {
		journal = journal || s == StreamJournal
	}
	if !journal {
		q.Set("tag", "occulited")
	}
	f.mu.Lock()
	if f.cursor != "" {
		q.Set("after", f.cursor)
	}
	f.mu.Unlock()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.o.SystemURL+"/api/system/v1/log/stream?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	res, err := f.o.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		msg := strings.TrimSpace(string(b))
		if res.StatusCode == http.StatusForbidden {
			return fmt.Errorf("HTTP 403: the addon's token lacks the scope logs:read (reinstall or re-mint the token)")
		}
		if res.StatusCode == http.StatusNotImplemented {
			return errors.New("the system has no journal to follow (busybox syslog)")
		}
		return fmt.Errorf("HTTP %d: %s", res.StatusCode, firstLine(msg))
	}
	f.mu.Lock()
	f.stats.Following = true
	f.stats.LastError = ""
	f.mu.Unlock()
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	var data []string
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if len(data) > 0 {
				f.line(strings.Join(data, "\n"), journal)
				data = nil
			}
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := sc.Err(); err != nil && ctx.Err() == nil {
		return err
	}
	if ctx.Err() != nil {
		return nil
	}
	return errors.New("the stream ended")
}

// Line is one line of the system's log as the stream sends it.
type Line struct {
	Time      string `json:"time"`
	Timestamp string `json:"timestamp"`
	Host      string `json:"host"`
	Facility  string `json:"facility"`
	Severity  string `json:"severity"`
	Tag       string `json:"tag"`
	Unit      string `json:"unit"`
	PID       int    `json:"pid"`
	Message   string `json:"message"`
	Cursor    string `json:"cursor"`
	Area      string `json:"area"`
}

// line turns one event's data into records.
func (f *Follower) line(data string, journal bool) {
	var l Line
	if err := json.Unmarshal([]byte(data), &l); err != nil {
		return
	}
	ts := f.o.Now()
	if l.Timestamp != "" {
		if t, err := time.Parse(time.RFC3339Nano, l.Timestamp); err == nil {
			ts = t
		}
	}
	f.mu.Lock()
	if l.Cursor != "" {
		f.cursor = l.Cursor
	}
	f.stats.Lines++
	f.stats.LastLine = ts
	if l.Unit != "" && len(f.units) < 500 {
		f.units[l.Unit] = true
	}
	f.mu.Unlock()
	if journal {
		fields := map[string]string{}
		if l.Tag != "" {
			fields["tag"] = l.Tag
		}
		if l.PID != 0 {
			fields["pid"] = strconv.Itoa(l.PID)
		}
		if l.Host != "" {
			fields["host"] = l.Host
		}
		if l.Facility != "" {
			fields["facility"] = l.Facility
		}
		if l.Area != "" {
			fields["area"] = l.Area
		}
		f.emit(record.Log(ts, StreamJournal, l.Unit, l.Severity, l.Message, fields))
	}
	if l.Tag == "occulited" {
		if msg, attrs, ok := ParseAudit(l.Message); ok {
			if l.Host != "" {
				attrs["host"] = l.Host
			}
			f.mu.Lock()
			f.stats.Audit++
			f.mu.Unlock()
			f.emit(record.Log(ts, StreamAudit, l.Unit, l.Severity, msg, attrs))
		}
	}
}

func (f *Follower) emit(r *record.Record) {
	if f.o.Emit != nil {
		f.o.Emit(r)
	}
}

// ParseAudit recognises a line occulited wrote about a change: its message followed by slog's
// key=value attributes, among them user and remote. It answers the message, the attributes and
// whether the line is one.
func ParseAudit(line string) (string, map[string]string, bool) {
	toks := tokens(line)
	// the attributes are the longest suffix of key=value tokens
	first := len(toks)
	for first > 0 && isPair(toks[first-1].text) {
		first--
	}
	if first == len(toks) {
		return "", nil, false
	}
	attrs := map[string]string{}
	for _, t := range toks[first:] {
		k, v, _ := strings.Cut(t.text, "=")
		if strings.HasPrefix(v, `"`) {
			if u, err := strconv.Unquote(v); err == nil {
				v = u
			}
		}
		attrs[k] = v
	}
	if attrs["user"] == "" || attrs["remote"] == "" {
		return "", nil, false
	}
	msg := line
	if first < len(toks) {
		msg = strings.TrimSpace(line[:toks[first].start])
	}
	return msg, attrs, true
}

type token struct {
	start int
	text  string
}

// tokens splits a line at spaces, keeping quoted values whole.
func tokens(s string) []token {
	var out []token
	i := 0
	for i < len(s) {
		for i < len(s) && s[i] == ' ' {
			i++
		}
		if i >= len(s) {
			break
		}
		start := i
		for i < len(s) && s[i] != ' ' {
			if s[i] == '"' {
				i++
				for i < len(s) && s[i] != '"' {
					if s[i] == '\\' {
						i++
					}
					i++
				}
				if i < len(s) {
					i++
				}
				continue
			}
			i++
		}
		if i > len(s) {
			i = len(s)
		}
		out = append(out, token{start, s[start:i]})
	}
	return out
}

func isPair(t string) bool {
	k, v, ok := strings.Cut(t, "=")
	if !ok || k == "" || v == "" {
		return false
	}
	for i, c := range k {
		if !(c == '_' || c == '.' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (i > 0 && c >= '0' && c <= '9')) {
			return false
		}
	}
	return true
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
