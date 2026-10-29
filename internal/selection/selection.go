// Package selection is what a target forwards: a reusable set of criteria over the records
// (the UI's "Auswahl"/"Selection"). A selection combines ticked devices and channels (Objects)
// with rules by interface, device type, channel type, datapoint key (globs), room and function,
// plus explicit exclusions (ExcludeKeys, ExcludeObjects). Within one criterion the entries are
// ORed, across the criteria they are ANDed, and an empty criterion restricts nothing - so the
// empty selection matches everything of the kinds it names. Rooms and functions are matched on
// the record's labels, which the source keeps live, so a room rule follows a later move.
//
// The package is self-contained: it matches a Subject, the few fields of a record a selection
// looks at, so it can move into the kit or another addon unchanged.
package selection

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

// The record kinds a selection knows.
const (
	KindEvent  = "event"
	KindMetric = "metric"
	KindLog    = "log"
)

// Subject is what a selection matches: the fields of a record that the criteria look at.
type Subject struct {
	Kind string
	// an event
	Interface   string
	Device      string
	Address     string
	Key         string
	DeviceType  string
	ChannelType string
	Rooms       []string
	Functions   []string
	Source      string
	// a metric
	MetricGroup string
	// a log line
	LogStream   string
	LogUnit     string
	LogPriority string
}

// Selection is one selection.
type Selection struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Kinds restricts to record kinds (event, metric, log); empty: every kind.
	Kinds []string `json:"kinds"`
	// Interfaces restricts events to interfaces (HmIP-RF, …).
	Interfaces []string `json:"interfaces"`
	// DeviceTypes restricts events to device types (HmIP-eTRV-2, …).
	DeviceTypes []string `json:"device_types"`
	// ChannelTypes restricts events to channel types (CLIMATECONTROL_REGULATOR, SWITCH_VIRTUAL_RECEIVER, …).
	ChannelTypes []string `json:"channel_types"`
	// Objects restricts events to devices and channels, as "<interface>/<address>"; a device
	// includes its channels.
	Objects []string `json:"objects"`
	// ExcludeObjects drops events of these devices and channels, the same form; applied last.
	ExcludeObjects []string `json:"exclude_objects"`
	// Keys restricts events to datapoint keys; globs (PRESS_*, *_TEMPERATURE).
	Keys []string `json:"keys"`
	// ExcludeKeys drops events of these keys; globs. Applied after Keys.
	ExcludeKeys []string `json:"exclude_keys"`
	// Rooms and Functions restrict events to channels (or their devices) in these rooms/functions.
	Rooms     []string `json:"rooms"`
	Functions []string `json:"functions"`
	// Sources restricts events to their source (event, snapshot, sweep); empty: every source.
	Sources []string `json:"sources"`
	// MetricGroups restricts metrics to groups (radio, service_messages, cpu, …).
	MetricGroups []string `json:"metric_groups"`
	// LogStreams restricts log records to streams (journal, audit); LogUnits to units; LogPriority
	// is the least important priority forwarded (emerg … debug), "" for all.
	LogStreams  []string `json:"log_streams"`
	LogUnits    []string `json:"log_units"`
	LogPriority string   `json:"log_priority"`
}

// Validate checks the selection's shape.
func (s *Selection) Validate() error {
	if strings.TrimSpace(s.Name) == "" {
		return fmt.Errorf("the selection needs a name")
	}
	for _, k := range s.Kinds {
		if k != KindEvent && k != KindMetric && k != KindLog {
			return fmt.Errorf("unknown kind %q", k)
		}
	}
	for _, g := range append(append([]string{}, s.Keys...), s.ExcludeKeys...) {
		if _, err := path.Match(g, ""); err != nil {
			return fmt.Errorf("bad key pattern %q", g)
		}
	}
	if s.LogPriority != "" && priorityRank(s.LogPriority) < 0 {
		return fmt.Errorf("unknown priority %q", s.LogPriority)
	}
	s.normalise()
	return nil
}

func (s *Selection) normalise() {
	for _, l := range []*[]string{&s.Kinds, &s.Interfaces, &s.DeviceTypes, &s.ChannelTypes, &s.Objects, &s.ExcludeObjects, &s.Keys, &s.ExcludeKeys, &s.Rooms, &s.Functions, &s.Sources, &s.MetricGroups, &s.LogStreams, &s.LogUnits} {
		out := (*l)[:0]
		seen := map[string]bool{}
		for _, v := range *l {
			v = strings.TrimSpace(v)
			if v == "" || seen[v] {
				continue
			}
			seen[v] = true
			out = append(out, v)
		}
		if out == nil {
			out = []string{}
		}
		*l = out
	}
}

// Matcher is a selection compiled for matching.
type Matcher struct {
	s        Selection
	kinds    map[string]bool
	ifaces   map[string]bool
	dtypes   map[string]bool
	ctypes   map[string]bool
	devices  map[string]bool // "<iface>/<device>"
	channels map[string]bool // "<iface>/<device>:<n>"
	exDev    map[string]bool
	exChan   map[string]bool
	rooms    map[string]bool
	funcs    map[string]bool
	sources  map[string]bool
	groups   map[string]bool
	streams  map[string]bool
	units    map[string]bool
	minPrio  int
}

// New compiles a selection.
func New(s Selection) *Matcher {
	m := &Matcher{s: s, kinds: set(s.Kinds), ifaces: set(s.Interfaces), dtypes: set(s.DeviceTypes), ctypes: set(s.ChannelTypes), rooms: set(s.Rooms), funcs: set(s.Functions), sources: set(s.Sources), groups: set(s.MetricGroups), streams: set(s.LogStreams), units: set(s.LogUnits), minPrio: -1}
	m.devices, m.channels = splitObjects(s.Objects)
	m.exDev, m.exChan = splitObjects(s.ExcludeObjects)
	if s.LogPriority != "" {
		m.minPrio = priorityRank(s.LogPriority)
	}
	return m
}

func splitObjects(l []string) (devices, channels map[string]bool) {
	devices, channels = map[string]bool{}, map[string]bool{}
	for _, o := range l {
		if strings.Contains(o, ":") {
			channels[o] = true
		} else {
			devices[o] = true
		}
	}
	return devices, channels
}

func set(l []string) map[string]bool {
	if len(l) == 0 {
		return nil
	}
	m := make(map[string]bool, len(l))
	for _, v := range l {
		m[v] = true
	}
	return m
}

// Selection answers the selection the matcher was compiled from.
func (m *Matcher) Selection() Selection { return m.s }

// Match tells whether a subject is in the selection.
func (m *Matcher) Match(r Subject) bool {
	if m.kinds != nil && !m.kinds[r.Kind] {
		return false
	}
	switch r.Kind {
	case KindEvent:
		return m.matchEvent(r)
	case KindMetric:
		return m.groups == nil || m.groups[r.MetricGroup]
	case KindLog:
		if m.streams != nil && !m.streams[r.LogStream] {
			return false
		}
		if m.units != nil && !m.units[r.LogUnit] {
			return false
		}
		if m.minPrio >= 0 {
			if p := priorityRank(r.LogPriority); p < 0 || p > m.minPrio {
				return false
			}
		}
		return true
	}
	return false
}

func (m *Matcher) matchEvent(r Subject) bool {
	if m.ifaces != nil && !m.ifaces[r.Interface] {
		return false
	}
	if m.dtypes != nil && !m.dtypes[r.DeviceType] {
		return false
	}
	if m.ctypes != nil && !m.ctypes[r.ChannelType] {
		return false
	}
	if m.sources != nil && !m.sources[r.Source] {
		return false
	}
	if len(m.devices)+len(m.channels) > 0 {
		if !m.devices[r.Interface+"/"+r.Device] && !m.channels[r.Interface+"/"+r.Address] {
			return false
		}
	}
	if len(m.s.Keys) > 0 && !matchAny(m.s.Keys, r.Key) {
		return false
	}
	if len(m.s.ExcludeKeys) > 0 && matchAny(m.s.ExcludeKeys, r.Key) {
		return false
	}
	if m.rooms != nil && !anyIn(m.rooms, r.Rooms) {
		return false
	}
	if m.funcs != nil && !anyIn(m.funcs, r.Functions) {
		return false
	}
	if m.exDev[r.Interface+"/"+r.Device] || m.exChan[r.Interface+"/"+r.Address] {
		return false
	}
	return true
}

func matchAny(globs []string, s string) bool {
	for _, g := range globs {
		if ok, _ := path.Match(g, s); ok {
			return true
		}
	}
	return false
}

func anyIn(set map[string]bool, l []string) bool {
	for _, v := range l {
		if set[v] {
			return true
		}
	}
	return false
}

// Priorities are syslog's, most important first.
var Priorities = []string{"emerg", "alert", "crit", "err", "warning", "notice", "info", "debug"}

func priorityRank(p string) int {
	p = strings.ToLower(p)
	for i, name := range Priorities {
		if name == p {
			return i
		}
	}
	switch p {
	case "0", "1", "2", "3", "4", "5", "6", "7":
		return int(p[0] - '0')
	case "warn":
		return 4
	case "error":
		return 3
	}
	return -1
}

// SortedKeys answers a set's keys sorted, for deterministic output.
func SortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
