// Package config holds the forwarder's configuration: the settings, the selections and the
// targets in etc/forwarder.json, and the targets' secrets (passwords, tokens) in
// etc/secrets.json with mode 0600. Writes are atomic (a temp file and a rename); the secrets
// never leave this package in a form the API would answer - a target carries only which of its
// secrets are set.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/hobbyquaker/lite-addon-forwarder/internal/selection"
)

// Format is the configuration file's format version.
const Format = 1

// Settings are the general settings.
type Settings struct {
	// Snapshot forwards the current value of every datapoint at start (GET /state) and after
	// a resync, as records of source "snapshot".
	Snapshot bool `json:"snapshot"`
	// Recent is how many recent records are kept for previews.
	Recent int `json:"recent"`
	// Language of the room and function names in the records (de, en).
	Lang string `json:"lang"`
}

// DefaultSettings are the defaults of a fresh installation.
func DefaultSettings() Settings { return Settings{Snapshot: true, Recent: 500, Lang: "de"} }

// Batch is a target's batching.
type Batch struct {
	Size       int `json:"size"`
	IntervalMS int `json:"interval_ms"`
	MaxBytes   int `json:"max_bytes"`
}

// Queue is a target's RAM queue.
type Queue struct {
	MaxRecords int `json:"max_records"`
}

// Retry is a target's backoff.
type Retry struct {
	MinMS int `json:"min_ms"`
	MaxMS int `json:"max_ms"`
}

// TLS is a target's TLS configuration.
type TLS struct {
	// Insecure skips the certificate check.
	Insecure bool `json:"insecure"`
	// CA is a PEM bundle of the certificate authorities to trust instead of the system's.
	CA string `json:"ca"`
	// ServerName overrides the name the certificate is checked against.
	ServerName string `json:"server_name"`
}

// NamedTemplate is a tag: a name and a value, both templates.
type NamedTemplate struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// FieldTemplate is a field: name and value templates and the type the value is written as
// (auto, float, integer, boolean, string).
type FieldTemplate struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Type  string `json:"type"`
}

// InfluxTemplates shape the line protocol of one record kind.
type InfluxTemplates struct {
	Measurement string          `json:"measurement"`
	Tags        []NamedTemplate `json:"tags"`
	Fields      []FieldTemplate `json:"fields"`
}

// Influx is an InfluxDB target (line protocol over HTTP).
type Influx struct {
	// Version is 1 (/write with db, rp, user and password) or 2 (/api/v2/write with org, bucket
	// and token; InfluxDB 3 speaks this too).
	Version         string `json:"version"`
	URL             string `json:"url"`
	Database        string `json:"database"`
	RetentionPolicy string `json:"retention_policy"`
	User            string `json:"user"`
	Org             string `json:"org"`
	Bucket          string `json:"bucket"`
	Gzip            bool   `json:"gzip"`
	// Preset names the preset the templates started from (clean, one_measurement, per_device,
	// custom); informational.
	Preset string `json:"preset"`
	// Event, Metric and Log are the templates per record kind.
	Event  InfluxTemplates `json:"event"`
	Metric InfluxTemplates `json:"metric"`
	Log    InfluxTemplates `json:"log"`
}

// Webhook is an HTTP target.
type Webhook struct {
	// URL is a template rendered per record; a batch is grouped by the rendered URL.
	URL    string `json:"url"`
	Method string `json:"method"`
	// Format is json (an array of bodies) or ndjson (one body per line).
	Format string `json:"format"`
	// Body is the template of one record's body; the default is the record as JSON.
	Body        string            `json:"body"`
	ContentType string            `json:"content_type"`
	Headers     map[string]string `json:"headers"`
	// Auth is none, basic (User and the secret password) or bearer (the secret token).
	Auth string `json:"auth"`
	User string `json:"user"`
	Gzip bool   `json:"gzip"`
}

// Target is one target.
type Target struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Enabled   bool   `json:"enabled"`
	Selection string `json:"selection"`
	Batch     Batch  `json:"batch"`
	Queue     Queue  `json:"queue"`
	Retry     Retry  `json:"retry"`
	TLS       TLS    `json:"tls"`
	// Timeout of one send in seconds.
	TimeoutS int `json:"timeout_s"`

	Influx  *Influx  `json:"influx,omitempty"`
	Webhook *Webhook `json:"webhook,omitempty"`

	// SecretsSet says which secrets are stored (password, token); read-only in the API.
	SecretsSet map[string]bool `json:"secrets"`

	// SetSecrets and ClearSecrets are the API's way to change secrets; never stored.
	SetSecrets   map[string]string `json:"set_secrets,omitempty"`
	ClearSecrets []string          `json:"clear_secrets,omitempty"`

	// Secrets are the stored secrets, for the target implementation; never serialised.
	Secrets map[string]string `json:"-"`
}

// Types lists the target types this build has.
var Types = []string{"influx", "webhook"}

// SecretNames are the secrets a type may hold.
var SecretNames = map[string][]string{
	"influx":  {"password", "token"},
	"webhook": {"password", "token"},
}

// DefaultTarget is a new target's defaults.
func DefaultTarget(typ string) Target {
	t := Target{Type: typ, Enabled: true, Batch: Batch{Size: 500, IntervalMS: 5000, MaxBytes: 1 << 20}, Queue: Queue{MaxRecords: 50000}, Retry: Retry{MinMS: 1000, MaxMS: 60000}, TimeoutS: 15, SecretsSet: map[string]bool{}}
	switch typ {
	case "influx":
		t.Influx = &Influx{Version: "2", Gzip: true, Preset: "clean", Event: InfluxPresets["clean"].Event, Metric: InfluxPresets["clean"].Metric, Log: InfluxPresets["clean"].Log}
	case "webhook":
		t.Webhook = &Webhook{Method: "POST", Format: "json", Body: "{{ record | tojson }}", Headers: map[string]string{}, Auth: "none"}
	}
	return t
}

// Validate checks a target and fills defaults where a field is empty or out of range.
func (t *Target) Validate() error {
	if strings.TrimSpace(t.Name) == "" {
		return errors.New("the target needs a name")
	}
	known := false
	for _, typ := range Types {
		known = known || typ == t.Type
	}
	if !known {
		return fmt.Errorf("unknown target type %q", t.Type)
	}
	d := DefaultTarget(t.Type)
	if t.Batch.Size <= 0 || t.Batch.Size > 10000 {
		t.Batch.Size = d.Batch.Size
	}
	if t.Batch.IntervalMS < 100 || t.Batch.IntervalMS > 3600000 {
		t.Batch.IntervalMS = d.Batch.IntervalMS
	}
	if t.Batch.MaxBytes < 1024 || t.Batch.MaxBytes > 64<<20 {
		t.Batch.MaxBytes = d.Batch.MaxBytes
	}
	if t.Queue.MaxRecords < 100 || t.Queue.MaxRecords > 1000000 {
		t.Queue.MaxRecords = d.Queue.MaxRecords
	}
	if t.Retry.MinMS < 100 {
		t.Retry.MinMS = d.Retry.MinMS
	}
	if t.Retry.MaxMS < t.Retry.MinMS {
		t.Retry.MaxMS = max(d.Retry.MaxMS, t.Retry.MinMS)
	}
	if t.TimeoutS <= 0 || t.TimeoutS > 300 {
		t.TimeoutS = d.TimeoutS
	}
	switch t.Type {
	case "influx":
		if t.Influx == nil {
			return errors.New("influx settings missing")
		}
		t.Webhook = nil
		i := t.Influx
		if i.Version != "1" && i.Version != "2" {
			return errors.New("influx version must be 1 or 2")
		}
		if err := checkURL(i.URL); err != nil {
			return err
		}
		if i.Version == "1" && i.Database == "" {
			return errors.New("the database is missing")
		}
		if i.Version == "2" && (i.Org == "" || i.Bucket == "") {
			return errors.New("org and bucket are missing")
		}
		if i.Event.Measurement == "" {
			return errors.New("the measurement template is empty")
		}
		if len(i.Event.Fields) == 0 {
			return errors.New("at least one field is needed")
		}
		for k, tpls := range map[string]*InfluxTemplates{"event": &i.Event, "metric": &i.Metric, "log": &i.Log} {
			for n := range tpls.Fields {
				switch tpls.Fields[n].Type {
				case "", "auto":
					tpls.Fields[n].Type = "auto"
				case "float", "integer", "boolean", "string":
				default:
					return fmt.Errorf("%s field %q: unknown type %q", k, tpls.Fields[n].Name, tpls.Fields[n].Type)
				}
			}
			if tpls.Tags == nil {
				tpls.Tags = []NamedTemplate{}
			}
			if tpls.Fields == nil {
				tpls.Fields = []FieldTemplate{}
			}
		}
	case "webhook":
		if t.Webhook == nil {
			return errors.New("webhook settings missing")
		}
		t.Influx = nil
		w := t.Webhook
		if strings.TrimSpace(w.URL) == "" {
			return errors.New("the URL is missing")
		}
		if !strings.Contains(w.URL, "{{") {
			if err := checkURL(w.URL); err != nil {
				return err
			}
		}
		switch w.Method {
		case "":
			w.Method = "POST"
		case "POST", "PUT", "PATCH":
		default:
			return fmt.Errorf("method %q is not POST, PUT or PATCH", w.Method)
		}
		switch w.Format {
		case "":
			w.Format = "json"
		case "json", "ndjson":
		default:
			return fmt.Errorf("format %q is not json or ndjson", w.Format)
		}
		switch w.Auth {
		case "":
			w.Auth = "none"
		case "none", "basic", "bearer":
		default:
			return fmt.Errorf("auth %q is not none, basic or bearer", w.Auth)
		}
		if w.Body == "" {
			w.Body = "{{ record | tojson }}"
		}
		if w.Headers == nil {
			w.Headers = map[string]string{}
		}
		for k := range w.Headers {
			if strings.ContainsAny(k, " :\r\n") || strings.ContainsAny(w.Headers[k], "\r\n") {
				return fmt.Errorf("bad header %q", k)
			}
		}
	}
	for name := range t.SetSecrets {
		if !allowedSecret(t.Type, name) {
			return fmt.Errorf("unknown secret %q", name)
		}
	}
	return nil
}

func allowedSecret(typ, name string) bool {
	for _, n := range SecretNames[typ] {
		if n == name {
			return true
		}
	}
	return false
}

func checkURL(s string) error {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("the URL %q is not an http(s) URL", s)
	}
	return nil
}

// File is the configuration file.
type File struct {
	Format     int                   `json:"format"`
	Settings   Settings              `json:"settings"`
	Selections []selection.Selection `json:"selections"`
	Targets    []Target              `json:"targets"`
}

type secretsFile struct {
	Targets map[string]map[string]string `json:"targets"`
}

// Store is the configuration on disk.
type Store struct {
	mu      sync.Mutex
	path    string
	secrets string
	file    File
	sec     secretsFile
	// OnChange is called after every successful write, outside the lock.
	OnChange func()
}

// Open reads etc/forwarder.json and etc/secrets.json below dir, creating defaults when they are
// missing.
func Open(dir string) (*Store, error) {
	etc := filepath.Join(dir, "etc")
	if err := os.MkdirAll(etc, 0o755); err != nil {
		return nil, err
	}
	s := &Store{path: filepath.Join(etc, "forwarder.json"), secrets: filepath.Join(etc, "secrets.json")}
	s.file = File{Format: Format, Settings: DefaultSettings(), Selections: []selection.Selection{}, Targets: []Target{}}
	s.sec.Targets = map[string]map[string]string{}
	if b, err := os.ReadFile(s.path); err == nil {
		if err := json.Unmarshal(b, &s.file); err != nil {
			return nil, fmt.Errorf("%s: %w", s.path, err)
		}
		if s.file.Format > Format {
			return nil, fmt.Errorf("%s: format %d is newer than this version knows", s.path, s.file.Format)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if b, err := os.ReadFile(s.secrets); err == nil {
		if err := json.Unmarshal(b, &s.sec); err != nil {
			return nil, fmt.Errorf("%s: %w", s.secrets, err)
		}
		if s.sec.Targets == nil {
			s.sec.Targets = map[string]map[string]string{}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if s.file.Selections == nil {
		s.file.Selections = []selection.Selection{}
	}
	if s.file.Targets == nil {
		s.file.Targets = []Target{}
	}
	if s.file.Settings.Recent <= 0 {
		s.file.Settings.Recent = DefaultSettings().Recent
	}
	if s.file.Settings.Lang == "" {
		s.file.Settings.Lang = "de"
	}
	for i := range s.file.Targets {
		s.decorate(&s.file.Targets[i])
	}
	return s, nil
}

// decorate fills SecretsSet from the secrets file.
func (s *Store) decorate(t *Target) {
	t.SecretsSet = map[string]bool{}
	for name, v := range s.sec.Targets[t.ID] {
		t.SecretsSet[name] = v != ""
	}
	t.SetSecrets, t.ClearSecrets, t.Secrets = nil, nil, nil
}

// Settings answers the settings.
func (s *Store) Settings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.file.Settings
}

// SetSettings replaces the settings.
func (s *Store) SetSettings(st Settings) error {
	if st.Recent < 10 || st.Recent > 10000 {
		st.Recent = DefaultSettings().Recent
	}
	if st.Lang != "de" && st.Lang != "en" {
		st.Lang = "de"
	}
	s.mu.Lock()
	s.file.Settings = st
	err := s.save()
	s.mu.Unlock()
	s.changed(err)
	return err
}

// Selections answers the selections, sorted by name.
func (s *Store) Selections() []selection.Selection {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]selection.Selection, len(s.file.Selections))
	copy(out, s.file.Selections)
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// Selection answers one selection.
func (s *Store) Selection(id string) (selection.Selection, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sel := range s.file.Selections {
		if sel.ID == id {
			return sel, true
		}
	}
	return selection.Selection{}, false
}

// UpsertSelection creates (empty ID) or replaces a selection.
func (s *Store) UpsertSelection(sel selection.Selection) (selection.Selection, error) {
	if err := sel.Validate(); err != nil {
		return sel, err
	}
	s.mu.Lock()
	if sel.ID == "" {
		sel.ID = newID()
		s.file.Selections = append(s.file.Selections, sel)
	} else {
		found := false
		for i := range s.file.Selections {
			if s.file.Selections[i].ID == sel.ID {
				s.file.Selections[i] = sel
				found = true
			}
		}
		if !found {
			s.mu.Unlock()
			return sel, ErrNotFound
		}
	}
	err := s.save()
	s.mu.Unlock()
	s.changed(err)
	return sel, err
}

// ErrNotFound is answered for an unknown id.
var ErrNotFound = errors.New("not found")

// ErrInUse is answered when a selection a target uses is deleted.
var ErrInUse = errors.New("the selection is used by a target")

// DeleteSelection removes a selection no target uses.
func (s *Store) DeleteSelection(id string) error {
	s.mu.Lock()
	for _, t := range s.file.Targets {
		if t.Selection == id {
			s.mu.Unlock()
			return ErrInUse
		}
	}
	idx := -1
	for i := range s.file.Selections {
		if s.file.Selections[i].ID == id {
			idx = i
		}
	}
	if idx < 0 {
		s.mu.Unlock()
		return ErrNotFound
	}
	s.file.Selections = append(s.file.Selections[:idx], s.file.Selections[idx+1:]...)
	err := s.save()
	s.mu.Unlock()
	s.changed(err)
	return err
}

// Targets answers the targets without their secrets, sorted by name.
func (s *Store) Targets() []Target {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Target, len(s.file.Targets))
	for i, t := range s.file.Targets {
		out[i] = t.clone()
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// TargetsWithSecrets answers every target with its secrets filled, for the pipeline.
func (s *Store) TargetsWithSecrets() []Target {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Target, len(s.file.Targets))
	for i, t := range s.file.Targets {
		out[i] = t.clone()
		out[i].Secrets = map[string]string{}
		for k, v := range s.sec.Targets[t.ID] {
			out[i].Secrets[k] = v
		}
	}
	return out
}

// WithSecrets fills a target's secrets from the store (an unsaved target in a test or a
// preview keeps the secrets of the stored one with its id).
func (s *Store) WithSecrets(t Target) Target {
	s.mu.Lock()
	defer s.mu.Unlock()
	t = t.clone()
	t.Secrets = map[string]string{}
	for k, v := range s.sec.Targets[t.ID] {
		t.Secrets[k] = v
	}
	return t
}

// Target answers one target.
func (s *Store) Target(id string) (Target, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.file.Targets {
		if t.ID == id {
			return t.clone(), true
		}
	}
	return Target{}, false
}

func (t Target) clone() Target {
	b, _ := json.Marshal(t)
	var c Target
	_ = json.Unmarshal(b, &c)
	c.SecretsSet = map[string]bool{}
	for k, v := range t.SecretsSet {
		c.SecretsSet[k] = v
	}
	c.SetSecrets, c.ClearSecrets, c.Secrets = nil, nil, nil
	return c
}

// UpsertTarget creates (empty ID) or replaces a target, applying SetSecrets and ClearSecrets.
func (s *Store) UpsertTarget(t Target) (Target, error) {
	if err := t.Validate(); err != nil {
		return t, err
	}
	s.mu.Lock()
	if t.Selection != "" {
		ok := false
		for _, sel := range s.file.Selections {
			ok = ok || sel.ID == t.Selection
		}
		if !ok {
			s.mu.Unlock()
			return t, fmt.Errorf("unknown selection %q", t.Selection)
		}
	}
	set, clear := t.SetSecrets, t.ClearSecrets
	t.SetSecrets, t.ClearSecrets, t.Secrets = nil, nil, nil
	if t.ID == "" {
		t.ID = newID()
		s.file.Targets = append(s.file.Targets, t)
	} else {
		found := false
		for i := range s.file.Targets {
			if s.file.Targets[i].ID == t.ID {
				s.file.Targets[i] = t
				found = true
			}
		}
		if !found {
			s.mu.Unlock()
			return t, ErrNotFound
		}
	}
	secretsChanged := false
	if len(set)+len(clear) > 0 {
		m := s.sec.Targets[t.ID]
		if m == nil {
			m = map[string]string{}
		}
		for k, v := range set {
			if v != "" {
				m[k] = v
			}
		}
		for _, k := range clear {
			delete(m, k)
		}
		s.sec.Targets[t.ID] = m
		secretsChanged = true
	}
	err := s.save()
	if err == nil && secretsChanged {
		err = s.saveSecrets()
	}
	var out Target
	for i := range s.file.Targets {
		if s.file.Targets[i].ID == t.ID {
			s.decorate(&s.file.Targets[i])
			out = s.file.Targets[i].clone()
		}
	}
	s.mu.Unlock()
	s.changed(err)
	return out, err
}

// SetEnabled switches a target on or off.
func (s *Store) SetEnabled(id string, enabled bool) (Target, error) {
	s.mu.Lock()
	var out Target
	found := false
	for i := range s.file.Targets {
		if s.file.Targets[i].ID == id {
			s.file.Targets[i].Enabled = enabled
			out = s.file.Targets[i].clone()
			found = true
		}
	}
	if !found {
		s.mu.Unlock()
		return out, ErrNotFound
	}
	err := s.save()
	s.mu.Unlock()
	s.changed(err)
	return out, err
}

// DeleteTarget removes a target and its secrets.
func (s *Store) DeleteTarget(id string) error {
	s.mu.Lock()
	idx := -1
	for i := range s.file.Targets {
		if s.file.Targets[i].ID == id {
			idx = i
		}
	}
	if idx < 0 {
		s.mu.Unlock()
		return ErrNotFound
	}
	s.file.Targets = append(s.file.Targets[:idx], s.file.Targets[idx+1:]...)
	_, had := s.sec.Targets[id]
	delete(s.sec.Targets, id)
	err := s.save()
	if err == nil && had {
		err = s.saveSecrets()
	}
	s.mu.Unlock()
	s.changed(err)
	return err
}

func (s *Store) changed(err error) {
	if err == nil && s.OnChange != nil {
		s.OnChange()
	}
}

func (s *Store) save() error {
	s.file.Format = Format
	return writeAtomic(s.path, s.file, 0o644)
}

func (s *Store) saveSecrets() error {
	return writeAtomic(s.secrets, s.sec, 0o600)
}

func writeAtomic(path string, v any, mode os.FileMode) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
