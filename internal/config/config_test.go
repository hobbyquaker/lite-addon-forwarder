package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hobbyquaker/lite-addon-forwarder/internal/selection"
)

func TestStore(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	changes := 0
	s.OnChange = func() { changes++ }
	sel, err := s.UpsertSelection(selection.Selection{Name: "all"})
	if err != nil || sel.ID == "" {
		t.Fatal(err)
	}
	tg := DefaultTarget("influx")
	tg.Name, tg.Selection = "db", sel.ID
	tg.Influx.URL = "http://influx:8086"
	tg.Influx.Org, tg.Influx.Bucket = "home", "hm"
	tg.SetSecrets = map[string]string{"token": "t0k"}
	tg, err = s.UpsertTarget(tg)
	if err != nil {
		t.Fatal(err)
	}
	if !tg.SecretsSet["token"] || tg.SetSecrets != nil || tg.Secrets != nil {
		t.Errorf("after upsert: %+v", tg)
	}
	if changes != 2 {
		t.Errorf("changes %d", changes)
	}
	// the secrets file is 0600 and holds the token; the config file does not
	st, err := os.Stat(filepath.Join(dir, "etc", "secrets.json"))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("secrets mode: %v %v", st, err)
	}
	cfg, _ := os.ReadFile(filepath.Join(dir, "etc", "forwarder.json"))
	if strings.Contains(string(cfg), "t0k") {
		t.Error("secret in the config file")
	}
	// reopened: the same, secrets fed to the pipeline only
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ts := s2.Targets()
	if len(ts) != 1 || !ts[0].SecretsSet["token"] || ts[0].Secrets != nil {
		t.Errorf("reopened: %+v", ts)
	}
	if ws := s2.TargetsWithSecrets(); ws[0].Secrets["token"] != "t0k" {
		t.Errorf("with secrets: %+v", ws)
	}
	// an update without set_secrets keeps the secret; clear removes it
	tg.Name = "db2"
	tg, _ = s2.UpsertTarget(tg)
	if !tg.SecretsSet["token"] {
		t.Error("secret lost on update")
	}
	tg.ClearSecrets = []string{"token"}
	tg, _ = s2.UpsertTarget(tg)
	if tg.SecretsSet["token"] {
		t.Error("secret not cleared")
	}
	// delete rules
	if err := s2.DeleteSelection(sel.ID); err != ErrInUse {
		t.Errorf("delete used selection: %v", err)
	}
	if err := s2.DeleteTarget("nope"); err != ErrNotFound {
		t.Errorf("delete unknown: %v", err)
	}
	if _, err := s2.SetEnabled(tg.ID, false); err != nil {
		t.Error(err)
	}
	if err := s2.DeleteTarget(tg.ID); err != nil {
		t.Error(err)
	}
	if err := s2.DeleteSelection(sel.ID); err != nil {
		t.Error(err)
	}
	if _, err := s2.UpsertTarget(Target{ID: "nope", Name: "x", Type: "webhook", Webhook: &Webhook{URL: "http://x"}}); err != ErrNotFound {
		t.Errorf("update unknown: %v", err)
	}
}

func TestValidate(t *testing.T) {
	good := func() Target {
		tg := DefaultTarget("webhook")
		tg.Name = "x"
		tg.Webhook.URL = "https://example.org/hook"
		return tg
	}
	tg := good()
	tg.Batch.Size, tg.Retry.MaxMS, tg.TimeoutS = -1, 1, 0
	if err := tg.Validate(); err != nil || tg.Batch.Size != 500 || tg.Retry.MaxMS != 60000 || tg.TimeoutS != 15 || tg.Influx != nil {
		t.Errorf("defaults: %v %+v", err, tg)
	}
	bad := []func(*Target){
		func(t *Target) { t.Name = "" },
		func(t *Target) { t.Type = "ftp" },
		func(t *Target) { t.Webhook.URL = "ftp://x" },
		func(t *Target) { t.Webhook.Method = "GET" },
		func(t *Target) { t.Webhook.Format = "xml" },
		func(t *Target) { t.Webhook.Auth = "digest" },
		func(t *Target) { t.Webhook.Headers = map[string]string{"X Y": "1"} },
		func(t *Target) { t.SetSecrets = map[string]string{"key": "x"} },
		func(t *Target) { t.Webhook = nil },
	}
	for i, f := range bad {
		tg := good()
		f(&tg)
		if err := tg.Validate(); err == nil {
			t.Errorf("bad %d accepted", i)
		}
	}
	in := DefaultTarget("influx")
	in.Name = "i"
	in.Influx.URL = "http://x"
	if err := in.Validate(); err == nil {
		t.Error("v2 without org/bucket accepted")
	}
	in.Influx.Version = "3"
	if err := in.Validate(); err == nil {
		t.Error("version 3 accepted")
	}
	in.Influx.Version, in.Influx.Org, in.Influx.Bucket = "2", "o", "b"
	in.Influx.Event.Fields[0].Type = "double"
	if err := in.Validate(); err == nil {
		t.Error("bad field type accepted")
	}
	in.Influx.Event.Fields[0].Type = ""
	if err := in.Validate(); err != nil || in.Influx.Event.Fields[0].Type != "auto" || in.Webhook != nil {
		t.Errorf("influx: %v", err)
	}
	for name, p := range InfluxPresets {
		for _, tpls := range []InfluxTemplates{p.Event, p.Metric, p.Log} {
			if tpls.Measurement == "" || len(tpls.Fields) == 0 {
				t.Errorf("preset %s incomplete", name)
			}
		}
	}
}
