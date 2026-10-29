package selection

import (
	"fmt"
	"strings"
	"testing"
)

func ev(iface, address, key string, dtype string, rooms ...string) *Subject {
	dev := address[:strings.IndexByte(address, ':')]
	return &Subject{Kind: KindEvent, Interface: iface, Device: dev, Address: address, Key: key, DeviceType: dtype, ChannelType: map[string]string{"HmIP-eTRV-2": "HEATING_CLIMATECONTROL_TRANSCEIVER", "HM-PB-2-WM55": "KEY"}[dtype], Rooms: rooms, Source: "event"}
}

func TestMatch(t *testing.T) {
	temp := ev("HmIP-RF", "000A:1", "ACTUAL_TEMPERATURE", "HmIP-eTRV-2", "Bad")
	press := ev("BidCos-RF", "ABC:2", "PRESS_SHORT", "HM-PB-2-WM55", "Flur")
	metric := &Subject{Kind: KindMetric, MetricGroup: "cpu"}
	logr := &Subject{Kind: KindLog, LogStream: "journal", LogUnit: "occulited.service", LogPriority: "warning"}
	tests := []struct {
		name string
		sel  Selection
		want map[*Subject]bool
	}{
		{"empty matches all", Selection{}, map[*Subject]bool{temp: true, press: true, metric: true, logr: true}},
		{"kinds", Selection{Kinds: []string{"event"}}, map[*Subject]bool{temp: true, press: true, metric: false, logr: false}},
		{"interface", Selection{Interfaces: []string{"HmIP-RF"}}, map[*Subject]bool{temp: true, press: false, metric: true}},
		{"device type", Selection{DeviceTypes: []string{"HM-PB-2-WM55"}}, map[*Subject]bool{temp: false, press: true}},
		{"device object", Selection{Objects: []string{"HmIP-RF/000A"}}, map[*Subject]bool{temp: true, press: false}},
		{"channel object", Selection{Objects: []string{"BidCos-RF/ABC:2", "BidCos-RF/ABC:1"}}, map[*Subject]bool{temp: false, press: true}},
		{"keys glob", Selection{Keys: []string{"*_TEMPERATURE"}}, map[*Subject]bool{temp: true, press: false}},
		{"exclude", Selection{ExcludeKeys: []string{"PRESS_*"}}, map[*Subject]bool{temp: true, press: false}},
		{"rooms", Selection{Rooms: []string{"Bad", "Küche"}}, map[*Subject]bool{temp: true, press: false}},
		{"and across", Selection{Interfaces: []string{"HmIP-RF"}, Keys: []string{"PRESS_*"}}, map[*Subject]bool{temp: false, press: false}},
		{"channel type", Selection{ChannelTypes: []string{"KEY"}}, map[*Subject]bool{temp: false, press: true}},
		{"exclude device", Selection{ExcludeObjects: []string{"HmIP-RF/000A"}}, map[*Subject]bool{temp: false, press: true}},
		{"exclude channel wins over tick", Selection{Objects: []string{"BidCos-RF/ABC"}, ExcludeObjects: []string{"BidCos-RF/ABC:2"}}, map[*Subject]bool{temp: false, press: false}},
		{"sources", Selection{Sources: []string{"snapshot"}}, map[*Subject]bool{temp: false}},
		{"metric groups", Selection{MetricGroups: []string{"memory"}}, map[*Subject]bool{metric: false, temp: true}},
		{"log priority", Selection{LogPriority: "err"}, map[*Subject]bool{logr: false}},
		{"log priority info", Selection{LogPriority: "info", LogUnits: []string{"occulited.service"}}, map[*Subject]bool{logr: true}},
		{"log stream", Selection{LogStreams: []string{"audit"}}, map[*Subject]bool{logr: false}},
	}
	for _, tt := range tests {
		m := New(tt.sel)
		for r, want := range tt.want {
			if got := m.Match(*r); got != want {
				t.Errorf("%s: %s: got %v want %v", tt.name, fmt.Sprint(*r), got, want)
			}
		}
	}
}

func TestValidate(t *testing.T) {
	s := Selection{Name: " x ", Keys: []string{" A ", "A", ""}, Kinds: []string{"event"}}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(s.Keys) != 1 || s.Objects == nil {
		t.Errorf("normalise: %+v", s)
	}
	for _, bad := range []Selection{{}, {Name: "x", Kinds: []string{"foo"}}, {Name: "x", Keys: []string{"["}}, {Name: "x", LogPriority: "loud"}} {
		if err := bad.Validate(); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}
