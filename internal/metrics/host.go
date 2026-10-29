package metrics

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// --- the host, from /proc and /sys ---

type cpuSample struct {
	user, nice, system, idle, iowait, irq, softirq, steal uint64
}

func (c cpuSample) total() uint64 {
	return c.user + c.nice + c.system + c.idle + c.iowait + c.irq + c.softirq + c.steal
}

func (p *Poller) cpu(emit emitFunc) error {
	f, err := os.Open(filepath.Join(p.o.ProcRoot, "stat"))
	if err != nil {
		return err
	}
	defer f.Close()
	var cur *cpuSample
	cpus := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 8 || !strings.HasPrefix(fields[0], "cpu") {
			continue
		}
		if fields[0] != "cpu" {
			cpus++
			continue
		}
		var n [8]uint64
		for i := 0; i < 8 && i+1 < len(fields); i++ {
			n[i], _ = strconv.ParseUint(fields[i+1], 10, 64)
		}
		cur = &cpuSample{n[0], n[1], n[2], n[3], n[4], n[5], n[6], n[7]}
	}
	if cur == nil {
		return fmt.Errorf("no cpu line in %s", filepath.Join(p.o.ProcRoot, "stat"))
	}
	p.mu.Lock()
	prev := p.prevCPU
	p.prevCPU = cur
	p.mu.Unlock()
	if prev != nil && cur.total() > prev.total() {
		d := float64(cur.total() - prev.total())
		pct := func(a, b uint64) float64 {
			if a < b {
				return 0
			}
			return float64(a-b) / d * 100
		}
		idle := pct(cur.idle, prev.idle)
		emit(GroupCPU, "usage_percent", 100-idle, "%", map[string]string{"mode": "total"})
		emit(GroupCPU, "usage_percent", pct(cur.user+cur.nice, prev.user+prev.nice), "%", map[string]string{"mode": "user"})
		emit(GroupCPU, "usage_percent", pct(cur.system+cur.irq+cur.softirq, prev.system+prev.irq+prev.softirq), "%", map[string]string{"mode": "system"})
		emit(GroupCPU, "usage_percent", pct(cur.iowait, prev.iowait), "%", map[string]string{"mode": "iowait"})
		emit(GroupCPU, "usage_percent", pct(cur.steal, prev.steal), "%", map[string]string{"mode": "steal"})
	}
	emit(GroupCPU, "count", float64(cpus), "", nil)
	if b, err := os.ReadFile(filepath.Join(p.o.ProcRoot, "loadavg")); err == nil {
		fields := strings.Fields(string(b))
		for i, period := range []string{"1", "5", "15"} {
			if i < len(fields) {
				if v, err := strconv.ParseFloat(fields[i], 64); err == nil {
					emit(GroupCPU, "load", v, "", map[string]string{"period": period})
				}
			}
		}
		if len(fields) > 3 {
			if a, b, ok := strings.Cut(fields[3], "/"); ok {
				if r, err := strconv.ParseFloat(a, 64); err == nil {
					emit(GroupCPU, "running_tasks", r, "", nil)
				}
				if t, err := strconv.ParseFloat(b, 64); err == nil {
					emit(GroupSystem, "tasks", t, "", nil)
				}
			}
		}
	}
	return nil
}

func (p *Poller) memory(emit emitFunc) error {
	m, err := p.meminfo()
	if err != nil {
		return err
	}
	kb := func(k string) float64 { return float64(m[k]) * 1024 }
	total, avail := kb("MemTotal"), kb("MemAvailable")
	if _, ok := m["MemAvailable"]; !ok {
		avail = kb("MemFree") + kb("Buffers") + kb("Cached")
	}
	emit(GroupMemory, "total_bytes", total, "bytes", nil)
	emit(GroupMemory, "available_bytes", avail, "bytes", nil)
	emit(GroupMemory, "used_bytes", total-avail, "bytes", nil)
	if total > 0 {
		emit(GroupMemory, "used_percent", (total-avail)/total*100, "%", nil)
	}
	emit(GroupMemory, "free_bytes", kb("MemFree"), "bytes", nil)
	emit(GroupMemory, "buffers_bytes", kb("Buffers"), "bytes", nil)
	emit(GroupMemory, "cached_bytes", kb("Cached"), "bytes", nil)
	st, sf := kb("SwapTotal"), kb("SwapFree")
	emit(GroupMemory, "swap_total_bytes", st, "bytes", nil)
	emit(GroupMemory, "swap_used_bytes", st-sf, "bytes", nil)
	if st > 0 {
		emit(GroupMemory, "swap_used_percent", (st-sf)/st*100, "%", nil)
	}
	return nil
}

func (p *Poller) meminfo() (map[string]uint64, error) {
	f, err := os.Open(filepath.Join(p.o.ProcRoot, "meminfo"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	m := map[string]uint64{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		fields := strings.Fields(v)
		if len(fields) == 0 {
			continue
		}
		n, err := strconv.ParseUint(fields[0], 10, 64)
		if err == nil {
			m[k] = n
		}
	}
	return m, nil
}

type diskSample struct {
	reads, readSectors, writes, writeSectors, ioMS uint64
}

// diskIO reads /proc/diskstats for the whole disks (not their partitions): mmcblk*, sd*,
// nvme*n*, vd*, xvd*.
func (p *Poller) diskIO(emit emitFunc) error {
	f, err := os.Open(filepath.Join(p.o.ProcRoot, "diskstats"))
	if err != nil {
		return err
	}
	defer f.Close()
	cur := map[string]diskSample{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 14 {
			continue
		}
		name := fields[2]
		if !isWholeDisk(name) {
			continue
		}
		var n [11]uint64
		for i := range n {
			n[i], _ = strconv.ParseUint(fields[3+i], 10, 64)
		}
		cur[name] = diskSample{reads: n[0], readSectors: n[2], writes: n[4], writeSectors: n[6], ioMS: n[9]}
	}
	p.mu.Lock()
	prev, prevAt := p.prevDisk, p.prevAt
	p.prevDisk = cur
	p.mu.Unlock()
	secs := 0.0
	if !prevAt.IsZero() {
		secs = p.o.Now().Sub(prevAt).Seconds()
	}
	for _, name := range sortedDisk(cur) {
		d := cur[name]
		l := map[string]string{"device": name}
		emit(GroupDiskIO, "read_bytes_total", float64(d.readSectors)*512, "bytes", copyLabels(l))
		emit(GroupDiskIO, "write_bytes_total", float64(d.writeSectors)*512, "bytes", copyLabels(l))
		emit(GroupDiskIO, "reads_total", float64(d.reads), "", copyLabels(l))
		emit(GroupDiskIO, "writes_total", float64(d.writes), "", copyLabels(l))
		if o, ok := prev[name]; ok && secs > 0 {
			emit(GroupDiskIO, "read_bytes_per_second", delta(d.readSectors, o.readSectors)*512/secs, "bytes/s", copyLabels(l))
			emit(GroupDiskIO, "write_bytes_per_second", delta(d.writeSectors, o.writeSectors)*512/secs, "bytes/s", copyLabels(l))
			emit(GroupDiskIO, "reads_per_second", delta(d.reads, o.reads)/secs, "1/s", copyLabels(l))
			emit(GroupDiskIO, "writes_per_second", delta(d.writes, o.writes)/secs, "1/s", copyLabels(l))
			busy := delta(d.ioMS, o.ioMS) / 1000 / secs * 100
			if busy > 100 {
				busy = 100
			}
			emit(GroupDiskIO, "busy_percent", busy, "%", copyLabels(l))
		}
	}
	return nil
}

func delta(a, b uint64) float64 {
	if a < b {
		return 0
	}
	return float64(a - b)
}

func isWholeDisk(name string) bool {
	switch {
	case strings.HasPrefix(name, "mmcblk"):
		return !strings.Contains(name, "p") && !strings.Contains(name, "boot") && !strings.Contains(name, "rpmb")
	case strings.HasPrefix(name, "nvme"):
		return nvmeDisk.MatchString(name)
	case strings.HasPrefix(name, "sd"), strings.HasPrefix(name, "vd"), strings.HasPrefix(name, "xvd"):
		return strings.TrimRight(name, "0123456789") == name
	}
	return false
}

var nvmeDisk = regexp.MustCompile(`^nvme[0-9]+n[0-9]+$`)

func sortedDisk(m map[string]diskSample) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// storageFS are the filesystems a mount is reported for.
var storageFS = map[string]bool{"ext2": true, "ext3": true, "ext4": true, "vfat": true, "exfat": true, "ntfs": true, "ntfs3": true, "btrfs": true, "xfs": true, "f2fs": true, "squashfs": true, "fuseblk": true}

// storage reads the mounts of real filesystems and their statfs; a device mounted twice is
// reported at its first mount point.
func (p *Poller) storage(emit emitFunc) error {
	f, err := os.Open(filepath.Join(p.o.ProcRoot, "mounts"))
	if err != nil {
		return err
	}
	defer f.Close()
	seen := map[string]bool{}
	type mnt struct{ dev, mount, fstype string }
	var mounts []mnt
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 3 || !storageFS[fields[2]] || !strings.HasPrefix(fields[0], "/dev/") {
			continue
		}
		if seen[fields[0]] {
			continue
		}
		seen[fields[0]] = true
		mounts = append(mounts, mnt{fields[0], unescapeMount(fields[1]), fields[2]})
	}
	for _, m := range mounts {
		var st syscall.Statfs_t
		if err := p.statfs(m.mount, &st); err != nil {
			continue
		}
		bs := float64(st.Bsize)
		total := float64(st.Blocks) * bs
		free := float64(st.Bfree) * bs
		avail := float64(st.Bavail) * bs
		used := total - free
		l := map[string]string{"mount": m.mount, "device": strings.TrimPrefix(m.dev, "/dev/"), "fstype": m.fstype}
		emit(GroupStorage, "total_bytes", total, "bytes", copyLabels(l))
		emit(GroupStorage, "used_bytes", used, "bytes", copyLabels(l))
		emit(GroupStorage, "free_bytes", avail, "bytes", copyLabels(l))
		if used+avail > 0 {
			emit(GroupStorage, "used_percent", used/(used+avail)*100, "%", copyLabels(l))
		}
		emit(GroupStorage, "inodes_used_percent", inodePct(st), "%", copyLabels(l))
	}
	return nil
}

func inodePct(st syscall.Statfs_t) float64 {
	if st.Files == 0 {
		return 0
	}
	return float64(st.Files-st.Ffree) / float64(st.Files) * 100
}

// statfs is syscall.Statfs; a test with fixtures replaces the mount table but statfs runs on
// the real path, so the fixtures name paths that exist.
func (p *Poller) statfs(path string, st *syscall.Statfs_t) error {
	return syscall.Statfs(path, st)
}

func unescapeMount(s string) string {
	return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`).Replace(s)
}

type netSample struct {
	rxBytes, rxPackets, rxErrors, rxDropped, txBytes, txPackets, txErrors, txDropped uint64
}

func (p *Poller) network(emit emitFunc) error {
	f, err := os.Open(filepath.Join(p.o.ProcRoot, "net", "dev"))
	if err != nil {
		return err
	}
	defer f.Close()
	cur := map[string]netSample{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		name, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "lo" || name == "" {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) < 16 {
			continue
		}
		var n [16]uint64
		for i := range n {
			n[i], _ = strconv.ParseUint(fields[i], 10, 64)
		}
		cur[name] = netSample{rxBytes: n[0], rxPackets: n[1], rxErrors: n[2], rxDropped: n[3], txBytes: n[8], txPackets: n[9], txErrors: n[10], txDropped: n[11]}
	}
	p.mu.Lock()
	prev, prevAt := p.prevNet, p.prevAt
	p.prevNet = cur
	p.mu.Unlock()
	secs := 0.0
	if !prevAt.IsZero() {
		secs = p.o.Now().Sub(prevAt).Seconds()
	}
	names := make([]string, 0, len(cur))
	for k := range cur {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, name := range names {
		s := cur[name]
		l := map[string]string{"interface": name}
		emit(GroupNetwork, "rx_bytes_total", float64(s.rxBytes), "bytes", copyLabels(l))
		emit(GroupNetwork, "tx_bytes_total", float64(s.txBytes), "bytes", copyLabels(l))
		emit(GroupNetwork, "rx_packets_total", float64(s.rxPackets), "", copyLabels(l))
		emit(GroupNetwork, "tx_packets_total", float64(s.txPackets), "", copyLabels(l))
		emit(GroupNetwork, "rx_errors_total", float64(s.rxErrors), "", copyLabels(l))
		emit(GroupNetwork, "tx_errors_total", float64(s.txErrors), "", copyLabels(l))
		emit(GroupNetwork, "rx_dropped_total", float64(s.rxDropped), "", copyLabels(l))
		emit(GroupNetwork, "tx_dropped_total", float64(s.txDropped), "", copyLabels(l))
		if o, ok := prev[name]; ok && secs > 0 {
			emit(GroupNetwork, "rx_bytes_per_second", delta(s.rxBytes, o.rxBytes)/secs, "bytes/s", copyLabels(l))
			emit(GroupNetwork, "tx_bytes_per_second", delta(s.txBytes, o.txBytes)/secs, "bytes/s", copyLabels(l))
		}
	}
	return nil
}

// temperature reads the thermal zones and the hwmon temperature inputs.
func (p *Poller) temperature(emit emitFunc) error {
	n := 0
	zones, _ := filepath.Glob(filepath.Join(p.o.SysRoot, "class", "thermal", "thermal_zone*"))
	sort.Strings(zones)
	for _, z := range zones {
		v, err := readMilli(filepath.Join(z, "temp"))
		if err != nil {
			continue
		}
		typ := strings.TrimSpace(readString(filepath.Join(z, "type")))
		if typ == "" {
			typ = filepath.Base(z)
		}
		emit(GroupTemperature, "celsius", v, "°C", map[string]string{"sensor": typ, "source": "thermal"})
		n++
	}
	hwmons, _ := filepath.Glob(filepath.Join(p.o.SysRoot, "class", "hwmon", "hwmon*"))
	sort.Strings(hwmons)
	for _, h := range hwmons {
		name := strings.TrimSpace(readString(filepath.Join(h, "name")))
		inputs, _ := filepath.Glob(filepath.Join(h, "temp*_input"))
		sort.Strings(inputs)
		for _, in := range inputs {
			v, err := readMilli(in)
			if err != nil {
				continue
			}
			label := strings.TrimSpace(readString(strings.TrimSuffix(in, "_input") + "_label"))
			sensor := name
			if label != "" {
				sensor = name + " " + label
			} else if len(inputs) > 1 {
				sensor = name + " " + strings.TrimSuffix(filepath.Base(in), "_input")
			}
			emit(GroupTemperature, "celsius", v, "°C", map[string]string{"sensor": sensor, "source": "hwmon"})
			n++
		}
	}
	if n == 0 && len(zones) == 0 && len(hwmons) == 0 {
		return fmt.Errorf("no temperature sensor below %s", p.o.SysRoot)
	}
	return nil
}

func (p *Poller) system(emit emitFunc) error {
	b, err := os.ReadFile(filepath.Join(p.o.ProcRoot, "uptime"))
	if err != nil {
		return err
	}
	fields := strings.Fields(string(b))
	if len(fields) > 0 {
		if v, err := strconv.ParseFloat(fields[0], 64); err == nil {
			emit(GroupSystem, "uptime_seconds", v, "s", nil)
			emit(GroupSystem, "boot_time_seconds", float64(p.o.Now().Unix())-v, "s", nil)
		}
	}
	entries, err := os.ReadDir(p.o.ProcRoot)
	if err == nil {
		n := 0
		for _, e := range entries {
			if e.IsDir() && e.Name()[0] >= '0' && e.Name()[0] <= '9' {
				n++
			}
		}
		emit(GroupSystem, "processes", float64(n), "", nil)
	}
	if b, err := os.ReadFile(filepath.Join(p.o.ProcRoot, "sys", "fs", "file-nr")); err == nil {
		if f := strings.Fields(string(b)); len(f) > 0 {
			if v, err := strconv.ParseFloat(f[0], 64); err == nil {
				emit(GroupSystem, "open_files", v, "", nil)
			}
		}
	}
	return nil
}

// rssOf reads a process's VmRSS in bytes.
func (p *Poller) rssOf(pid int) (int64, bool) {
	f, err := os.Open(filepath.Join(p.o.ProcRoot, strconv.Itoa(pid), "status"))
	if err != nil {
		return 0, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "VmRSS:") {
			fields := strings.Fields(sc.Text())
			if len(fields) >= 2 {
				kb, err := strconv.ParseInt(fields[1], 10, 64)
				return kb * 1024, err == nil
			}
		}
	}
	return 0, false
}

func readMilli(path string) (float64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
	if err != nil {
		return 0, err
	}
	return n / 1000, nil
}

func readString(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}
