// Package source turns openccu-lite's APIs into records: the lite-rpc event stream (resumed
// with Last-Event-ID, re-seeded from GET /state on a resync), a snapshot of every datapoint at
// start, the device and paramset descriptions for types and units, and the metadata (names,
// rooms, functions) kept live. It also keeps the inventory the selection editor browses and a
// ring of recent records for the previews.
package source

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hobbyquaker/ccu-addon-kit/go/litesource"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/record"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/selection"
)

// Options configure the source.
type Options struct {
	Client *litesource.Client
	// Emit receives every record.
	Emit func(*record.Record)
	// Snapshot tells whether to emit the current values at start and after a resync.
	Snapshot func() bool
	// Lang is the language of the room and function names.
	Lang func() string
	// Recent is the size of the ring of recent records.
	Recent func() int
	Log    func(format string, args ...any)
}

// Source is the running source.
type Source struct {
	o Options

	mu      sync.RWMutex
	names   *litesource.Names
	chans   map[string]*chanInfo // "<iface>/<address>"
	devs    map[string]*devInfo  // "<iface>/<device>"
	ifaces  []litesource.Interface
	ring    []*record.Record
	ringPos int
	ringN   int
	stats   Stats
	// perIface counts the stream's events per interface, for the rpc_events metrics
	perIface map[string]int64
}

// Stats is the source's state for the UI.
type Stats struct {
	Connected  bool      `json:"connected"`
	Interfaces int       `json:"interfaces"`
	Devices    int       `json:"devices"`
	Channels   int       `json:"channels"`
	Datapoints int       `json:"datapoints"`
	Events     int64     `json:"events"`
	Snapshots  int64     `json:"snapshots"`
	Resyncs    int64     `json:"resyncs"`
	LastEvent  time.Time `json:"last_event"`
	LastError  string    `json:"last_error"`
	MetaRev    int64     `json:"meta_revision"`
	Inventory  time.Time `json:"inventory_at"`
}

type devInfo struct {
	iface, address, typ string
	channels            []string
}

type chanInfo struct {
	iface, address, typ string
	device, devType     string
	index               int
	params              map[string]litesource.ParamDesc
}

// New makes a source.
func New(o Options) *Source {
	if o.Log == nil {
		o.Log = func(string, ...any) {}
	}
	if o.Snapshot == nil {
		o.Snapshot = func() bool { return true }
	}
	if o.Lang == nil {
		o.Lang = func() string { return "de" }
	}
	if o.Recent == nil {
		o.Recent = func() int { return 500 }
	}
	return &Source{o: o, chans: map[string]*chanInfo{}, devs: map[string]*devInfo{}, perIface: map[string]int64{}}
}

// Run runs the source until ctx ends.
func (s *Source) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); s.runNames(ctx) }()
	go func() { defer wg.Done(); s.runEvents(ctx) }()
	wg.Wait()
}

// runNames reads the metadata and follows its changes, re-reading the snapshot after each.
func (s *Source) runNames(ctx context.Context) {
	for ctx.Err() == nil {
		names, err := s.o.Client.Names(ctx)
		if err != nil {
			s.o.Log("names: %v", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}
		s.mu.Lock()
		s.names = names
		s.stats.MetaRev = names.Revision
		s.mu.Unlock()
		// follow: any change re-reads the snapshot, debounced
		events := s.o.Client.NameEvents(ctx, names.Revision)
		var timer <-chan time.Time
		reread := false
	loop:
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-events:
				if !ok {
					break loop
				}
				if timer == nil {
					timer = time.After(time.Second)
				}
				reread = true
			case <-timer:
				if reread {
					break loop
				}
			}
		}
	}
}

// runEvents seeds from GET /state and follows the stream, re-seeding on a resync.
func (s *Source) runEvents(ctx context.Context) {
	for ctx.Err() == nil {
		s.refreshInventory(ctx, "")
		snap, err := s.o.Client.State(ctx, litesource.Filter{})
		if err != nil {
			s.setError(err.Error())
			s.o.Log("state: %v", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}
		s.mu.Lock()
		s.stats.Connected = true
		s.stats.LastError = ""
		s.mu.Unlock()
		if s.o.Snapshot() {
			s.emitSnapshot(snap)
		}
		events := s.o.Client.Events(ctx, litesource.Filter{}, snap.EventID)
		resync := false
		for msg := range events {
			switch msg.Type {
			case "event", "state":
				if msg.Event != nil {
					src := "event"
					if msg.Type == "state" {
						src = "sweep"
					}
					if r := s.build(msg.Event.Interface, msg.Event.Address, msg.Event.Key, msg.Event.Value, msg.Event.TS, src); r != nil {
						s.emit(r)
						s.mu.Lock()
						s.stats.Events++
						s.stats.LastEvent = r.TS
						s.perIface[r.Interface]++
						s.mu.Unlock()
					}
				}
			case "resync":
				resync = true
			case "newDevices", "deleteDevices", "readdedDevice", "replaceDevice", "updateDevice":
				if msg.Devices != nil {
					s.refreshInventory(ctx, msg.Devices.Interface)
				}
			case "interface":
				if msg.Interface != nil && (msg.Interface.State == "up" || msg.Interface.State == "added") {
					s.refreshInventory(ctx, msg.Interface.Interface)
				}
			}
			if resync {
				break
			}
		}
		if ctx.Err() != nil {
			return
		}
		s.mu.Lock()
		s.stats.Resyncs++
		s.mu.Unlock()
		s.o.Log("stream resync: reading the state again")
	}
}

func (s *Source) setError(msg string) {
	s.mu.Lock()
	s.stats.Connected = false
	s.stats.LastError = msg
	s.mu.Unlock()
}

func (s *Source) emitSnapshot(snap *litesource.Snapshot) {
	ts := time.Now()
	n := 0
	for _, e := range snap.Entries {
		if !e.Confirmed && e.Source == "restored" {
			continue // a value restored from a file after a restart is not a current reading
		}
		t := e.TS
		if t.IsZero() {
			t = ts
		}
		if r := s.build(e.Interface, e.Address, e.Key, e.Value, t, "snapshot"); r != nil {
			s.emit(r)
			n++
		}
	}
	s.mu.Lock()
	s.stats.Snapshots += int64(n)
	s.mu.Unlock()
	s.o.Log("snapshot: %d values", n)
}

func (s *Source) emit(r *record.Record) {
	s.remember(r)
	if s.o.Emit != nil {
		s.o.Emit(r)
	}
}

// Emit adds a record that did not come from the stream (a metric, a log line): it goes through
// the ring and out.
func (s *Source) Emit(r *record.Record) { s.emit(r) }

func (s *Source) remember(r *record.Record) {
	s.mu.Lock()
	defer s.mu.Unlock()
	size := s.o.Recent()
	if size < 10 {
		size = 10
	}
	if len(s.ring) != size {
		// resize: keep the newest
		old := s.recentLocked(size)
		s.ring = make([]*record.Record, size)
		s.ringPos, s.ringN = 0, 0
		for _, o := range old {
			s.ring[s.ringPos] = o
			s.ringPos = (s.ringPos + 1) % size
			if s.ringN < size {
				s.ringN++
			}
		}
	}
	s.ring[s.ringPos] = r
	s.ringPos = (s.ringPos + 1) % size
	if s.ringN < size {
		s.ringN++
	}
}

// recentLocked answers the newest n records, oldest first.
func (s *Source) recentLocked(n int) []*record.Record {
	if n > s.ringN {
		n = s.ringN
	}
	out := make([]*record.Record, 0, n)
	size := len(s.ring)
	for i := s.ringN - n; i < s.ringN; i++ {
		idx := (s.ringPos - s.ringN + i + size*2) % size
		out = append(out, s.ring[idx])
	}
	return out
}

// Recent answers the newest records that match the selection (nil: every record), newest
// first, at most n.
func (s *Source) Recent(n int, m *selection.Matcher) []*record.Record {
	s.mu.RLock()
	all := s.recentLocked(s.ringN)
	s.mu.RUnlock()
	out := make([]*record.Record, 0, n)
	for i := len(all) - 1; i >= 0 && len(out) < n; i-- {
		if m == nil || m.Match(all[i].Subject()) {
			out = append(out, all[i])
		}
	}
	return out
}

// build makes the record of a datapoint report, with labels and types from the inventory.
func (s *Source) build(iface, address, key string, value []byte, ts time.Time, src string) *record.Record {
	if ts.IsZero() {
		ts = time.Now()
	}
	s.mu.RLock()
	ci := s.chans[iface+"/"+address]
	names := s.names
	lang := s.o.Lang()
	s.mu.RUnlock()
	var typ, unit string
	var vl []string
	if ci != nil {
		if p, ok := ci.params[key]; ok {
			typ, unit, vl = p.Type, p.Unit, p.ValueList
		}
	}
	r := record.Event(ts, iface, address, key, value, typ, vl)
	r.Unit = unit
	r.Source = src
	if ci != nil {
		r.DeviceType, r.ChannelType = ci.devType, ci.typ
	}
	s.label(r, names, lang)
	return r
}

// label fills the names, rooms and functions: the channel's, else the device's.
func (s *Source) label(r *record.Record, names *litesource.Names, lang string) {
	if names == nil {
		return
	}
	r.DeviceName = names.Name(r.Interface, r.Device)
	r.ChannelName = names.Name(r.Interface, r.Address)
	if r.ChannelName == "" {
		r.ChannelName = r.DeviceName
	}
	r.Rooms = names.RoomNames(r.Interface, r.Address, lang)
	if len(r.Rooms) == 0 && r.Channel >= 0 {
		r.Rooms = names.RoomNames(r.Interface, r.Device, lang)
	}
	r.Functions = names.FunctionNames(r.Interface, r.Address, lang)
	if len(r.Functions) == 0 && r.Channel >= 0 {
		r.Functions = names.FunctionNames(r.Interface, r.Device, lang)
	}
	if r.Rooms == nil {
		r.Rooms = []string{}
	}
	if r.Functions == nil {
		r.Functions = []string{}
	}
}

// refreshInventory reads the devices of one interface ("" for all) and their VALUES
// descriptions.
func (s *Source) refreshInventory(ctx context.Context, only string) {
	ifaces, err := s.o.Client.Interfaces(ctx)
	if err != nil {
		s.o.Log("interfaces: %v", err)
		return
	}
	for _, in := range ifaces {
		if only != "" && in.Name != only {
			continue
		}
		if !in.Running {
			continue
		}
		devices, err := s.o.Client.ListDevices(ctx, in.Name)
		if err != nil {
			s.o.Log("listDevices %s: %v", in.Name, err)
			continue
		}
		devs := map[string]*devInfo{}
		chans := map[string]*chanInfo{}
		var addresses []string
		for _, d := range devices {
			if d.Parent == "" {
				devs[in.Name+"/"+d.Address] = &devInfo{iface: in.Name, address: d.Address, typ: d.Type, channels: d.Children}
			}
		}
		for _, d := range devices {
			if d.Parent == "" {
				continue
			}
			dev, idx := record.SplitAddress(d.Address)
			ci := &chanInfo{iface: in.Name, address: d.Address, typ: d.Type, device: dev, index: idx, params: map[string]litesource.ParamDesc{}}
			if p := devs[in.Name+"/"+dev]; p != nil {
				ci.devType = p.typ
			}
			chans[in.Name+"/"+d.Address] = ci
			addresses = append(addresses, d.Address)
		}
		if len(addresses) > 0 {
			keys, sets, _, err := s.o.Client.ParamsetsFor(ctx, in.Name, addresses, "VALUES")
			if err != nil {
				s.o.Log("paramsets %s: %v", in.Name, err)
			}
			for addr, key := range keys {
				if ci := chans[in.Name+"/"+addr]; ci != nil {
					if ps, ok := sets[key]; ok {
						for name, desc := range ps {
							if desc.Operations&5 != 0 { // read or event
								ci.params[name] = desc
							}
						}
					}
				}
			}
		}
		s.mu.Lock()
		for k, v := range s.devs {
			if v.iface == in.Name {
				delete(s.devs, k)
			}
		}
		for k, v := range s.chans {
			if v.iface == in.Name {
				delete(s.chans, k)
			}
		}
		for k, v := range devs {
			s.devs[k] = v
		}
		for k, v := range chans {
			s.chans[k] = v
		}
		s.mu.Unlock()
	}
	s.mu.Lock()
	s.ifaces = ifaces
	s.stats.Interfaces = len(ifaces)
	s.stats.Devices = len(s.devs)
	s.stats.Channels = len(s.chans)
	n := 0
	for _, c := range s.chans {
		n += len(c.params)
	}
	s.stats.Datapoints = n
	s.stats.Inventory = time.Now()
	s.mu.Unlock()
}

// EventsByInterface answers the stream's event count per interface since the start.
func (s *Source) EventsByInterface() map[string]int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]int64, len(s.perIface))
	for k, v := range s.perIface {
		out[k] = v
	}
	return out
}

// Stats answers the source's state.
func (s *Source) Stats() Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.stats
}

// Inventory is what the selection editor browses.
type Inventory struct {
	Interfaces   []InterfaceInfo `json:"interfaces"`
	Devices      []DeviceInfo    `json:"devices"`
	Rooms        []string        `json:"rooms"`
	Functions    []string        `json:"functions"`
	DeviceTypes  []Counted       `json:"device_types"`
	ChannelTypes []Counted       `json:"channel_types"`
	Keys         []Counted       `json:"keys"`
	Datapoints   int             `json:"datapoints"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

// InterfaceInfo is one interface.
type InterfaceInfo struct {
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	Running  bool   `json:"running"`
	Devices  int    `json:"devices"`
}

// DeviceInfo is one device with its channels.
type DeviceInfo struct {
	Interface string        `json:"interface"`
	Address   string        `json:"address"`
	Type      string        `json:"type"`
	Name      string        `json:"name"`
	Rooms     []string      `json:"rooms"`
	Functions []string      `json:"functions"`
	Channels  []ChannelInfo `json:"channels"`
}

// ChannelInfo is one channel.
type ChannelInfo struct {
	Address   string   `json:"address"`
	Index     int      `json:"index"`
	Type      string   `json:"type"`
	Name      string   `json:"name"`
	Rooms     []string `json:"rooms"`
	Functions []string `json:"functions"`
	Keys      []string `json:"keys"`
}

// Counted is a name with how many datapoints carry it.
type Counted struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// Inventory builds the inventory.
func (s *Source) Inventory() Inventory {
	s.mu.RLock()
	defer s.mu.RUnlock()
	lang := s.o.Lang()
	inv := Inventory{Rooms: []string{}, Functions: []string{}, DeviceTypes: []Counted{}, ChannelTypes: []Counted{}, Keys: []Counted{}, UpdatedAt: s.stats.Inventory}
	perIface := map[string]int{}
	rooms, funcs := map[string]bool{}, map[string]bool{}
	dtypes, ctypes, keys := map[string]int{}, map[string]int{}, map[string]int{}
	for _, d := range s.devs {
		perIface[d.iface]++
		di := DeviceInfo{Interface: d.iface, Address: d.address, Type: d.typ, Rooms: []string{}, Functions: []string{}, Channels: []ChannelInfo{}}
		if s.names != nil {
			di.Name = s.names.Name(d.iface, d.address)
			di.Rooms = nz(s.names.RoomNames(d.iface, d.address, lang))
			di.Functions = nz(s.names.FunctionNames(d.iface, d.address, lang))
		}
		for _, addr := range d.channels {
			c := s.chans[d.iface+"/"+addr]
			if c == nil {
				continue
			}
			ci := ChannelInfo{Address: c.address, Index: c.index, Type: c.typ, Rooms: []string{}, Functions: []string{}, Keys: make([]string, 0, len(c.params))}
			if s.names != nil {
				ci.Name = s.names.Name(d.iface, c.address)
				ci.Rooms = nz(s.names.RoomNames(d.iface, c.address, lang))
				ci.Functions = nz(s.names.FunctionNames(d.iface, c.address, lang))
			}
			for k := range c.params {
				ci.Keys = append(ci.Keys, k)
				keys[k]++
				dtypes[d.typ]++
				ctypes[c.typ]++
				inv.Datapoints++
			}
			sort.Strings(ci.Keys)
			for _, r := range ci.Rooms {
				rooms[r] = true
			}
			for _, f := range ci.Functions {
				funcs[f] = true
			}
			di.Channels = append(di.Channels, ci)
		}
		sort.Slice(di.Channels, func(i, j int) bool { return di.Channels[i].Index < di.Channels[j].Index })
		for _, r := range di.Rooms {
			rooms[r] = true
		}
		for _, f := range di.Functions {
			funcs[f] = true
		}
		inv.Devices = append(inv.Devices, di)
	}
	sort.Slice(inv.Devices, func(i, j int) bool {
		a, b := inv.Devices[i], inv.Devices[j]
		if a.Interface != b.Interface {
			return a.Interface < b.Interface
		}
		if an, bn := strings.ToLower(a.Name), strings.ToLower(b.Name); an != bn {
			return an < bn
		}
		return a.Address < b.Address
	})
	for _, in := range s.ifaces {
		inv.Interfaces = append(inv.Interfaces, InterfaceInfo{Name: in.Name, Protocol: in.Protocol, Running: in.Running, Devices: perIface[in.Name]})
	}
	if inv.Interfaces == nil {
		inv.Interfaces = []InterfaceInfo{}
	}
	if inv.Devices == nil {
		inv.Devices = []DeviceInfo{}
	}
	inv.Rooms = selection.SortedKeys(rooms)
	inv.Functions = selection.SortedKeys(funcs)
	inv.DeviceTypes = counted(dtypes)
	inv.ChannelTypes = counted(ctypes)
	inv.Keys = counted(keys)
	return inv
}

func nz(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

func counted(m map[string]int) []Counted {
	out := make([]Counted, 0, len(m))
	for k, v := range m {
		out = append(out, Counted{k, v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Count is what a selection matches of the inventory.
type Count struct {
	Datapoints int `json:"datapoints"`
	Channels   int `json:"channels"`
	Devices    int `json:"devices"`
}

// Count counts the datapoints, channels and devices of the inventory a selection matches (its
// event rules; metrics and logs have no inventory).
func (s *Source) Count(m *selection.Matcher) Count {
	s.mu.RLock()
	defer s.mu.RUnlock()
	lang := s.o.Lang()
	var c Count
	devs := map[string]bool{}
	// the datapoint is the same whatever source its reports have: a selection of snapshots
	// only still counts the datapoints it would forward
	src := "event"
	if sources := m.Selection().Sources; len(sources) > 0 {
		src = sources[0]
	}
	for _, ch := range s.chans {
		hit := false
		for key := range ch.params {
			r := &record.Record{Kind: record.KindEvent, Interface: ch.iface, Device: ch.device, Channel: ch.index, Address: ch.address, Key: key, DeviceType: ch.devType, ChannelType: ch.typ, Source: src}
			s.label(r, s.names, lang)
			if m.Match(r.Subject()) {
				c.Datapoints++
				hit = true
			}
		}
		if hit {
			c.Channels++
			devs[ch.iface+"/"+ch.device] = true
		}
	}
	c.Devices = len(devs)
	return c
}
