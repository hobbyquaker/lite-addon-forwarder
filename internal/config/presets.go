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
