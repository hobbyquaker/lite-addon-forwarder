package tmpl

import (
	"strings"
	"testing"
	"time"
)

func ctx() map[string]any {
	return map[string]any{"device_name": "Heizung Bad", "rooms": []string{"Bad", "EG"}, "value": 21.4, "key": "ACTUAL_TEMPERATURE", "ts": time.Date(2026, 10, 29, 14, 3, 12, 481000000, time.UTC), "value_bool": true, "record": map[string]any{"a": 1}}
}

func TestRender(t *testing.T) {
	tests := []struct{ src, want string }{
		{`{{ device_name | lower }}`, "heizung bad"},
		{`{{ device_name | slug }}`, "heizung_bad"},
		{`{{ "Küche Süd:1" | slug }}`, "kueche_sued_1"},
		{`{{ rooms | join(",") }}`, "Bad,EG"},
		{`{% if value > 20 %}warm{% else %}cold{% endif %}`, "warm"},
		{`{{ missing | default("none") }}`, "none"},
		{`{{ ts | date("2006-01-02") }}`, "2026-10-29"},
		{`{{ ts | unix }}`, "1793282592"},
		{`{{ ts | unix_ms }}`, "1793282592481"},
		{`{{ ts | iso }}`, "2026-10-29T14:03:12.481Z"},
		{`{{ ts | date("15:04", "Europe/Berlin") }}`, "15:03"},
		{`{{ record | tojson }}`, `{"a":1}`},
		{`literal`, "literal"},
		{``, ""},
		{"  {{ key }}\n", "ACTUAL_TEMPERATURE"},
		{`{{ value_bool | int }}`, "1"},
		{`{{ range(5) | length }}`, "5"},
		{`{{ range(1000000) | length }}`, "10000"},
	}
	for _, tt := range tests {
		got, err := Render(tt.src, ctx())
		if err != nil {
			t.Errorf("%s: %v", tt.src, err)
			continue
		}
		if got != tt.want {
			t.Errorf("%s: got %q want %q", tt.src, got, tt.want)
		}
	}
}

func TestSandbox(t *testing.T) {
	for _, src := range []string{`{% include "x" %}`, `{% import "x" as y %}`, `{% extends "x" %}`, `{% from "x" import y %}`} {
		if _, err := Compile(src); err == nil {
			t.Errorf("%s compiled", src)
		}
	}
	if _, err := Render(`{% for i in range(10000) %}0123456789{% endfor %}`, ctx()); err != ErrOutput {
		t.Errorf("output limit: %v", err)
	}
	if _, err := Compile(strings.Repeat("x", MaxSource+1)); err == nil {
		t.Error("source limit")
	}
	if _, err := Compile(`{{ value `); err == nil {
		t.Error("syntax error accepted")
	}
	if _, err := Render(`{{ value | nosuchfilter }}`, ctx()); err == nil {
		t.Error("unknown filter accepted")
	}
}
