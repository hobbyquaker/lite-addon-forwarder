package metrics

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hobbyquaker/lite-addon-forwarder/internal/record"
)

// fixtures writes a /proc and a /sys tree; the second call writes the "later" samples.
func fixtures(t *testing.T, dir string, later bool) {
	t.Helper()
	w := func(path, content string) {
		t.Helper()
		p := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stat := "cpu  1000 0 500 8000 100 0 0 0 0 0\ncpu0 500 0 250 4000 50 0 0 0 0 0\ncpu1 500 0 250 4000 50 0 0 0 0 0\nintr 1\nctxt 1\nbtime 1\n"
	disk := "   1  0 mmcblk0 100 0 2000 50 200 0 4000 80 0 1000 130 0 0 0 0 0 0\n   1  1 mmcblk0p1 1 0 2 1 1 0 2 1 0 1 1 0 0 0 0 0 0\n   8  0 sda 10 0 100 5 20 0 200 8 0 10 13 0 0 0 0 0 0\n 179 16 mmcblk0boot0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0\n"
	net := "Inter-|   Receive                                                |  Transmit\n face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed\n    lo: 100 1 0 0 0 0 0 0 100 1 0 0 0 0 0 0\n  eth0: 1000 10 1 2 0 0 0 0 2000 20 3 4 0 0 0 0\n"
	if later {
		stat = "cpu  1100 0 550 8300 150 0 0 0 0 0\ncpu0 550 0 275 4150 75 0 0 0 0 0\ncpu1 550 0 275 4150 75 0 0 0 0 0\n"
		disk = "   1  0 mmcblk0 110 0 2200 55 220 0 4400 88 0 1500 143 0 0 0 0 0 0\n   8  0 sda 10 0 100 5 20 0 200 8 0 10 13 0 0 0 0 0 0\n"
		net = "Inter-|   Receive                                                |  Transmit\n face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed\n    lo: 100 1 0 0 0 0 0 0 100 1 0 0 0 0 0 0\n  eth0: 2000 20 1 2 0 0 0 0 2500 25 3 4 0 0 0 0\n"
	}
	w("proc/stat", stat)
	w("proc/loadavg", "0.50 0.40 0.30 2/150 1234\n")
	w("proc/meminfo", "MemTotal:        1000000 kB\nMemFree:          200000 kB\nMemAvailable:     600000 kB\nBuffers:           50000 kB\nCached:           300000 kB\nSwapTotal:        100000 kB\nSwapFree:          75000 kB\n")
	w("proc/diskstats", disk)
	w("proc/mounts", "/dev/root / ext4 ro,relatime 0 0\nproc /proc proc rw 0 0\n/dev/root /also ext4 rw 0 0\ntmpfs /tmp tmpfs rw 0 0\n")
	w("proc/net/dev", net)
	w("proc/uptime", "3600.50 7000.00\n")
	w("proc/sys/fs/file-nr", "416\t0\t9223372036854775807\n")
	w("proc/4711/status", "Name:\trfd\nVmRSS:\t   12345 kB\n")
	w("proc/4712/status", "Name:\thelper\nVmRSS:\t   1000 kB\n")
	w("sys/class/thermal/thermal_zone0/temp", "45678\n")
	w("sys/class/thermal/thermal_zone0/type", "cpu-thermal\n")
	w("sys/class/hwmon/hwmon0/name", "rpi_volt\n")
	w("sys/class/hwmon/hwmon1/name", "nvme\n")
	w("sys/class/hwmon/hwmon1/temp1_input", "38000\n")
	w("sys/class/hwmon/hwmon1/temp1_label", "Composite\n")
}

// fakeSystem answers the system API routes the poller reads.
func fakeSystem(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	j := func(v any) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer tok" {
				w.WriteHeader(401)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(v)
		}
	}
	mux.HandleFunc("/api/system/v1/service-messages", j(map[string]any{"count": 3, "messages": []map[string]any{
		{"interface": "HmIP-RF", "address": "000A:0", "key": "UNREACH"},
		{"interface": "HmIP-RF", "address": "000B:0", "key": "LOW_BAT"},
		{"interface": "BidCos-RF", "address": "MEQ1:0", "key": "UNREACH"},
	}}))
	mux.HandleFunc("/api/system/v1/radio/health", j(map[string]any{
		"interfaces": []map[string]any{{"interface": "HmIP-RF", "address": "3014F711A0001F58A9A1B2C3", "type": "HmIP-RFUSB", "connected": true, "duty_cycle": 7, "carrier_sense": 12}, {"interface": "BidCos-RF", "address": "JEQ0534849", "type": "HM-CFG-USB-2", "connected": false, "duty_cycle": 0}},
		"errors":     map[string]string{"HmIP-RF": "x"},
		"rates":      map[string]any{"HmIP-RF": []map[string]any{{"t": "2026-10-30T10:00:00Z", "in": 3.5, "up": true}, {"t": "2026-10-30T10:01:00Z", "in": 4.25, "up": true}}},
		"busy":       false,
	}))
	mux.HandleFunc("/api/system/v1/radio/subscribers/reachability", j(map[string]any{"subscribers": []map[string]any{
		{"interface": "HmIP-RF", "id": "occulited", "url": "http://127.0.0.1:1999", "reachable": true},
		{"interface": "HmIP-RF", "id": "nodered", "url": "http://10.0.0.5:2001", "reachable": false, "reason": "refused"},
		{"interface": "BidCos-RF", "id": "x", "url": "binrpc://10.0.0.5"},
	}}))
	mux.HandleFunc("/api/system/v1/radio/subscribers/stalls", j(map[string]any{"stalls": []map[string]any{{"interface": "HmIP-RF", "kind": "delivery"}}}))
	var pid int
	var mu sync.Mutex
	mux.HandleFunc("/api/system/v1/services", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		pid++
		leader := 4711
		if pid > 1 {
			leader = 4712 // a restart: a new leader pid
		}
		mu.Unlock()
		j(map[string]any{"systemd": true, "services": []map[string]any{
			{"id": "rfd", "kind": "system", "category": "radio", "running": true, "pid": leader, "procs": []map[string]any{{"pid": leader, "cmd": "rfd"}, {"pid": 4712, "cmd": "helper"}}},
			{"id": "addon-forwarder", "kind": "addon", "category": "addon", "running": false, "failed": true},
		}})(w, r)
	})
	mux.HandleFunc("/api/system/v1/status", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`{"error":"forbidden","scope":"system:read"}`))
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

type authRT struct{ tok string }

func (a authRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+a.tok)
	return http.DefaultTransport.RoundTrip(r)
}

type collector struct {
	mu   sync.Mutex
	recs []*record.Record
}

func (c *collector) emit(r *record.Record) {
	c.mu.Lock()
	c.recs = append(c.recs, r)
	c.mu.Unlock()
}

func (c *collector) find(group, name string, labels map[string]string) (*record.Record, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
outer:
	for _, r := range c.recs {
		if r.Group != group || r.Name != name {
			continue
		}
		for k, v := range labels {
			if r.Labels[k] != v {
				continue outer
			}
		}
		return r, true
	}
	return nil, false
}

func (c *collector) reset() {
	c.mu.Lock()
	c.recs = nil
	c.mu.Unlock()
}

func TestPollEverything(t *testing.T) {
	dir := t.TempDir()
	fixtures(t, dir, false)
	sys := fakeSystem(t)
	col := &collector{}
	now := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	events := map[string]int64{"HmIP-RF": 100, "BidCos-RF": 10}
	p := New(Options{SystemURL: sys.URL, HTTP: &http.Client{Transport: authRT{"tok"}}, ProcRoot: filepath.Join(dir, "proc"), SysRoot: filepath.Join(dir, "sys"), Emit: col.emit, Events: func() map[string]int64 { return events }, Now: func() time.Time { return now }, Log: t.Logf})
	p.Poll(context.Background())
	st := p.Stats()
	if len(st.Errors) != 0 {
		t.Fatalf("errors: %v", st.Errors)
	}
	if st.Polls != 1 || len(st.Groups) != len(Groups) {
		t.Fatalf("stats %+v", st)
	}
	want := []struct {
		group, name string
		labels      map[string]string
		value       float64
		unit        string
	}{
		{GroupServiceMessages, "total", nil, 3, ""},
		{GroupServiceMessages, "count", map[string]string{"type": "UNREACH"}, 2, ""},
		{GroupServiceMessages, "by_interface", map[string]string{"interface": "BidCos-RF"}, 1, ""},
		{GroupRPCEvents, "received_total", map[string]string{"interface": "HmIP-RF"}, 100, ""},
		{GroupRPCEvents, "interface_rate_per_minute", map[string]string{"interface": "HmIP-RF"}, 4.25, "1/min"},
		{GroupRPCEvents, "interface_up", map[string]string{"interface": "HmIP-RF"}, 1, ""},
		{GroupRadio, "duty_cycle_percent", map[string]string{"interface": "HmIP-RF"}, 7, "%"},
		{GroupRadio, "carrier_sense_percent", map[string]string{"interface": "HmIP-RF"}, 12, "%"},
		{GroupRadio, "connected", map[string]string{"interface": "BidCos-RF"}, 0, ""},
		{GroupRadio, "errors", nil, 1, ""},
		{GroupReachability, "reachable", map[string]string{"id": "nodered"}, 0, ""},
		{GroupReachability, "unreachable_total", nil, 1, ""},
		{GroupStalls, "stalled", map[string]string{"kind": "delivery"}, 1, ""},
		{GroupStalls, "total", nil, 1, ""},
		{GroupServices, "running", map[string]string{"unit": "rfd"}, 1, ""},
		{GroupServices, "failed", map[string]string{"unit": "addon-forwarder"}, 1, ""},
		{GroupServices, "restarts_total", map[string]string{"unit": "rfd"}, 0, ""},
		{GroupServices, "rss_bytes", map[string]string{"unit": "rfd"}, (12345 + 1000) * 1024, "bytes"},
		{GroupCPU, "count", nil, 2, ""},
		{GroupCPU, "load", map[string]string{"period": "5"}, 0.4, ""},
		{GroupCPU, "running_tasks", nil, 2, ""},
		{GroupSystem, "tasks", nil, 150, ""},
		{GroupMemory, "total_bytes", nil, 1000000 * 1024, "bytes"},
		{GroupMemory, "used_percent", nil, 40, "%"},
		{GroupMemory, "swap_used_percent", nil, 25, "%"},
		{GroupDiskIO, "read_bytes_total", map[string]string{"device": "mmcblk0"}, 2000 * 512, "bytes"},
		{GroupDiskIO, "writes_total", map[string]string{"device": "sda"}, 20, ""},
		{GroupStorage, "total_bytes", map[string]string{"mount": "/", "device": "root", "fstype": "ext4"}, -1, "bytes"},
		{GroupNetwork, "rx_bytes_total", map[string]string{"interface": "eth0"}, 1000, "bytes"},
		{GroupNetwork, "tx_dropped_total", map[string]string{"interface": "eth0"}, 4, ""},
		{GroupTemperature, "celsius", map[string]string{"sensor": "cpu-thermal"}, 45.678, "°C"},
		{GroupTemperature, "celsius", map[string]string{"sensor": "nvme Composite"}, 38, "°C"},
		{GroupSystem, "uptime_seconds", nil, 3600.5, "s"},
		{GroupSystem, "processes", nil, 2, ""},
		{GroupSystem, "open_files", nil, 416, ""},
	}
	for _, w := range want {
		r, ok := col.find(w.group, w.name, w.labels)
		if !ok {
			t.Errorf("%s/%s %v: missing", w.group, w.name, w.labels)
			continue
		}
		if w.value >= 0 && (*r.ValueNum < w.value-0.0001 || *r.ValueNum > w.value+0.0001) {
			t.Errorf("%s/%s %v: value %v, want %v", w.group, w.name, w.labels, *r.ValueNum, w.value)
		}
		if r.Unit != w.unit {
			t.Errorf("%s/%s: unit %q, want %q", w.group, w.name, r.Unit, w.unit)
		}
		if r.Labels["host"] == "" || r.Kind != record.KindMetric || !r.TS.Equal(now) {
			t.Errorf("%s/%s: host/kind/ts wrong: %+v", w.group, w.name, r)
		}
	}
	// the first poll has no rates
	for _, name := range []string{"usage_percent", "rate_per_minute"} {
		if _, ok := col.find(GroupCPU, name, nil); ok {
			t.Errorf("%s on the first poll", name)
		}
	}
	if _, ok := col.find(GroupNetwork, "rx_bytes_per_second", nil); ok {
		t.Error("network rate on the first poll")
	}
	// the "also" mount of the same device and the partition are left out
	if _, ok := col.find(GroupStorage, "total_bytes", map[string]string{"mount": "/also"}); ok {
		t.Error("a second mount of the same device was reported")
	}
	if _, ok := col.find(GroupDiskIO, "reads_total", map[string]string{"device": "mmcblk0p1"}); ok {
		t.Error("a partition was reported")
	}
	// the "lo" interface is left out
	if _, ok := col.find(GroupNetwork, "rx_bytes_total", map[string]string{"interface": "lo"}); ok {
		t.Error("lo was reported")
	}

	// the second poll, a minute later: the rates, the restart
	col.reset()
	fixtures(t, dir, true)
	now = now.Add(time.Minute)
	events = map[string]int64{"HmIP-RF": 160, "BidCos-RF": 10}
	p.Poll(context.Background())
	want2 := []struct {
		group, name string
		labels      map[string]string
		value       float64
	}{
		// total delta 1000: user 100, system 50, iowait 50, idle 300 of 500 -> 40 % busy
		{GroupCPU, "usage_percent", map[string]string{"mode": "total"}, 40},
		{GroupCPU, "usage_percent", map[string]string{"mode": "user"}, 20},
		{GroupCPU, "usage_percent", map[string]string{"mode": "iowait"}, 10},
		{GroupRPCEvents, "rate_per_minute", map[string]string{"interface": "HmIP-RF"}, 60},
		{GroupRPCEvents, "rate_per_minute", map[string]string{"interface": "BidCos-RF"}, 0},
		{GroupDiskIO, "read_bytes_per_second", map[string]string{"device": "mmcblk0"}, 200 * 512 / 60.0},
		{GroupDiskIO, "writes_per_second", map[string]string{"device": "mmcblk0"}, 20 / 60.0},
		{GroupDiskIO, "busy_percent", map[string]string{"device": "mmcblk0"}, 500.0 / 1000 / 60 * 100},
		{GroupNetwork, "rx_bytes_per_second", map[string]string{"interface": "eth0"}, 1000 / 60.0},
		{GroupNetwork, "tx_bytes_per_second", map[string]string{"interface": "eth0"}, 500 / 60.0},
		{GroupServices, "restarts_total", map[string]string{"unit": "rfd"}, 1},
	}
	for _, w := range want2 {
		r, ok := col.find(w.group, w.name, w.labels)
		if !ok {
			t.Errorf("second poll %s/%s %v: missing", w.group, w.name, w.labels)
			continue
		}
		if *r.ValueNum < w.value-0.001 || *r.ValueNum > w.value+0.001 {
			t.Errorf("second poll %s/%s %v: value %v, want %v", w.group, w.name, w.labels, *r.ValueNum, w.value)
		}
	}
	if _, ok := col.find(GroupDiskIO, "reads_total", map[string]string{"device": "sda"}); !ok {
		t.Error("sda missing on the second poll")
	}
}

func TestWantsAndErrors(t *testing.T) {
	dir := t.TempDir()
	fixtures(t, dir, false)
	// a system that refuses: the scope error is reported per group, the host groups still work
	sys := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`{"error":"forbidden","scope":"system:read","message":"the token lacks system:read"}`))
	}))
	t.Cleanup(sys.Close)
	col := &collector{}
	wanted := map[string]bool{GroupMemory: true, GroupRadio: true}
	p := New(Options{SystemURL: sys.URL, ProcRoot: filepath.Join(dir, "proc"), SysRoot: filepath.Join(dir, "sys"), Emit: col.emit, Wants: func(g string) bool { return wanted[g] }, Log: t.Logf})
	p.Poll(context.Background())
	st := p.Stats()
	sort.Strings(st.Groups)
	if strings.Join(st.Groups, ",") != "memory,radio" {
		t.Fatalf("groups %v", st.Groups)
	}
	if e := st.Errors[GroupRadio]; !strings.Contains(e, "lacks the scope system:read") {
		t.Fatalf("radio error %q", e)
	}
	if _, ok := st.Errors[GroupMemory]; ok {
		t.Fatal("memory errored")
	}
	if _, ok := col.find(GroupMemory, "total_bytes", nil); !ok {
		t.Fatal("memory not emitted")
	}
	if _, ok := col.find(GroupCPU, "count", nil); ok {
		t.Fatal("cpu polled although not wanted")
	}
	if st.Records != 10 {
		t.Fatalf("records %d", st.Records)
	}
}

func TestGroupsDocumented(t *testing.T) {
	seen := map[string]bool{}
	for _, g := range Groups {
		if seen[g.Name] {
			t.Fatalf("group %s twice", g.Name)
		}
		seen[g.Name] = true
		if g.Title["de"] == "" || g.Title["en"] == "" || g.Doc["de"] == "" || g.Doc["en"] == "" || (g.Source != "homematic" && g.Source != "host") {
			t.Errorf("group %s: incomplete", g.Name)
		}
		for _, m := range g.Metrics {
			if m.Doc["de"] == "" || m.Doc["en"] == "" || m.Labels == nil {
				t.Errorf("%s/%s: incomplete", g.Name, m.Name)
			}
		}
	}
	if len(GroupNames()) != len(Groups) {
		t.Fatal("names")
	}
}

func TestIsWholeDisk(t *testing.T) {
	for name, want := range map[string]bool{"mmcblk0": true, "mmcblk0p1": false, "mmcblk0boot0": false, "mmcblk0rpmb": false, "sda": true, "sda1": false, "sdb": true, "nvme0n1": true, "nvme0n1p2": false, "nvme0": false, "vda": true, "vda1": false, "loop0": false, "zram0": false, "ram0": false, "dm-0": false} {
		if got := isWholeDisk(name); got != want {
			t.Errorf("%s: %v", name, got)
		}
	}
}
