package metrics

// The metric groups: what a selection's metric_groups names.
const (
	GroupServiceMessages = "service_messages"
	GroupRPCEvents       = "rpc_events"
	GroupRadio           = "radio"
	GroupReachability    = "reachability"
	GroupStalls          = "stalls"
	GroupServices        = "services"
	GroupCPU             = "cpu"
	GroupMemory          = "memory"
	GroupDiskIO          = "disk_io"
	GroupStorage         = "storage"
	GroupNetwork         = "network"
	GroupTemperature     = "temperature"
	GroupSystem          = "system"
)

// GroupInfo documents one group for the selection editor and the readme.
type GroupInfo struct {
	Name string `json:"name"`
	// Source is homematic (occulited's system API) or host (/proc and /sys).
	Source  string            `json:"source"`
	Title   map[string]string `json:"title"`
	Doc     map[string]string `json:"doc"`
	Metrics []MetricInfo      `json:"metrics"`
}

// MetricInfo documents one metric of a group.
type MetricInfo struct {
	Name   string            `json:"name"`
	Labels []string          `json:"labels"`
	Unit   string            `json:"unit"`
	Doc    map[string]string `json:"doc"`
}

func d(de, en string) map[string]string { return map[string]string{"de": de, "en": en} }

func m(name string, labels []string, unit, de, en string) MetricInfo {
	if labels == nil {
		labels = []string{}
	}
	return MetricInfo{Name: name, Labels: labels, Unit: unit, Doc: d(de, en)}
}

// Groups lists every group in the order the UI shows them. Every metric carries the label
// `host` besides the ones listed.
var Groups = []GroupInfo{
	{Name: GroupServiceMessages, Source: "homematic", Title: d("Servicemeldungen", "Service messages"), Doc: d("Die aktiven Servicemeldungen des Systems, gezählt je Typ und je Schnittstelle.", "The system's active service messages, counted per type and per interface."), Metrics: []MetricInfo{
		m("total", nil, "", "alle aktiven Servicemeldungen", "every active service message"),
		m("count", []string{"type"}, "", "aktive Meldungen je Typ (UNREACH, LOWBAT, CONFIG_PENDING, …)", "active messages per type (UNREACH, LOWBAT, CONFIG_PENDING, …)"),
		m("by_interface", []string{"interface"}, "", "aktive Meldungen je Schnittstelle", "active messages per interface"),
	}},
	{Name: GroupRPCEvents, Source: "homematic", Title: d("RPC-Ereignisse", "RPC events"), Doc: d("Die Ereignisse der Schnittstellenprozesse: was der Forwarder selbst empfängt und die Telegrammrate, die das System je Schnittstelle misst.", "The interface processes' events: what the forwarder itself receives and the telegram rate the system measures per interface."), Metrics: []MetricInfo{
		m("received_total", []string{"interface"}, "", "vom Forwarder empfangene Ereignisse seit dem Start", "events the forwarder received since its start"),
		m("rate_per_minute", []string{"interface"}, "1/min", "empfangene Ereignisse je Minute (über das Abfrageintervall)", "events received per minute (over the poll interval)"),
		m("interface_rate_per_minute", []string{"interface"}, "1/min", "Telegramme je Minute, wie das System sie je Schnittstellenprozess zählt", "telegrams per minute as the system counts them per interface process"),
		m("interface_up", []string{"interface"}, "", "1, wenn der Schnittstellenprozess antwortet", "1 while the interface process answers"),
	}},
	{Name: GroupRadio, Source: "homematic", Title: d("Funk", "Radio"), Doc: d("Die Funkmodule: Duty Cycle, Carrier Sense, Verbindung.", "The radio modules: duty cycle, carrier sense, connection."), Metrics: []MetricInfo{
		m("duty_cycle_percent", []string{"interface", "address", "type"}, "%", "Duty Cycle des Funkmoduls", "the radio module's duty cycle"),
		m("carrier_sense_percent", []string{"interface", "address", "type"}, "%", "Carrier Sense, wo das Modul ihn meldet", "carrier sense where the module reports it"),
		m("connected", []string{"interface", "address", "type"}, "", "1, wenn das Modul verbunden ist", "1 while the module is connected"),
		m("busy", nil, "", "1, wenn ein Duty Cycle bei 80 % oder darüber liegt", "1 while any duty cycle is at 80 % or above"),
		m("errors", nil, "", "Schnittstellenprozesse, die nicht antworten", "interface processes that do not answer"),
	}},
	{Name: GroupReachability, Source: "homematic", Title: d("Erreichbarkeit", "Reachability"), Doc: d("Ob die angemeldeten Callback-Empfänger der Schnittstellenprozesse eine Verbindung annehmen.", "Whether the interface processes' registered callback subscribers accept a connection."), Metrics: []MetricInfo{
		m("reachable", []string{"interface", "id", "url"}, "", "1, wenn der Empfänger erreichbar ist", "1 while the subscriber is reachable"),
		m("unreachable_total", nil, "", "nicht erreichbare Empfänger", "unreachable subscribers"),
	}},
	{Name: GroupStalls, Source: "homematic", Title: d("Blockaden", "Stalls"), Doc: d("Schnittstellenprozesse, die ein Callback-Empfänger festhält.", "Interface processes held by a callback listener."), Metrics: []MetricInfo{
		m("stalled", []string{"interface", "kind"}, "", "1 je blockierter Schnittstelle (kind: delivery, calls)", "1 per stalled interface (kind: delivery, calls)"),
		m("total", nil, "", "blockierte Schnittstellen", "stalled interfaces"),
	}},
	{Name: GroupServices, Source: "homematic", Title: d("Dienste und Addons", "Services and addons"), Doc: d("Die systemd-Units der Dienste und Addons: Zustand, beobachtete Neustarts, Speicher.", "The services' and addons' systemd units: state, observed restarts, memory."), Metrics: []MetricInfo{
		m("running", []string{"unit", "kind", "category"}, "", "1, wenn die Unit läuft", "1 while the unit runs"),
		m("failed", []string{"unit", "kind", "category"}, "", "1, wenn die Unit fehlgeschlagen ist", "1 while the unit has failed"),
		m("restarts_total", []string{"unit", "kind", "category"}, "", "Neustarts, die der Forwarder seit seinem Start beobachtet hat (neue Prozess-ID)", "restarts the forwarder observed since its start (a new process id)"),
		m("rss_bytes", []string{"unit", "kind", "category"}, "bytes", "Arbeitsspeicher der Prozesse der Unit", "resident memory of the unit's processes"),
	}},
	{Name: GroupCPU, Source: "host", Title: d("CPU und Last", "CPU and load"), Doc: d("Die CPU-Auslastung seit der letzten Abfrage und die Lastmittel.", "The CPU usage since the last poll and the load averages."), Metrics: []MetricInfo{
		m("usage_percent", []string{"mode"}, "%", "Auslastung je Modus (total, user, system, iowait, steal)", "usage per mode (total, user, system, iowait, steal)"),
		m("load", []string{"period"}, "", "Lastmittel über 1, 5 und 15 Minuten", "load average over 1, 5 and 15 minutes"),
		m("count", nil, "", "Anzahl CPUs", "number of CPUs"),
		m("running_tasks", nil, "", "laufende Tasks", "running tasks"),
	}},
	{Name: GroupMemory, Source: "host", Title: d("Arbeitsspeicher und Swap", "Memory and swap"), Doc: d("Gesamt, verfügbar, belegt, Puffer und Cache, Swap.", "Total, available, used, buffers and cache, swap."), Metrics: []MetricInfo{
		m("total_bytes", nil, "bytes", "Gesamtspeicher", "total memory"),
		m("available_bytes", nil, "bytes", "verfügbarer Speicher", "available memory"),
		m("used_bytes", nil, "bytes", "belegter Speicher (gesamt minus verfügbar)", "used memory (total less available)"),
		m("used_percent", nil, "%", "belegter Speicher in Prozent", "used memory in per cent"),
		m("free_bytes", nil, "bytes", "freier Speicher", "free memory"),
		m("buffers_bytes", nil, "bytes", "Puffer", "buffers"),
		m("cached_bytes", nil, "bytes", "Cache", "cache"),
		m("swap_total_bytes", nil, "bytes", "Swap gesamt", "swap total"),
		m("swap_used_bytes", nil, "bytes", "Swap belegt", "swap used"),
		m("swap_used_percent", nil, "%", "Swap belegt in Prozent", "swap used in per cent"),
	}},
	{Name: GroupDiskIO, Source: "host", Title: d("Datenträger-IO", "Disk IO"), Doc: d("Lese- und Schreibvorgänge je Datenträger (SD-Karte, USB, NVMe).", "Reads and writes per disk (SD card, USB, NVMe)."), Metrics: []MetricInfo{
		m("read_bytes_total", []string{"device"}, "bytes", "gelesene Bytes seit dem Start des Systems", "bytes read since the system started"),
		m("write_bytes_total", []string{"device"}, "bytes", "geschriebene Bytes seit dem Start des Systems", "bytes written since the system started"),
		m("reads_total", []string{"device"}, "", "Lesevorgänge seit dem Start", "reads since the start"),
		m("writes_total", []string{"device"}, "", "Schreibvorgänge seit dem Start", "writes since the start"),
		m("read_bytes_per_second", []string{"device"}, "bytes/s", "Leserate seit der letzten Abfrage", "read rate since the last poll"),
		m("write_bytes_per_second", []string{"device"}, "bytes/s", "Schreibrate seit der letzten Abfrage", "write rate since the last poll"),
		m("reads_per_second", []string{"device"}, "1/s", "Lesevorgänge je Sekunde", "reads per second"),
		m("writes_per_second", []string{"device"}, "1/s", "Schreibvorgänge je Sekunde", "writes per second"),
		m("busy_percent", []string{"device"}, "%", "Anteil der Zeit mit IO", "share of time with IO in flight"),
	}},
	{Name: GroupStorage, Source: "host", Title: d("Speicherplatz", "Storage"), Doc: d("Belegung der eingehängten Dateisysteme (Rootfs, Userfs, USB).", "Usage of the mounted filesystems (rootfs, userfs, USB)."), Metrics: []MetricInfo{
		m("total_bytes", []string{"mount", "device", "fstype"}, "bytes", "Größe", "size"),
		m("used_bytes", []string{"mount", "device", "fstype"}, "bytes", "belegt", "used"),
		m("free_bytes", []string{"mount", "device", "fstype"}, "bytes", "frei für Benutzer", "free for users"),
		m("used_percent", []string{"mount", "device", "fstype"}, "%", "belegt in Prozent", "used in per cent"),
		m("inodes_used_percent", []string{"mount", "device", "fstype"}, "%", "belegte Inodes in Prozent", "inodes used in per cent"),
	}},
	{Name: GroupNetwork, Source: "host", Title: d("Netzwerk", "Network"), Doc: d("Bytes, Pakete, Fehler und verworfene Pakete je Netzwerkschnittstelle.", "Bytes, packets, errors and drops per network interface."), Metrics: []MetricInfo{
		m("rx_bytes_total", []string{"interface"}, "bytes", "empfangene Bytes", "bytes received"),
		m("tx_bytes_total", []string{"interface"}, "bytes", "gesendete Bytes", "bytes sent"),
		m("rx_packets_total", []string{"interface"}, "", "empfangene Pakete", "packets received"),
		m("tx_packets_total", []string{"interface"}, "", "gesendete Pakete", "packets sent"),
		m("rx_errors_total", []string{"interface"}, "", "Empfangsfehler", "receive errors"),
		m("tx_errors_total", []string{"interface"}, "", "Sendefehler", "transmit errors"),
		m("rx_dropped_total", []string{"interface"}, "", "verworfen beim Empfang", "dropped on receive"),
		m("tx_dropped_total", []string{"interface"}, "", "verworfen beim Senden", "dropped on transmit"),
		m("rx_bytes_per_second", []string{"interface"}, "bytes/s", "Empfangsrate seit der letzten Abfrage", "receive rate since the last poll"),
		m("tx_bytes_per_second", []string{"interface"}, "bytes/s", "Senderate seit der letzten Abfrage", "transmit rate since the last poll"),
	}},
	{Name: GroupTemperature, Source: "host", Title: d("Temperatur", "Temperature"), Doc: d("Die Temperatursensoren des Rechners (CPU, SoC, Datenträger).", "The machine's temperature sensors (CPU, SoC, disks)."), Metrics: []MetricInfo{
		m("celsius", []string{"sensor", "source"}, "°C", "Temperatur je Sensor", "temperature per sensor"),
	}},
	{Name: GroupSystem, Source: "host", Title: d("System", "System"), Doc: d("Laufzeit, Prozesse, offene Dateien.", "Uptime, processes, open files."), Metrics: []MetricInfo{
		m("uptime_seconds", nil, "s", "Sekunden seit dem Start des Systems", "seconds since the system started"),
		m("boot_time_seconds", nil, "s", "Startzeit des Systems (Unix-Zeit)", "the system's boot time (Unix time)"),
		m("processes", nil, "", "Anzahl Prozesse", "number of processes"),
		m("tasks", nil, "", "Anzahl Tasks (Threads)", "number of tasks (threads)"),
		m("open_files", nil, "", "offene Dateien", "open files"),
	}},
}

// GroupNames lists the group names in order.
func GroupNames() []string {
	out := make([]string, 0, len(Groups))
	for _, g := range Groups {
		out = append(out, g.Name)
	}
	return out
}
