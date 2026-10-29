// Package record is the forwarder's normalised record: one shape for a Homematic datapoint
// event, a system metric and a log line, which every target maps to its own format and every
// selection matches against. The template context (Context) exposes the same fields to the
// Jinja-like templates, plus a few derived ones.
package record

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hobbyquaker/lite-addon-forwarder/internal/selection"
)

// The kinds.
const (
	KindEvent  = "event"
	KindMetric = "metric"
	KindLog    = "log"
)

// Kinds lists every kind, in the order the UI shows them.
var Kinds = []string{KindEvent, KindMetric, KindLog}

// Record is one normalised record.
type Record struct {
	TS   time.Time `json:"ts"`
	Kind string    `json:"kind"`

	// an event: the datapoint and its value
	Interface   string   `json:"interface,omitempty"`
	Device      string   `json:"device,omitempty"`
	Channel     int      `json:"channel"`
	Address     string   `json:"address,omitempty"`
	Key         string   `json:"key,omitempty"`
	Value       any      `json:"value"`
	ValueNum    *float64 `json:"value_num,omitempty"`
	ValueText   string   `json:"value_text,omitempty"`
	Type        string   `json:"type,omitempty"`
	Unit        string   `json:"unit,omitempty"`
	DeviceType  string   `json:"device_type,omitempty"`
	ChannelType string   `json:"channel_type,omitempty"`
	DeviceName  string   `json:"device_name,omitempty"`
	ChannelName string   `json:"channel_name,omitempty"`
	Rooms       []string `json:"rooms"`
	Functions   []string `json:"functions"`
	// Source is event, snapshot or sweep: where the value came from.
	Source string `json:"source,omitempty"`

	// a metric: its group, name, labels; Value and ValueNum carry the number
	Group  string            `json:"group,omitempty"`
	Name   string            `json:"name,omitempty"`
	Labels map[string]string `json:"labels,omitempty"`

	// a log line: the stream it came from, the unit, the priority and the message
	Stream   string            `json:"stream,omitempty"`
	LogUnit  string            `json:"log_unit,omitempty"`
	Priority string            `json:"priority,omitempty"`
	Message  string            `json:"message,omitempty"`
	Fields   map[string]string `json:"fields,omitempty"`
}

// Event builds an event record from a value; typ is the datapoint's TYPE (FLOAT, INTEGER, BOOL,
// ENUM, STRING, ACTION), "" when unknown - then the JSON value's own type decides.
func Event(ts time.Time, iface, address, key string, value json.RawMessage, typ string, valueList []string) *Record {
	r := &Record{TS: ts, Kind: KindEvent, Interface: iface, Address: address, Key: key, Type: typ, Rooms: []string{}, Functions: []string{}}
	r.Device, r.Channel = SplitAddress(address)
	r.SetValue(value, valueList)
	return r
}

// SplitAddress splits "ABC123:1" into the device and the channel index, -1 for a device address.
func SplitAddress(address string) (string, int) {
	if i := strings.LastIndexByte(address, ':'); i >= 0 {
		if n, err := strconv.Atoi(address[i+1:]); err == nil {
			return address[:i], n
		}
	}
	return address, -1
}

// SetValue decodes the JSON value by the record's Type: Value keeps the native value (a float64,
// an int64, a bool or a string), ValueNum the number a numeric store wants (bools as 0/1, enums
// as their index), ValueText the text (a string value, or an enum's text).
func (r *Record) SetValue(raw json.RawMessage, valueList []string) {
	r.Value, r.ValueNum, r.ValueText = nil, nil, ""
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		r.Value = string(raw)
		r.ValueText = string(raw)
		return
	}
	switch x := v.(type) {
	case bool:
		r.Value = x
		n := 0.0
		if x {
			n = 1
		}
		r.ValueNum = &n
		if r.Type == "" {
			r.Type = "BOOL"
		}
	case float64:
		n := x
		r.ValueNum = &n
		switch r.Type {
		case "INTEGER", "ENUM":
			r.Value = int64(x)
		case "":
			if x == float64(int64(x)) && !strings.ContainsAny(string(raw), ".eE") {
				r.Value = int64(x)
				r.Type = "INTEGER"
			} else {
				r.Value = x
				r.Type = "FLOAT"
			}
		default:
			r.Value = x
		}
		if r.Type == "ENUM" {
			if i := int(x); i >= 0 && i < len(valueList) {
				r.ValueText = valueList[i]
			}
		}
	case string:
		r.Value = x
		r.ValueText = x
		if r.Type == "" {
			r.Type = "STRING"
		}
		if n, err := strconv.ParseFloat(x, 64); err == nil && r.Type != "STRING" {
			r.ValueNum = &n
		}
	case nil:
		r.Value = nil
	default:
		r.Value = x
		r.ValueText = string(raw)
	}
}

// Metric builds a metric record.
func Metric(ts time.Time, group, name string, value float64, labels map[string]string) *Record {
	v := value
	if labels == nil {
		labels = map[string]string{}
	}
	return &Record{TS: ts, Kind: KindMetric, Group: group, Name: name, Value: value, ValueNum: &v, Type: "FLOAT", Labels: labels, Rooms: []string{}, Functions: []string{}, Channel: -1}
}

// Log builds a log record.
func Log(ts time.Time, stream, unit, priority, message string, fields map[string]string) *Record {
	if fields == nil {
		fields = map[string]string{}
	}
	return &Record{TS: ts, Kind: KindLog, Stream: stream, LogUnit: unit, Priority: priority, Message: message, Value: message, ValueText: message, Type: "STRING", Fields: fields, Rooms: []string{}, Functions: []string{}, Channel: -1}
}

// Context is the record as the templates see it: every field by its JSON name, the time in
// several shapes, and `record` as the whole document for `{{ record | tojson }}`.
func (r *Record) Context() map[string]any {
	var num any
	if r.ValueNum != nil {
		num = *r.ValueNum
	}
	vb, isBool := r.Value.(bool)
	ctx := map[string]any{
		"ts":           r.TS,
		"ts_rfc3339":   r.TS.UTC().Format(time.RFC3339Nano),
		"ts_unix":      r.TS.Unix(),
		"ts_ms":        r.TS.UnixMilli(),
		"ts_ns":        r.TS.UnixNano(),
		"kind":         r.Kind,
		"interface":    r.Interface,
		"device":       r.Device,
		"channel":      r.Channel,
		"address":      r.Address,
		"key":          r.Key,
		"value":        r.Value,
		"value_num":    num,
		"value_text":   r.ValueText,
		"value_bool":   isBool && vb,
		"type":         r.Type,
		"unit":         r.Unit,
		"device_type":  r.DeviceType,
		"channel_type": r.ChannelType,
		"device_name":  r.DeviceName,
		"channel_name": r.ChannelName,
		"rooms":        r.Rooms,
		"functions":    r.Functions,
		"source":       r.Source,
		"group":        r.Group,
		"name":         r.Name,
		"labels":       r.Labels,
		"stream":       r.Stream,
		"log_unit":     r.LogUnit,
		"priority":     r.Priority,
		"message":      r.Message,
		"fields":       r.Fields,
	}
	if r.Labels == nil {
		ctx["labels"] = map[string]string{}
	}
	if r.Fields == nil {
		ctx["fields"] = map[string]string{}
	}
	ctx["record"] = r.Map()
	return ctx
}

// Subject is the record as a selection matches it.
func (r *Record) Subject() selection.Subject {
	return selection.Subject{Kind: r.Kind, Interface: r.Interface, Device: r.Device, Address: r.Address, Key: r.Key, DeviceType: r.DeviceType, ChannelType: r.ChannelType, Rooms: r.Rooms, Functions: r.Functions, Source: r.Source, MetricGroup: r.Group, LogStream: r.Stream, LogUnit: r.LogUnit, LogPriority: r.Priority}
}

// Map is the record as a plain JSON document (what the webhook's default body sends).
func (r *Record) Map() map[string]any {
	b, _ := json.Marshal(r)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

// Summary is one line for the UI's previews: what the record is.
func (r *Record) Summary() string {
	switch r.Kind {
	case KindMetric:
		return fmt.Sprintf("%s %s=%v", r.Group, r.Name, r.Value)
	case KindLog:
		return fmt.Sprintf("%s %s: %s", r.Stream, r.LogUnit, r.Message)
	default:
		return fmt.Sprintf("%s %s %s=%v", r.Interface, r.Address, r.Key, r.Value)
	}
}

// Variables documents the template context for the UI: name, type and meaning per variable, in
// German and English.
type Variable struct {
	Name string            `json:"name"`
	Type string            `json:"type"`
	Doc  map[string]string `json:"doc"`
}

// Variables is the documented template context.
var Variables = []Variable{
	{"ts", "time", d("Zeitstempel (Zeitobjekt; Filter: date, unix, unix_ms, iso)", "timestamp (time object; filters: date, unix, unix_ms, iso)")},
	{"ts_rfc3339", "string", d("Zeitstempel als RFC 3339 in UTC", "timestamp as RFC 3339 in UTC")},
	{"ts_unix", "integer", d("Sekunden seit 1970", "seconds since 1970")},
	{"ts_ms", "integer", d("Millisekunden seit 1970", "milliseconds since 1970")},
	{"ts_ns", "integer", d("Nanosekunden seit 1970", "nanoseconds since 1970")},
	{"kind", "string", d("event, metric oder log", "event, metric or log")},
	{"interface", "string", d("Schnittstelle (HmIP-RF, BidCos-RF, …)", "interface (HmIP-RF, BidCos-RF, …)")},
	{"device", "string", d("Geräteadresse", "device address")},
	{"channel", "integer", d("Kanalnummer, -1 für das Gerät", "channel index, -1 for the device")},
	{"address", "string", d("Kanaladresse (Gerät:Kanal)", "channel address (device:channel)")},
	{"key", "string", d("Datenpunkt (ACTUAL_TEMPERATURE, STATE, …)", "datapoint (ACTUAL_TEMPERATURE, STATE, …)")},
	{"value", "any", d("der Wert in seinem Typ (Zahl, Wahrheitswert, Text)", "the value in its own type (number, boolean, string)")},
	{"value_num", "number", d("der Wert als Zahl (Wahrheitswerte 0/1, Aufzählungen ihr Index)", "the value as a number (booleans 0/1, enums their index)")},
	{"value_text", "string", d("der Wert als Text (Texte, Aufzählungstext)", "the value as text (strings, the enum's text)")},
	{"value_bool", "boolean", d("wahr für einen wahren Wahrheitswert", "true for a true boolean")},
	{"type", "string", d("FLOAT, INTEGER, BOOL, ENUM, STRING, ACTION", "FLOAT, INTEGER, BOOL, ENUM, STRING, ACTION")},
	{"unit", "string", d("Einheit des Datenpunkts", "the datapoint's unit")},
	{"device_type", "string", d("Gerätetyp (HmIP-eTRV-2, …)", "device type (HmIP-eTRV-2, …)")},
	{"channel_type", "string", d("Kanaltyp", "channel type")},
	{"device_name", "string", d("Gerätename", "device name")},
	{"channel_name", "string", d("Kanalname", "channel name")},
	{"rooms", "list", d("Räume des Kanals (Liste)", "the channel's rooms (list)")},
	{"functions", "list", d("Gewerke des Kanals (Liste)", "the channel's functions (list)")},
	{"source", "string", d("event, snapshot oder sweep", "event, snapshot or sweep")},
	{"group", "string", d("Metrik: die Gruppe", "metric: the group")},
	{"name", "string", d("Metrik: der Name", "metric: the name")},
	{"labels", "dict", d("Metrik: die Labels", "metric: the labels")},
	{"stream", "string", d("Protokoll: der Strom (journal, audit)", "log: the stream (journal, audit)")},
	{"log_unit", "string", d("Protokoll: die Unit", "log: the unit")},
	{"priority", "string", d("Protokoll: die Priorität", "log: the priority")},
	{"message", "string", d("Protokoll: die Nachricht", "log: the message")},
	{"fields", "dict", d("Protokoll: weitere Felder", "log: further fields")},
	{"record", "dict", d("der ganze Datensatz ({{ record | tojson }})", "the whole record ({{ record | tojson }})")},
}

func d(de, en string) map[string]string { return map[string]string{"de": de, "en": en} }
