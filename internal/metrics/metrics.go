// Package metrics polls the system's statistics and the host's resources and emits them as
// metric records, one per value, grouped: the Homematic statistics come from occulited's system
// API (the service messages, the radio modules' duty cycle and carrier sense, the interfaces'
// telegram rates, the callback subscribers' reachability and stalls, the services' state and
// memory), the host's figures from /proc and /sys (CPU, load, memory and swap, disk IO, storage,
// network, temperature, uptime). A group is polled only while a target wants it, so a system
// without a metric target reads nothing.
package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hobbyquaker/lite-addon-forwarder/internal/record"
)

// Options configure the poller.
type Options struct {
	// SystemURL is the system's URL (http://127.0.0.1); the system API is below /api/system/v1.
	SystemURL string
	// HTTP carries the addon's token (addonkit.AuthTransport).
	HTTP *http.Client
	// ProcRoot and SysRoot are /proc and /sys; a test points them at fixtures.
	ProcRoot, SysRoot string
	// Interval is the poll interval.
	Interval func() time.Duration
	// Wants tells whether any target takes the group; nil polls every group.
	Wants func(group string) bool
	// Emit receives every metric record.
	Emit func(*record.Record)
	// Events answers the source's event count per interface since start, for rpc_events.
	Events func() map[string]int64
	Log    func(format string, args ...any)
	Now    func() time.Time
}

// Poller is the running poller.
type Poller struct {
	o    Options
	host string

	mu    sync.Mutex
	stats Stats
	// the previous samples of the groups that report rates
	prevAt    time.Time
	prevCPU   *cpuSample
	prevDisk  map[string]diskSample
	prevNet   map[string]netSample
	prevEvent map[string]int64
	prevPIDs  map[string]int
	restarts  map[string]int64
	kick      chan struct{}
}

// Stats is the poller's state for the UI.
type Stats struct {
	// LastPoll is when the last poll ended, Records how many it emitted, DurationMS how long
	// it took, Groups which groups it polled.
	LastPoll   time.Time         `json:"last_poll"`
	Records    int               `json:"records"`
	DurationMS int64             `json:"duration_ms"`
	Groups     []string          `json:"groups"`
	Polls      int64             `json:"polls"`
	Errors     map[string]string `json:"errors"`
	IntervalS  int               `json:"interval_s"`
}

// New makes a poller.
func New(o Options) *Poller {
	if o.Log == nil {
		o.Log = func(string, ...any) {}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.ProcRoot == "" {
		o.ProcRoot = "/proc"
	}
	if o.SysRoot == "" {
		o.SysRoot = "/sys"
	}
	if o.Interval == nil {
		o.Interval = func() time.Duration { return time.Minute }
	}
	if o.HTTP == nil {
		o.HTTP = &http.Client{Timeout: 20 * time.Second}
	}
	o.SystemURL = strings.TrimRight(o.SystemURL, "/")
	host, _ := os.Hostname()
	return &Poller{o: o, host: host, stats: Stats{Errors: map[string]string{}, Groups: []string{}}, prevDisk: map[string]diskSample{}, prevNet: map[string]netSample{}, prevEvent: map[string]int64{}, prevPIDs: map[string]int{}, restarts: map[string]int64{}, kick: make(chan struct{}, 1)}
}

// Run polls until ctx ends: once soon after the start, then every interval.
func (p *Poller) Run(ctx context.Context) {
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-p.kick:
		}
		p.Poll(ctx)
		iv := p.o.Interval()
		if iv < 10*time.Second {
			iv = 10 * time.Second
		}
		timer.Reset(iv)
	}
}

// Kick asks for a poll now (a target was added).
func (p *Poller) Kick() {
	select {
	case p.kick <- struct{}{}:
	default:
	}
}

// Stats answers the poller's state.
func (p *Poller) Stats() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.stats
	s.Groups = append([]string{}, p.stats.Groups...)
	s.Errors = map[string]string{}
	for k, v := range p.stats.Errors {
		s.Errors[k] = v
	}
	s.IntervalS = int(p.o.Interval() / time.Second)
	return s
}

// Poll polls every wanted group once and emits the records.
func (p *Poller) Poll(ctx context.Context) {
	start := p.o.Now()
	ts := start
	var out []*record.Record
	errs := map[string]string{}
	var groups []string
	emit := func(g, name string, v float64, unit string, labels map[string]string) {
		if labels == nil {
			labels = map[string]string{}
		}
		labels["host"] = p.host
		r := record.Metric(ts, g, name, v, labels)
		r.Unit = unit
		out = append(out, r)
	}
	for _, g := range Groups {
		if p.o.Wants != nil && !p.o.Wants(g.Name) {
			continue
		}
		groups = append(groups, g.Name)
		if err := p.poll(ctx, g.Name, emit); err != nil {
			errs[g.Name] = err.Error()
		}
	}
	for _, r := range out {
		if p.o.Emit != nil {
			p.o.Emit(r)
		}
	}
	p.mu.Lock()
	for g, e := range errs {
		if p.stats.Errors[g] != e {
			p.o.Log("metrics %s: %s", g, e)
		}
	}
	p.prevAt = start
	p.stats.LastPoll = p.o.Now()
	p.stats.Records = len(out)
	p.stats.DurationMS = p.stats.LastPoll.Sub(start).Milliseconds()
	p.stats.Groups = groups
	if p.stats.Groups == nil {
		p.stats.Groups = []string{}
	}
	p.stats.Polls++
	p.stats.Errors = errs
	p.mu.Unlock()
}

type emitFunc func(group, name string, value float64, unit string, labels map[string]string)

func (p *Poller) poll(ctx context.Context, group string, emit emitFunc) error {
	switch group {
	case GroupServiceMessages:
		return p.serviceMessages(ctx, emit)
	case GroupRPCEvents:
		return p.rpcEvents(ctx, emit)
	case GroupRadio:
		return p.radio(ctx, emit)
	case GroupReachability:
		return p.reachability(ctx, emit)
	case GroupStalls:
		return p.stalls(ctx, emit)
	case GroupServices:
		return p.services(ctx, emit)
	case GroupCPU:
		return p.cpu(emit)
	case GroupMemory:
		return p.memory(emit)
	case GroupDiskIO:
		return p.diskIO(emit)
	case GroupStorage:
		return p.storage(emit)
	case GroupNetwork:
		return p.network(emit)
	case GroupTemperature:
		return p.temperature(emit)
	case GroupSystem:
		return p.system(emit)
	}
	return fmt.Errorf("unknown group %s", group)
}

// get reads one route of the system API into out.
func (p *Poller) get(ctx context.Context, path string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.o.SystemURL+"/api/system/v1"+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	res, err := p.o.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if err != nil {
		return err
	}
	if res.StatusCode/100 != 2 {
		var e struct {
			Error, Scope, Message string
		}
		_ = json.Unmarshal(b, &e)
		if e.Scope != "" {
			return fmt.Errorf("%s: HTTP %d: the addon's token lacks the scope %s", path, res.StatusCode, e.Scope)
		}
		if e.Error != "" {
			return fmt.Errorf("%s: HTTP %d %s", path, res.StatusCode, e.Error)
		}
		return fmt.Errorf("%s: HTTP %d", path, res.StatusCode)
	}
	return json.Unmarshal(b, out)
}

// --- the Homematic statistics, from occulited ---

func (p *Poller) serviceMessages(ctx context.Context, emit emitFunc) error {
	var v struct {
		Count    int `json:"count"`
		Messages []struct {
			Interface string `json:"interface"`
			Key       string `json:"key"`
		} `json:"messages"`
	}
	if err := p.get(ctx, "/service-messages", &v); err != nil {
		return err
	}
	byType, byIface := map[string]int{}, map[string]int{}
	for _, m := range v.Messages {
		byType[m.Key]++
		byIface[m.Interface]++
	}
	emit(GroupServiceMessages, "total", float64(len(v.Messages)), "", nil)
	for _, k := range sortedKeys(byType) {
		emit(GroupServiceMessages, "count", float64(byType[k]), "", map[string]string{"type": k})
	}
	for _, k := range sortedKeys(byIface) {
		emit(GroupServiceMessages, "by_interface", float64(byIface[k]), "", map[string]string{"interface": k})
	}
	return nil
}

type radioHealth struct {
	Interfaces []struct {
		Interface    string `json:"interface"`
		Address      string `json:"address"`
		Type         string `json:"type"`
		Connected    bool   `json:"connected"`
		DutyCycle    int    `json:"duty_cycle"`
		CarrierSense *int   `json:"carrier_sense"`
	} `json:"interfaces"`
	Errors map[string]string `json:"errors"`
	Rates  map[string][]struct {
		T  time.Time `json:"t"`
		In float64   `json:"in"`
		Up bool      `json:"up"`
	} `json:"rates"`
	Busy bool `json:"busy"`
}

func (p *Poller) rpcEvents(ctx context.Context, emit emitFunc) error {
	// the forwarder's own count: every event the stream delivered, per interface
	if p.o.Events != nil {
		now := p.o.Events()
		p.mu.Lock()
		prev, prevAt := p.prevEvent, p.prevAt
		p.prevEvent = now
		p.mu.Unlock()
		for _, iface := range sortedKeys64(now) {
			emit(GroupRPCEvents, "received_total", float64(now[iface]), "", map[string]string{"interface": iface})
			if !prevAt.IsZero() {
				secs := p.o.Now().Sub(prevAt).Seconds()
				if old, ok := prev[iface]; ok && secs > 0 && now[iface] >= old {
					emit(GroupRPCEvents, "rate_per_minute", float64(now[iface]-old)/secs*60, "1/min", map[string]string{"interface": iface})
				}
			}
		}
	}
	var h radioHealth
	if err := p.get(ctx, "/radio/health", &h); err != nil {
		return err
	}
	for _, iface := range sortedRateKeys(h.Rates) {
		samples := h.Rates[iface]
		if len(samples) == 0 {
			continue
		}
		last := samples[len(samples)-1]
		emit(GroupRPCEvents, "interface_rate_per_minute", last.In, "1/min", map[string]string{"interface": iface})
		emit(GroupRPCEvents, "interface_up", b2f(last.Up), "", map[string]string{"interface": iface})
	}
	return nil
}

func (p *Poller) radio(ctx context.Context, emit emitFunc) error {
	var h radioHealth
	if err := p.get(ctx, "/radio/health", &h); err != nil {
		return err
	}
	for _, in := range h.Interfaces {
		l := map[string]string{"interface": in.Interface, "address": in.Address, "type": in.Type}
		emit(GroupRadio, "duty_cycle_percent", float64(in.DutyCycle), "%", copyLabels(l))
		if in.CarrierSense != nil {
			emit(GroupRadio, "carrier_sense_percent", float64(*in.CarrierSense), "%", copyLabels(l))
		}
		emit(GroupRadio, "connected", b2f(in.Connected), "", copyLabels(l))
	}
	emit(GroupRadio, "busy", b2f(h.Busy), "", nil)
	emit(GroupRadio, "errors", float64(len(h.Errors)), "", nil)
	return nil
}

func (p *Poller) reachability(ctx context.Context, emit emitFunc) error {
	var v struct {
		Subscribers []struct {
			Interface string `json:"interface"`
			ID        string `json:"id"`
			URL       string `json:"url"`
			Reachable *bool  `json:"reachable"`
			Reason    string `json:"reason"`
		} `json:"subscribers"`
	}
	if err := p.get(ctx, "/radio/subscribers/reachability", &v); err != nil {
		return err
	}
	unreachable := 0
	for _, s := range v.Subscribers {
		if s.Reachable == nil {
			continue
		}
		if !*s.Reachable {
			unreachable++
		}
		emit(GroupReachability, "reachable", b2f(*s.Reachable), "", map[string]string{"interface": s.Interface, "id": s.ID, "url": s.URL})
	}
	emit(GroupReachability, "unreachable_total", float64(unreachable), "", nil)
	return nil
}

func (p *Poller) stalls(ctx context.Context, emit emitFunc) error {
	var v struct {
		Stalls []struct {
			Interface string `json:"interface"`
			Kind      string `json:"kind"`
		} `json:"stalls"`
	}
	if err := p.get(ctx, "/radio/subscribers/stalls", &v); err != nil {
		return err
	}
	for _, s := range v.Stalls {
		emit(GroupStalls, "stalled", 1, "", map[string]string{"interface": s.Interface, "kind": s.Kind})
	}
	emit(GroupStalls, "total", float64(len(v.Stalls)), "", nil)
	return nil
}

func (p *Poller) services(ctx context.Context, emit emitFunc) error {
	var v struct {
		Services []struct {
			ID       string `json:"id"`
			Kind     string `json:"kind"`
			Category string `json:"category"`
			Running  bool   `json:"running"`
			Failed   bool   `json:"failed"`
			PID      int    `json:"pid"`
			Procs    []struct {
				PID int `json:"pid"`
			} `json:"procs"`
		} `json:"services"`
	}
	if err := p.get(ctx, "/services", &v); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	seen := map[string]bool{}
	for _, s := range v.Services {
		seen[s.ID] = true
		l := map[string]string{"unit": s.ID, "kind": s.Kind, "category": s.Category}
		emit(GroupServices, "running", b2f(s.Running), "", copyLabels(l))
		emit(GroupServices, "failed", b2f(s.Failed), "", copyLabels(l))
		// a restart is a new leader pid of a unit that had one: counted since the addon started
		if prev, ok := p.prevPIDs[s.ID]; ok && prev != 0 && s.PID != 0 && s.PID != prev {
			p.restarts[s.ID]++
		}
		if s.PID != 0 {
			p.prevPIDs[s.ID] = s.PID
		}
		emit(GroupServices, "restarts_total", float64(p.restarts[s.ID]), "", copyLabels(l))
		if s.Running {
			pids := []int{s.PID}
			for _, pr := range s.Procs {
				if pr.PID != s.PID {
					pids = append(pids, pr.PID)
				}
			}
			var rss int64
			n := 0
			for _, pid := range pids {
				if pid <= 0 {
					continue
				}
				if r, ok := p.rssOf(pid); ok {
					rss += r
					n++
				}
			}
			if n > 0 {
				emit(GroupServices, "rss_bytes", float64(rss), "bytes", copyLabels(l))
			}
		}
	}
	for id := range p.prevPIDs {
		if !seen[id] {
			delete(p.prevPIDs, id)
		}
	}
	return nil
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func copyLabels(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeys64(m map[string]int64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedRateKeys[T any](m map[string][]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
