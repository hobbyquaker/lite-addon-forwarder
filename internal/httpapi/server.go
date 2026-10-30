// Package httpapi is the addon's own HTTP server: the Svelte app's files and the JSON API under
// one base path (/addons/forwarder/), which lighttpd proxies to the loopback port. Every request
// carries the shell's session, confirmed through the kit; writes carry the kit's CSRF header.
// docs/api.md documents the routes.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hobbyquaker/ccu-addon-kit/go/addonkit"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/config"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/logsource"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/metrics"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/pipeline"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/record"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/selection"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/source"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/target"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/tmpl"
)

// Options configure the server.
type Options struct {
	// BasePath is where the app is served, /addons/forwarder (no trailing slash).
	BasePath string
	// WWW is the directory with the built app.
	WWW      string
	Store    *config.Store
	Pipeline *pipeline.Manager
	Source   *source.Source
	// Metrics is the poller; nil when none runs (a test).
	Metrics *metrics.Poller
	// Logs is the journal follower; nil when none runs (a test).
	Logs *logsource.Follower
	// Wrap is the session check; nil: none (development only).
	Wrap func(http.Handler) http.Handler
	// WrapScrape is the session check of the Prometheus scrape endpoints: the user level, which
	// an API token with the scope addon:<id> holds (the system's gate admits such a token on
	// /addons/<id>/, so a scraper on the LAN carries one as its Bearer credential); nil: none.
	WrapScrape func(http.Handler) http.Handler
	Version    string
	Started    time.Time
	// Hub wakes the status streams; the pipeline's OnChange calls its Broadcast.
	Hub *Hub
}

// Hub fans a change signal out to the open status streams.
type Hub struct {
	mu   sync.Mutex
	subs map[chan struct{}]bool
}

// NewHub makes a hub.
func NewHub() *Hub { return &Hub{subs: map[chan struct{}]bool{}} }

// Broadcast wakes every stream; a stream that is busy is not waited for.
func (h *Hub) Broadcast() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Subscribe answers a channel and the function that ends the subscription.
func (h *Hub) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	h.subs[ch] = true
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs, ch)
		h.mu.Unlock()
	}
}

// Routes lists every "METHOD /path" the API serves; the readme's table is held to it by a test.
var Routes []string

func handle(mux *http.ServeMux, pattern string, h http.HandlerFunc) {
	Routes = append(Routes, pattern)
	mux.HandleFunc(pattern, h)
}

// New builds the handler.
func New(o Options) http.Handler {
	o.BasePath = strings.TrimRight(o.BasePath, "/")
	Routes = nil
	api := http.NewServeMux()
	handle(api, "GET /api/v1/health", o.health)
	handle(api, "GET /api/v1/inventory", o.inventory)
	handle(api, "GET /api/v1/recent", o.recent)
	handle(api, "GET /api/v1/selections", o.selections)
	handle(api, "POST /api/v1/selections", o.createSelection)
	handle(api, "PUT /api/v1/selections/{id}", o.updateSelection)
	handle(api, "DELETE /api/v1/selections/{id}", o.deleteSelection)
	handle(api, "POST /api/v1/selections/match", o.matchSelection)
	handle(api, "GET /api/v1/targets", o.targets)
	handle(api, "POST /api/v1/targets", o.createTarget)
	handle(api, "PUT /api/v1/targets/{id}", o.updateTarget)
	handle(api, "DELETE /api/v1/targets/{id}", o.deleteTarget)
	handle(api, "POST /api/v1/targets/{id}/enabled", o.enableTarget)
	handle(api, "POST /api/v1/targets/{id}/clear", o.clearTarget)
	handle(api, "POST /api/v1/targets/test", o.testTarget)
	handle(api, "POST /api/v1/targets/preview", o.previewTarget)
	handle(api, "GET /api/v1/status", o.status)
	handle(api, "GET /api/v1/stream", o.stream)
	handle(api, "POST /api/v1/templates/render", o.renderTemplate)
	handle(api, "GET /api/v1/templates/help", o.templateHelp)
	handle(api, "GET /api/v1/presets", o.presets)
	handle(api, "GET /api/v1/settings", o.settings)
	handle(api, "PUT /api/v1/settings", o.putSettings)
	api.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		addonkit.WriteError(w, http.StatusNotFound, "not-found", r.URL.Path)
	})

	root := http.NewServeMux()
	root.Handle("/api/", addonkit.CSRF(api))
	root.Handle("/", addonkit.AppFiles(o.WWW))

	var h http.Handler = http.StripPrefix(o.BasePath, root)
	if o.Wrap != nil {
		h = o.Wrap(h)
	}
	// the Prometheus scrape endpoints need the user level only, which an API token of the scope
	// addon:<id> holds - the admin level of the configuration API would shut a scraper out
	scrape := http.NewServeMux()
	scrape.HandleFunc("GET /metrics/{id}", o.scrape)
	Routes = append(Routes, "GET /metrics/{id}")
	var sh http.Handler = http.StripPrefix(o.BasePath, scrape)
	if o.WrapScrape != nil {
		sh = o.WrapScrape(sh)
	}
	outer := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, o.BasePath+"/metrics/") {
			sh.ServeHTTP(w, r)
			return
		}
		h.ServeHTTP(w, r)
	})
	return guard(outer)
}

// scrape is GET /metrics/{id}: the exposition of a Prometheus target in scrape mode, plus the
// forwarder's own figures.
func (o *Options) scrape(w http.ResponseWriter, r *http.Request) {
	e, ok := o.Pipeline.Exposer(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	e.Expose(w, time.Now())
	o.ownMetrics(w)
}

// ownMetrics writes the forwarder's own figures: the source and every target's counters.
func (o *Options) ownMetrics(w io.Writer) {
	src := o.Source.Stats()
	fmt.Fprintf(w, "# TYPE forwarder_source_connected gauge\nforwarder_source_connected %d\n", b2i(src.Connected))
	fmt.Fprintf(w, "# TYPE forwarder_source_events_total counter\nforwarder_source_events_total %d\n", src.Events)
	fmt.Fprintf(w, "# TYPE forwarder_source_datapoints gauge\nforwarder_source_datapoints %d\n", src.Datapoints)
	ms := o.metricsStats()
	fmt.Fprintf(w, "# TYPE forwarder_metrics_polls_total counter\nforwarder_metrics_polls_total %d\n", ms.Polls)
	fmt.Fprintf(w, "# TYPE forwarder_metrics_poll_errors gauge\nforwarder_metrics_poll_errors %d\n", len(ms.Errors))
	ls := o.logsStats()
	fmt.Fprintf(w, "# TYPE forwarder_logs_following gauge\nforwarder_logs_following %d\n", b2i(ls.Following))
	fmt.Fprintf(w, "# TYPE forwarder_logs_lines_total counter\nforwarder_logs_lines_total %d\n", ls.Lines)
	statuses := o.Pipeline.Status()
	type col struct{ name, typ string }
	cols := []col{{"sent_total", "counter"}, {"dropped_total", "counter"}, {"retried_total", "counter"}, {"batches_total", "counter"}, {"queued", "gauge"}, {"spilled", "gauge"}, {"lag_seconds", "gauge"}, {"connected", "gauge"}}
	for _, c := range cols {
		fmt.Fprintf(w, "# TYPE forwarder_target_%s %s\n", c.name, c.typ)
		for _, s := range statuses {
			var v string
			switch c.name {
			case "sent_total":
				v = strconv.FormatInt(s.Sent, 10)
			case "dropped_total":
				v = strconv.FormatInt(s.Dropped, 10)
			case "retried_total":
				v = strconv.FormatInt(s.Retried, 10)
			case "batches_total":
				v = strconv.FormatInt(s.Batches, 10)
			case "queued":
				v = strconv.Itoa(s.Queued)
			case "spilled":
				v = strconv.Itoa(s.Spilled)
			case "lag_seconds":
				v = strconv.FormatFloat(float64(s.LagMS)/1000, 'g', -1, 64)
			case "connected":
				v = strconv.Itoa(b2i(s.Connected))
			}
			fmt.Fprintf(w, "forwarder_target_%s{target=%q,type=%q,state=%q} %s\n", c.name, s.Name, s.Type, s.State, v)
		}
	}
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p := r.URL.Path; p != "/" && (path.Clean(p) != strings.TrimSuffix(p, "/") && path.Clean(p) != p) {
			http.NotFound(w, r)
			return
		}
		if strings.Contains(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(v); err != nil {
		addonkit.WriteError(w, http.StatusBadRequest, "invalid", err.Error())
		return false
	}
	return true
}

func writeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, config.ErrNotFound):
		addonkit.WriteError(w, http.StatusNotFound, "not-found", err.Error())
	case errors.Is(err, config.ErrInUse):
		addonkit.WriteError(w, http.StatusConflict, "in-use", err.Error())
	default:
		addonkit.WriteError(w, http.StatusBadRequest, "invalid", err.Error())
	}
}

// Health is GET /api/v1/health.
type Health struct {
	Version    string          `json:"version"`
	Started    time.Time       `json:"started"`
	UptimeS    int64           `json:"uptime_s"`
	Source     source.Stats    `json:"source"`
	Metrics    metrics.Stats   `json:"metrics"`
	Logs       logsource.Stats `json:"logs"`
	Targets    int             `json:"targets"`
	Selections int             `json:"selections"`
	Types      []string        `json:"types"`
}

func (o *Options) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, Health{Version: o.Version, Started: o.Started, UptimeS: int64(time.Since(o.Started).Seconds()), Source: o.Source.Stats(), Metrics: o.metricsStats(), Logs: o.logsStats(), Targets: len(o.Store.Targets()), Selections: len(o.Store.Selections()), Types: config.Types})
}

func (o *Options) metricsStats() metrics.Stats {
	if o.Metrics == nil {
		return metrics.Stats{Groups: []string{}, Errors: map[string]string{}}
	}
	return o.Metrics.Stats()
}

func (o *Options) logsStats() logsource.Stats {
	if o.Logs == nil {
		return logsource.Stats{Streams: []string{}}
	}
	return o.Logs.Stats()
}

func (o *Options) logUnits() []string {
	if o.Logs == nil {
		return []string{}
	}
	return o.Logs.Units()
}

// InventoryView is GET /api/v1/inventory: the devices and the metric groups and log streams
// a selection can name.
type InventoryView struct {
	source.Inventory
	MetricGroups []metrics.GroupInfo `json:"metric_groups"`
	LogStreams   []StreamInfo        `json:"log_streams"`
	// LogUnits are the units seen in the journal so far, for the editor's suggestions.
	LogUnits []string `json:"log_units"`
}

// StreamInfo documents one log stream.
type StreamInfo struct {
	Name  string            `json:"name"`
	Title map[string]string `json:"title"`
	Doc   map[string]string `json:"doc"`
}

// LogStreams lists the log streams: the system's journal and the audit lines occulited writes
// about changes of the system.
var LogStreams = []StreamInfo{
	{Name: logsource.StreamJournal, Title: map[string]string{"de": "Journal", "en": "Journal"}, Doc: map[string]string{
		"de": "Jedes Protokoll des Systems (systemd-journal): Unit, Priorität, Nachricht; Felder tag, pid, host, facility. Nach Units und Priorität einschränkbar.",
		"en": "Every log line of the system (the systemd journal): unit, priority, message; fields tag, pid, host, facility. Narrowed by units and priority.",
	}},
	{Name: logsource.StreamAudit, Title: map[string]string{"de": "Audit (wer hat was geändert)", "en": "Audit (who changed what)"}, Doc: map[string]string{
		"de": "Die Zeilen, die das System über Änderungen schreibt - Anmeldungen, Einstellungen, Addons, Firmware - mit user, remote, method, route und status als Feldern.",
		"en": "The lines the system writes about changes - logins, settings, addons, firmware - with user, remote, method, route and status as fields.",
	}},
}

func (o *Options) inventory(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, InventoryView{Inventory: o.Source.Inventory(), MetricGroups: metrics.Groups, LogStreams: LogStreams, LogUnits: o.logUnits()})
}

func (o *Options) statusMessage() map[string]any {
	return map[string]any{"source": o.Source.Stats(), "metrics": o.metricsStats(), "logs": o.logsStats(), "targets": o.Pipeline.Status()}
}

// RecentRecord is a record with the targets that would take it.
type RecentRecord struct {
	*record.Record
	Summary string   `json:"summary"`
	Targets []string `json:"targets"`
}

func (o *Options) recent(w http.ResponseWriter, r *http.Request) {
	n := queryInt(r, "n", 50, 1, 1000)
	var m *selection.Matcher
	if id := r.URL.Query().Get("selection"); id != "" {
		sel, ok := o.Store.Selection(id)
		if !ok {
			addonkit.WriteError(w, 404, "not-found", "selection")
			return
		}
		m = selection.New(sel)
	}
	recs := o.Source.Recent(n, m)
	out := make([]RecentRecord, 0, len(recs))
	for _, rec := range recs {
		out = append(out, RecentRecord{Record: rec, Summary: rec.Summary(), Targets: nz(o.Pipeline.Matches(rec))})
	}
	writeJSON(w, 200, out)
}

func nz(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

func queryInt(r *http.Request, name string, def, lo, hi int) int {
	n, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil || n < lo || n > hi {
		return def
	}
	return n
}

// SelectionView is a selection with its count.
type SelectionView struct {
	selection.Selection
	Count   source.Count `json:"count"`
	UsedBy  []string     `json:"used_by"`
	Targets int          `json:"targets"`
}

func (o *Options) view(sel selection.Selection) SelectionView {
	v := SelectionView{Selection: sel, Count: o.Source.Count(selection.New(sel)), UsedBy: []string{}}
	for _, t := range o.Store.Targets() {
		if t.Selection == sel.ID {
			v.UsedBy = append(v.UsedBy, t.Name)
		}
	}
	v.Targets = len(v.UsedBy)
	return v
}

func (o *Options) selections(w http.ResponseWriter, r *http.Request) {
	sels := o.Store.Selections()
	out := make([]SelectionView, 0, len(sels))
	for _, s := range sels {
		out = append(out, o.view(s))
	}
	writeJSON(w, 200, out)
}

func (o *Options) createSelection(w http.ResponseWriter, r *http.Request) {
	var sel selection.Selection
	if !decode(w, r, &sel) {
		return
	}
	sel.ID = ""
	sel, err := o.Store.UpsertSelection(sel)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 201, o.view(sel))
}

func (o *Options) updateSelection(w http.ResponseWriter, r *http.Request) {
	var sel selection.Selection
	if !decode(w, r, &sel) {
		return
	}
	sel.ID = r.PathValue("id")
	sel, err := o.Store.UpsertSelection(sel)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, o.view(sel))
}

func (o *Options) deleteSelection(w http.ResponseWriter, r *http.Request) {
	if err := o.Store.DeleteSelection(r.PathValue("id")); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(204)
}

// Match is POST /api/v1/selections/match: the count and a sample of an unsaved selection.
type Match struct {
	Count  source.Count   `json:"count"`
	Recent []RecentRecord `json:"recent"`
}

func (o *Options) matchSelection(w http.ResponseWriter, r *http.Request) {
	var sel selection.Selection
	if !decode(w, r, &sel) {
		return
	}
	if sel.Name == "" {
		sel.Name = "-"
	}
	if err := sel.Validate(); err != nil {
		writeErr(w, err)
		return
	}
	m := selection.New(sel)
	recs := o.Source.Recent(queryInt(r, "n", 20, 1, 200), m)
	out := Match{Count: o.Source.Count(m), Recent: make([]RecentRecord, 0, len(recs))}
	for _, rec := range recs {
		out.Recent = append(out.Recent, RecentRecord{Record: rec, Summary: rec.Summary(), Targets: []string{}})
	}
	writeJSON(w, 200, out)
}

// TargetView is a target with its status.
type TargetView struct {
	config.Target
	Status *pipeline.Status `json:"status,omitempty"`
}

func (o *Options) withStatus(t config.Target) TargetView {
	v := TargetView{Target: t}
	for _, s := range o.Pipeline.Status() {
		if s.ID == t.ID {
			s := s
			v.Status = &s
		}
	}
	return v
}

func (o *Options) targets(w http.ResponseWriter, r *http.Request) {
	ts := o.Store.Targets()
	out := make([]TargetView, 0, len(ts))
	for _, t := range ts {
		out = append(out, o.withStatus(t))
	}
	writeJSON(w, 200, out)
}

func (o *Options) createTarget(w http.ResponseWriter, r *http.Request) {
	var t config.Target
	if !decode(w, r, &t) {
		return
	}
	t.ID = ""
	t, err := o.Store.UpsertTarget(t)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 201, o.withStatus(t))
}

func (o *Options) updateTarget(w http.ResponseWriter, r *http.Request) {
	var t config.Target
	if !decode(w, r, &t) {
		return
	}
	t.ID = r.PathValue("id")
	t, err := o.Store.UpsertTarget(t)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, o.withStatus(t))
}

func (o *Options) deleteTarget(w http.ResponseWriter, r *http.Request) {
	if err := o.Store.DeleteTarget(r.PathValue("id")); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(204)
}

func (o *Options) enableTarget(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if !decode(w, r, &body) {
		return
	}
	t, err := o.Store.SetEnabled(r.PathValue("id"), body.Enabled)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, o.withStatus(t))
}

func (o *Options) clearTarget(w http.ResponseWriter, r *http.Request) {
	if !o.Pipeline.Clear(r.PathValue("id")) {
		addonkit.WriteError(w, 404, "not-found", "target")
		return
	}
	w.WriteHeader(204)
}

// TestResult is what a target's test answered.
type TestResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
	// Record is the record that was sent, as the UI shows it.
	Record string `json:"record"`
}

// testTarget is POST /api/v1/targets/test: the body is the (possibly unsaved) target; its stored
// secrets are used where the body sets none.
func (o *Options) testTarget(w http.ResponseWriter, r *http.Request) {
	t, ok := o.decodeTarget(w, r)
	if !ok {
		return
	}
	tgt, err := target.New(t)
	if err != nil {
		writeJSON(w, 200, TestResult{OK: false, Message: err.Error()})
		return
	}
	sample := target.Sample()
	if recent := o.Source.Recent(1, nil); len(recent) > 0 && r.URL.Query().Get("sample") == "recent" {
		sample = recent[0]
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	msg, err := tgt.Test(ctx, sample)
	if err != nil {
		writeJSON(w, 200, TestResult{OK: false, Message: err.Error(), Record: sample.Summary()})
		return
	}
	writeJSON(w, 200, TestResult{OK: true, Message: msg, Record: sample.Summary()})
}

// decodeTarget reads a target from the body, validates it and fills its secrets: the ones the
// body sets, else the stored ones of the target with its id.
func (o *Options) decodeTarget(w http.ResponseWriter, r *http.Request) (config.Target, bool) {
	var t config.Target
	if !decode(w, r, &t) {
		return t, false
	}
	if t.Name == "" {
		t.Name = "-"
	}
	if err := t.Validate(); err != nil {
		writeErr(w, err)
		return t, false
	}
	set := t.SetSecrets
	t = o.Store.WithSecrets(t)
	for k, v := range set {
		if v != "" {
			t.Secrets[k] = v
		}
	}
	return t, true
}

// Preview is POST /api/v1/targets/preview: the recent records as the target serialises them.
type Preview struct {
	Records int      `json:"records"`
	Text    string   `json:"text"`
	Errors  []string `json:"errors"`
}

func (o *Options) previewTarget(w http.ResponseWriter, r *http.Request) {
	t, ok := o.decodeTarget(w, r)
	if !ok {
		return
	}
	tgt, err := target.New(t)
	if err != nil {
		writeJSON(w, 200, Preview{Errors: []string{err.Error()}})
		return
	}
	var m *selection.Matcher
	if t.Selection != "" {
		if sel, ok := o.Store.Selection(t.Selection); ok {
			m = selection.New(sel)
		}
	}
	recs := o.Source.Recent(queryInt(r, "n", 10, 1, 100), m)
	if len(recs) == 0 {
		recs = []*record.Record{target.Sample()}
	}
	// oldest first, as they would go out
	for i, j := 0, len(recs)-1; i < j; i, j = i+1, j-1 {
		recs[i], recs[j] = recs[j], recs[i]
	}
	text, errs := tgt.Preview(recs)
	writeJSON(w, 200, Preview{Records: len(recs), Text: text, Errors: nz(errs)})
}

func (o *Options) status(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, o.statusMessage())
}

// stream is GET /api/v1/stream: the status as SSE, on every change and at least every 5 s.
func (o *Options) stream(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		addonkit.WriteError(w, 500, "no-stream", "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	send := func() {
		b, _ := json.Marshal(o.statusMessage())
		fmt.Fprintf(w, "event: status\ndata: %s\n\n", b)
		fl.Flush()
	}
	send()
	var notify <-chan struct{}
	if o.Hub != nil {
		ch, unsub := o.Hub.Subscribe()
		defer unsub()
		notify = ch
	}
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	debounce := time.NewTimer(time.Hour)
	debounce.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			send()
		case <-notify:
			debounce.Reset(300 * time.Millisecond)
		case <-debounce.C:
			send()
		}
	}
}

// RenderRequest is POST /api/v1/templates/render.
type RenderRequest struct {
	Template string `json:"template"`
	// Selection narrows the records the template is rendered against; "" takes any.
	Selection string `json:"selection"`
	N         int    `json:"n"`
}

// Rendered is one record's rendering.
type Rendered struct {
	Record string `json:"record"`
	Output string `json:"output"`
	Error  string `json:"error,omitempty"`
}

func (o *Options) renderTemplate(w http.ResponseWriter, r *http.Request) {
	var req RenderRequest
	if !decode(w, r, &req) {
		return
	}
	t, err := tmpl.Compile(req.Template)
	if err != nil {
		writeJSON(w, 200, map[string]any{"error": err.Error(), "results": []Rendered{}})
		return
	}
	var m *selection.Matcher
	if req.Selection != "" {
		if sel, ok := o.Store.Selection(req.Selection); ok {
			m = selection.New(sel)
		}
	}
	n := req.N
	if n <= 0 || n > 50 {
		n = 5
	}
	recs := o.Source.Recent(n, m)
	if len(recs) == 0 {
		recs = []*record.Record{target.Sample()}
	}
	out := make([]Rendered, 0, len(recs))
	for _, rec := range recs {
		s, err := t.Render(rec.Context())
		res := Rendered{Record: rec.Summary(), Output: s}
		if err != nil {
			res.Error = err.Error()
		}
		out = append(out, res)
	}
	writeJSON(w, 200, map[string]any{"results": out})
}

func (o *Options) templateHelp(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"variables": record.Variables, "filters": tmpl.Filters, "placeholders": target.Placeholders})
}

func (o *Options) presets(w http.ResponseWriter, r *http.Request) {
	defaults := map[string]config.Target{}
	for _, typ := range config.Types {
		defaults[typ] = config.DefaultTarget(typ)
	}
	writeJSON(w, 200, map[string]any{"influx": config.InfluxPresets, "influx_order": config.PresetNames, "webhook": config.WebhookPresets, "elastic": config.ElasticPresets, "elastic_order": config.ElasticPresetNames, "prometheus": config.PrometheusPresets, "prometheus_order": config.PrometheusPresetNames, "sql": config.SQLPresets, "sql_order": config.SQLPresetNames, "syslog": config.SyslogPresets, "syslog_order": config.SyslogPresetNames, "gelf": config.GELFPresets, "gelf_order": config.GELFPresetNames, "loki": config.LokiPresets, "loki_order": config.LokiPresetNames, "defaults": defaults})
}

func (o *Options) settings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, o.Store.Settings())
}

func (o *Options) putSettings(w http.ResponseWriter, r *http.Request) {
	var s config.Settings
	if !decode(w, r, &s) {
		return
	}
	if err := o.Store.SetSettings(s); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, o.Store.Settings())
}
