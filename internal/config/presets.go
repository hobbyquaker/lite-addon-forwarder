package config

// InfluxPresets are the template sets an Influx target starts from. "clean" is the default:
// a measurement per datapoint key, the address as tags, the names as tags too (readable; a
// rename starts a new series), the value as a typed field. "one_measurement" puts everything
// into one measurement with the key as a tag; "per_device" makes a measurement per device with
// a field per key. A tag named "*" stands for every label of a metric (or field of a log line).
var InfluxPresets = map[string]Influx{
	"clean": {
		Event: InfluxTemplates{
			Measurement: "{{ key }}",
			Tags: []NamedTemplate{
				{"interface", "{{ interface }}"},
				{"device", "{{ device }}"},
				{"channel", "{{ channel }}"},
				{"address", "{{ address }}"},
				{"device_type", "{{ device_type }}"},
				{"device_name", "{{ device_name }}"},
				{"channel_name", "{{ channel_name }}"},
				{"room", "{{ rooms | join(',') }}"},
				{"function", "{{ functions | join(',') }}"},
				{"unit", "{{ unit }}"},
			},
			Fields: []FieldTemplate{
				{"value", "{{ value }}", "auto"},
				{"value_num", "{{ value_num }}", "float"},
				{"value_text", "{{ value_text }}", "string"},
			},
		},
		Metric: InfluxTemplates{
			Measurement: "{{ group }}",
			Tags:        []NamedTemplate{{"*", ""}, {"unit", "{{ unit }}"}},
			Fields:      []FieldTemplate{{"{{ name }}", "{{ value_num }}", "float"}},
		},
		Log: InfluxTemplates{
			Measurement: "log",
			Tags:        []NamedTemplate{{"stream", "{{ stream }}"}, {"unit", "{{ log_unit }}"}, {"priority", "{{ priority }}"}},
			Fields:      []FieldTemplate{{"message", "{{ message }}", "string"}, {"user", "{{ fields.user }}", "string"}},
		},
	},
	"one_measurement": {
		Event: InfluxTemplates{
			Measurement: "homematic",
			Tags: []NamedTemplate{
				{"interface", "{{ interface }}"},
				{"device", "{{ device }}"},
				{"channel", "{{ channel }}"},
				{"key", "{{ key }}"},
				{"device_type", "{{ device_type }}"},
				{"device_name", "{{ device_name }}"},
				{"channel_name", "{{ channel_name }}"},
				{"room", "{{ rooms | join(',') }}"},
				{"function", "{{ functions | join(',') }}"},
			},
			Fields: []FieldTemplate{
				{"value", "{{ value }}", "auto"},
				{"value_num", "{{ value_num }}", "float"},
				{"value_text", "{{ value_text }}", "string"},
			},
		},
		Metric: InfluxTemplates{
			Measurement: "system",
			Tags:        []NamedTemplate{{"group", "{{ group }}"}, {"*", ""}},
			Fields:      []FieldTemplate{{"{{ name }}", "{{ value_num }}", "float"}},
		},
		Log: InfluxTemplates{
			Measurement: "log",
			Tags:        []NamedTemplate{{"stream", "{{ stream }}"}, {"unit", "{{ log_unit }}"}, {"priority", "{{ priority }}"}},
			Fields:      []FieldTemplate{{"message", "{{ message }}", "string"}},
		},
	},
	"per_device": {
		Event: InfluxTemplates{
			Measurement: "{{ device_name | default(device) }}",
			Tags: []NamedTemplate{
				{"interface", "{{ interface }}"},
				{"device", "{{ device }}"},
				{"channel", "{{ channel }}"},
				{"channel_name", "{{ channel_name }}"},
				{"room", "{{ rooms | join(',') }}"},
			},
			Fields: []FieldTemplate{
				{"{{ key }}", "{{ value }}", "auto"},
			},
		},
		Metric: InfluxTemplates{
			Measurement: "{{ group }}",
			Tags:        []NamedTemplate{{"*", ""}},
			Fields:      []FieldTemplate{{"{{ name }}", "{{ value_num }}", "float"}},
		},
		Log: InfluxTemplates{
			Measurement: "log",
			Tags:        []NamedTemplate{{"stream", "{{ stream }}"}, {"unit", "{{ log_unit }}"}, {"priority", "{{ priority }}"}},
			Fields:      []FieldTemplate{{"message", "{{ message }}", "string"}},
		},
	},
}

// PresetNames lists the Influx presets in the UI's order.
var PresetNames = []string{"clean", "one_measurement", "per_device"}

// WebhookPresets are the body templates a webhook starts from.
var WebhookPresets = map[string]Webhook{
	"record":  {Method: "POST", Format: "json", Body: "{{ record | tojson }}", Auth: "none", Headers: map[string]string{}},
	"compact": {Method: "POST", Format: "ndjson", Body: `{"ts": {{ ts_ms }}, "address": "{{ interface }}/{{ address }}", "key": "{{ key }}", "value": {{ value | tojson }}, "name": {{ channel_name | tojson }}}`, Auth: "none", Headers: map[string]string{}},
}

// ElasticPresets are the document templates an Elasticsearch target starts from: "record" is
// the whole record (the default), "flat" a small document with the value fields only.
var ElasticPresets = map[string]Elastic{
	"record": {Index: "homematic-{{ kind }}-{{ ts | date('2006.01.02') }}", Document: "{{ record | tojson }}", Timestamp: true, Action: "index", Auth: "none", Gzip: true, InstallTemplate: true, TemplatePattern: "homematic-*", Preset: "record"},
	"flat":   {Index: "homematic-{{ ts | date('2006.01') }}", Document: `{"kind": "{{ kind }}", "address": "{{ interface }}/{{ address }}", "key": {{ key | tojson }}, "name": {{ channel_name | tojson }}, "device_name": {{ device_name | tojson }}, "rooms": {{ rooms | tojson }}, "value_num": {{ value_num | tojson }}, "value_text": {{ value_text | tojson }}, "group": "{{ group }}", "metric": "{{ name }}", "labels": {{ labels | tojson }}}`, Timestamp: true, Action: "index", Auth: "none", Gzip: true, InstallTemplate: true, TemplatePattern: "homematic-*", Preset: "flat"},
}

// ElasticPresetNames lists the Elastic presets in the UI's order.
var ElasticPresetNames = []string{"record", "flat"}

// PrometheusPresets are the name and label templates a Prometheus target starts from: "clean"
// names an event homematic_<key> with the address and the names as labels and a metric
// <group>_<name> with its labels; "by_name" names an event by the channel name instead.
var PrometheusPresets = map[string]Prometheus{
	"clean": {Mode: "scrape", Auth: "none", Name: "{% if kind == 'event' %}homematic_{{ key | lower }}{% elif kind == 'metric' %}{{ group }}_{{ name }}{% endif %}", Labels: []NamedTemplate{
		{"*", ""},
		{"interface", "{{ interface }}"},
		{"address", "{{ address }}"},
		{"device_type", "{{ device_type }}"},
		{"device_name", "{{ device_name }}"},
		{"channel_name", "{{ channel_name }}"},
		{"room", "{{ rooms | join(',') }}"},
		{"function", "{{ functions | join(',') }}"},
	}, Preset: "clean"},
	"one_metric": {Mode: "scrape", Auth: "none", Name: "{% if kind == 'event' %}homematic_value{% elif kind == 'metric' %}{{ group }}_{{ name }}{% endif %}", Labels: []NamedTemplate{
		{"*", ""},
		{"interface", "{{ interface }}"},
		{"address", "{{ address }}"},
		{"key", "{{ key }}"},
		{"device_name", "{{ device_name }}"},
		{"channel_name", "{{ channel_name }}"},
		{"room", "{{ rooms | join(',') }}"},
	}, Preset: "one_metric"},
}

// PrometheusPresetNames lists the Prometheus presets in the UI's order.
var PrometheusPresetNames = []string{"clean", "one_metric"}

// SQLStatements are a dialect's INSERT and schema of an SQL preset.
type SQLStatements struct {
	Insert string `json:"insert"`
	Schema string `json:"schema"`
}

// SQLPreset is one SQL preset: the statements per dialect.
type SQLPreset struct {
	Postgres SQLStatements `json:"postgres"`
	MySQL    SQLStatements `json:"mysql"`
	// TimescaleOnly marks a preset that needs TimescaleDB (postgres only).
	TimescaleOnly bool `json:"timescale_only"`
}

const sqlWideInsertPG = `INSERT INTO homematic (ts, kind, interface, device, channel, address, key, value_num, value_text, value_bool, type, unit,
  device_type, channel_type, device_name, channel_name, rooms, functions, source,
  metric_group, metric_name, labels, stream, log_unit, priority, message, fields)
VALUES (:ts, :kind, :interface, :device, :channel, :address, :key, :value_num, :value_text, :value_bool, :type, :unit,
  :device_type, :channel_type, :device_name, :channel_name, :rooms::jsonb, :functions::jsonb, :source,
  :group, :name, :labels::jsonb, :stream, :log_unit, :priority, :message, :fields::jsonb)`

const sqlWideSchemaPG = `CREATE TABLE IF NOT EXISTS homematic (
  ts           TIMESTAMPTZ NOT NULL,
  kind         TEXT NOT NULL,
  interface    TEXT,
  device       TEXT,
  channel      INTEGER,
  address      TEXT,
  key          TEXT,
  value_num    DOUBLE PRECISION,
  value_text   TEXT,
  value_bool   BOOLEAN,
  type         TEXT,
  unit         TEXT,
  device_type  TEXT,
  channel_type TEXT,
  device_name  TEXT,
  channel_name TEXT,
  rooms        JSONB,
  functions    JSONB,
  source       TEXT,
  metric_group TEXT,
  metric_name  TEXT,
  labels       JSONB,
  stream       TEXT,
  log_unit     TEXT,
  priority     TEXT,
  message      TEXT,
  fields       JSONB
);
CREATE INDEX IF NOT EXISTS homematic_ts ON homematic (ts);
CREATE INDEX IF NOT EXISTS homematic_address_key_ts ON homematic (address, key, ts)`

const sqlWideInsertMy = `INSERT INTO homematic (ts, kind, interface, device, channel, address, ` + "`key`" + `, value_num, value_text, value_bool, type, unit,
  device_type, channel_type, device_name, channel_name, rooms, functions, source,
  metric_group, metric_name, labels, stream, log_unit, priority, message, fields)
VALUES (:ts, :kind, :interface, :device, :channel, :address, :key, :value_num, :value_text, :value_bool, :type, :unit,
  :device_type, :channel_type, :device_name, :channel_name, :rooms, :functions, :source,
  :group, :name, :labels, :stream, :log_unit, :priority, :message, :fields)`

const sqlWideSchemaMy = `CREATE TABLE IF NOT EXISTS homematic (
  id           BIGINT AUTO_INCREMENT PRIMARY KEY,
  ts           DATETIME(6) NOT NULL,
  kind         VARCHAR(8) NOT NULL,
  interface    VARCHAR(32),
  device       VARCHAR(64),
  channel      INT,
  address      VARCHAR(80),
  ` + "`key`" + `          VARCHAR(64),
  value_num    DOUBLE,
  value_text   TEXT,
  value_bool   BOOLEAN,
  type         VARCHAR(16),
  unit         VARCHAR(32),
  device_type  VARCHAR(64),
  channel_type VARCHAR(64),
  device_name  VARCHAR(255),
  channel_name VARCHAR(255),
  rooms        JSON,
  functions    JSON,
  source       VARCHAR(16),
  metric_group VARCHAR(64),
  metric_name  VARCHAR(128),
  labels       JSON,
  stream       VARCHAR(32),
  log_unit     VARCHAR(128),
  priority     VARCHAR(16),
  message      TEXT,
  fields       JSON,
  INDEX homematic_ts (ts),
  INDEX homematic_address_key_ts (address, ` + "`key`" + `, ts)
) CHARACTER SET utf8mb4`

const sqlEventsInsertPG = `INSERT INTO homematic_events (ts, interface, address, key, value_num, value_text, unit, device_name, channel_name, room)
VALUES (:ts, :interface, :address, :key, :value_num, :value_text, :unit, :device_name, :channel_name, :rooms::jsonb ->> 0)`

const sqlEventsSchemaPG = `CREATE TABLE IF NOT EXISTS homematic_events (
  ts           TIMESTAMPTZ NOT NULL,
  interface    TEXT NOT NULL,
  address      TEXT NOT NULL,
  key          TEXT NOT NULL,
  value_num    DOUBLE PRECISION,
  value_text   TEXT,
  unit         TEXT,
  device_name  TEXT,
  channel_name TEXT,
  room         TEXT
);
CREATE INDEX IF NOT EXISTS homematic_events_address_key_ts ON homematic_events (address, key, ts)`

const sqlEventsInsertMy = `INSERT INTO homematic_events (ts, interface, address, ` + "`key`" + `, value_num, value_text, unit, device_name, channel_name, room)
VALUES (:ts, :interface, :address, :key, :value_num, :value_text, :unit, :device_name, :channel_name, JSON_UNQUOTE(JSON_EXTRACT(:rooms, '$[0]')))`

const sqlEventsSchemaMy = `CREATE TABLE IF NOT EXISTS homematic_events (
  id           BIGINT AUTO_INCREMENT PRIMARY KEY,
  ts           DATETIME(6) NOT NULL,
  interface    VARCHAR(32) NOT NULL,
  address      VARCHAR(80) NOT NULL,
  ` + "`key`" + `          VARCHAR(64) NOT NULL,
  value_num    DOUBLE,
  value_text   TEXT,
  unit         VARCHAR(32),
  device_name  VARCHAR(255),
  channel_name VARCHAR(255),
  room         VARCHAR(255),
  INDEX homematic_events_address_key_ts (address, ` + "`key`" + `, ts)
) CHARACTER SET utf8mb4`

// SQLPresets are the statement sets an SQL target starts from: "wide" (the default) is one
// table with a column for every field of the record, "events" a narrow table of the events'
// values, "timescale" the wide table as a TimescaleDB hypertable.
var SQLPresets = map[string]SQLPreset{
	"wide": {
		Postgres: SQLStatements{Insert: sqlWideInsertPG, Schema: sqlWideSchemaPG},
		MySQL:    SQLStatements{Insert: sqlWideInsertMy, Schema: sqlWideSchemaMy},
	},
	"events": {
		Postgres: SQLStatements{Insert: sqlEventsInsertPG, Schema: sqlEventsSchemaPG},
		MySQL:    SQLStatements{Insert: sqlEventsInsertMy, Schema: sqlEventsSchemaMy},
	},
	"timescale": {
		Postgres:      SQLStatements{Insert: sqlWideInsertPG, Schema: "CREATE EXTENSION IF NOT EXISTS timescaledb;\n" + sqlWideSchemaPG + ";\nSELECT create_hypertable('homematic', 'ts', if_not_exists => TRUE)"},
		TimescaleOnly: true,
	},
}

// SQLPresetNames lists the SQL presets in the UI's order.
var SQLPresetNames = []string{"wide", "events", "timescale"}

// SyslogPresets are the template sets a syslog target starts from: "structured" (the default)
// carries the record's fields as structured data and a short message, "plain" a message line
// without structured data.
var SyslogPresets = map[string]Syslog{
	"structured": {
		Port: 514, Transport: "udp", Framing: "octet", Facility: 16, AppName: "homematic", MsgID: "{{ kind }}",
		SDID: "homematic@32473",
		SDParams: []NamedTemplate{
			{"interface", "{{ interface }}"}, {"address", "{{ address }}"}, {"key", "{{ key }}"}, {"value", "{% if kind == 'event' %}{{ value }}{% endif %}"}, {"unit", "{{ unit }}"},
			{"device_name", "{{ device_name }}"}, {"channel_name", "{{ channel_name }}"}, {"room", "{{ rooms | join(',') }}"},
			{"group", "{{ group }}"}, {"name", "{{ name }}"}, {"stream", "{{ stream }}"}, {"unit_name", "{{ log_unit }}"}, {"*", ""},
		},
		Message: "{% if kind == 'event' %}{{ channel_name | default(address) }} {{ key }} = {{ value }}{{ unit }}{% elif kind == 'metric' %}{{ group }} {{ name }} = {{ value_num }}{% for k, v in labels.items() %} {{ k }}={{ v }}{% endfor %}{% else %}{{ message }}{% endif %}",
		Preset:  "structured",
	},
	"plain": {
		Port: 514, Transport: "udp", Framing: "octet", Facility: 16, AppName: "homematic", MsgID: "{{ kind }}",
		SDParams: []NamedTemplate{},
		Message:  "{% if kind == 'event' %}{{ interface }}/{{ address }} {{ key }}={{ value }} ({{ channel_name }}){% elif kind == 'metric' %}{{ group }}.{{ name }}={{ value_num }}{% for k, v in labels.items() %} {{ k }}={{ v }}{% endfor %}{% else %}{{ log_unit }}: {{ message }}{% endif %}",
		Preset:   "plain",
	},
}

// SyslogPresetNames lists the syslog presets in the UI's order.
var SyslogPresetNames = []string{"structured", "plain"}

// GELFPresets are the template sets a GELF target starts from: "record" (the default) sends
// every field of the record as an additional field, "compact" a chosen few.
var GELFPresets = map[string]GELF{
	"record": {
		Port: 12201, Transport: "udp", Compression: "gzip",
		ShortMessage: "{% if kind == 'event' %}{{ channel_name | default(address) }} {{ key }} = {{ value }}{{ unit }}{% elif kind == 'metric' %}{{ group }} {{ name }} = {{ value_num }}{% else %}{{ message }}{% endif %}",
		Fields:       []NamedTemplate{{"*", ""}}, AllFields: true, Preset: "record",
	},
	"compact": {
		Port: 12201, Transport: "udp", Compression: "gzip",
		ShortMessage: "{% if kind == 'event' %}{{ channel_name | default(address) }} {{ key }} = {{ value }}{{ unit }}{% elif kind == 'metric' %}{{ group }} {{ name }} = {{ value_num }}{% else %}{{ message }}{% endif %}",
		Fields: []NamedTemplate{
			{"kind", "{{ kind }}"}, {"interface", "{{ interface }}"}, {"address", "{{ address }}"}, {"key", "{{ key }}"}, {"value", "{% if kind == 'event' %}{{ value }}{% endif %}"},
			{"device_name", "{{ device_name }}"}, {"room", "{{ rooms | join(',') }}"}, {"group", "{{ group }}"}, {"name", "{{ name }}"}, {"stream", "{{ stream }}"}, {"unit_name", "{{ log_unit }}"}, {"*", ""},
		},
		Preset: "compact",
	},
}

// GELFPresetNames lists the GELF presets in the UI's order.
var GELFPresetNames = []string{"record", "compact"}

// LokiPresets are the template sets a Loki target starts from: "logfmt" (the default) writes
// key=value lines under few labels, "json" the whole record as a JSON line.
var LokiPresets = map[string]Loki{
	"logfmt": {
		Auth: "none", Gzip: true,
		Labels:   []NamedTemplate{{"job", "homematic"}, {"kind", "{{ kind }}"}, {"interface", "{{ interface }}"}, {"group", "{{ group }}"}, {"stream", "{{ stream }}"}, {"unit", "{{ log_unit }}"}, {"priority", "{{ priority }}"}},
		Line:     `{% if kind == 'event' %}address={{ address }} key={{ key }} value={{ value | tojson }} type={{ type }}{% if unit %} unit={{ unit | tojson }}{% endif %}{% if device_name %} device_name={{ device_name | tojson }}{% endif %}{% if channel_name %} channel_name={{ channel_name | tojson }}{% endif %}{% if device_type %} device_type={{ device_type }}{% endif %}{% if rooms %} room={{ rooms | join(',') | tojson }}{% endif %}{% if functions %} function={{ functions | join(',') | tojson }}{% endif %} source={{ source }}{% elif kind == 'metric' %}group={{ group }} name={{ name }} value={{ value_num }}{% for k, v in labels.items() %} {{ k }}={{ v | tojson }}{% endfor %}{% else %}{{ message }}{% endif %}`,
		Metadata: []NamedTemplate{{"*", ""}},
		Preset:   "logfmt",
	},
	"json": {
		Auth: "none", Gzip: true,
		Labels:   []NamedTemplate{{"job", "homematic"}, {"kind", "{{ kind }}"}, {"interface", "{{ interface }}"}, {"stream", "{{ stream }}"}},
		Line:     "{{ record | tojson }}",
		Metadata: []NamedTemplate{},
		Preset:   "json",
	},
}

// LokiPresetNames lists the Loki presets in the UI's order.
var LokiPresetNames = []string{"logfmt", "json"}
