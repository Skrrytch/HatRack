package core

import (
	"strings"
	"testing"
	"time"
)

func TestObserveRetention(t *testing.T) {
	tests := []struct {
		days int
		want time.Duration
	}{
		{0, 28 * 24 * time.Hour},
		{7, 7 * 24 * time.Hour},
		{365, 365 * 24 * time.Hour},
	}
	for _, tc := range tests {
		o := Observe{RetentionDays: tc.days}
		if got := o.Retention(); got != tc.want {
			t.Errorf("RetentionDays %d: Retention() = %v, want %v", tc.days, got, tc.want)
		}
	}
}

func TestObserveEnabled(t *testing.T) {
	tests := []struct {
		name   string
		obs    Observe
		device string
		sensor string
		want   bool
	}{
		{"empty idle on", Observe{}, "laptop", "idle", true},
		{"empty window off", Observe{}, "laptop", "window", false},
		{"window on for laptop", Observe{Devices: map[string]map[string]bool{"laptop": {"window": true}}}, "laptop", "window", true},
		{"window off for other device", Observe{Devices: map[string]map[string]bool{"laptop": {"window": true}}}, "buero", "window", false},
		{"idle unset stays on", Observe{Devices: map[string]map[string]bool{"laptop": {"window": true}}}, "laptop", "idle", true},
		{"idle switched off", Observe{Devices: map[string]map[string]bool{"laptop": {"idle": false}}}, "laptop", "idle", false},
		{"idle on other device", Observe{Devices: map[string]map[string]bool{"laptop": {"idle": false}}}, "buero", "idle", true},
		{"window switched off", Observe{Devices: map[string]map[string]bool{"laptop": {"window": false}}}, "laptop", "window", false},
	}
	for _, tc := range tests {
		if got := tc.obs.Enabled(tc.device, tc.sensor); got != tc.want {
			t.Errorf("%s: Enabled(%q, %q) = %v, want %v", tc.name, tc.device, tc.sensor, got, tc.want)
		}
	}
}

func TestObserveIgnoredRedacted(t *testing.T) {
	o := Observe{Ignore: []string{"KeePassXC", " signal "}, Redact: []string{"thunderbird"}}
	ignored := []struct {
		app  string
		want bool
	}{
		{"keepassxc", true},
		{"KEEPASSXC", true},
		{"signal", true},
		{" signal", true},
		{"keepass", false},
		{"", false},
	}
	for _, tc := range ignored {
		if got := o.Ignored(tc.app); got != tc.want {
			t.Errorf("Ignored(%q) = %v, want %v", tc.app, got, tc.want)
		}
	}
	redacted := []struct {
		app  string
		want bool
	}{
		{"Thunderbird", true},
		{"keepassxc", false},
	}
	for _, tc := range redacted {
		if got := o.Redacted(tc.app); got != tc.want {
			t.Errorf("Redacted(%q) = %v, want %v", tc.app, got, tc.want)
		}
	}
}

func TestObserveValidate(t *testing.T) {
	tests := []struct {
		name    string
		obs     Observe
		wantErr bool
	}{
		{"retention -1", Observe{RetentionDays: -1}, true},
		{"retention 400", Observe{RetentionDays: 400}, true},
		{"retention 366", Observe{RetentionDays: 366}, true},
		{"retention 0", Observe{RetentionDays: 0}, false},
		{"retention 365", Observe{RetentionDays: 365}, false},
		{"ignore blank", Observe{Ignore: []string{"  "}}, true},
		{"redact empty", Observe{Redact: []string{""}}, true},
		{"device empty name", Observe{Devices: map[string]map[string]bool{"": {"idle": true}}}, true},
		{"sensor upper case", Observe{Devices: map[string]map[string]bool{"laptop": {"Window": true}}}, true},
		{"sensor starts with digit", Observe{Devices: map[string]map[string]bool{"laptop": {"9x": true}}}, true},
		{"valid sensors", Observe{Devices: map[string]map[string]bool{"laptop": {"window": true, "media_cam": true}}}, false},
	}
	for _, tc := range tests {
		err := tc.obs.Validate()
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: Validate() error = %v, wantErr %v", tc.name, err, tc.wantErr)
		}
	}
}

func TestHatObserves(t *testing.T) {
	yes, no := true, false
	tests := []struct {
		name string
		obs  *bool
		want bool
	}{
		{"nil", nil, true},
		{"false", &no, false},
		{"true", &yes, true},
	}
	for _, tc := range tests {
		h := Hat{Observe: tc.obs}
		if got := h.Observes(); got != tc.want {
			t.Errorf("%s: Observes() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

const observeYAML = `version: 1
observe:
  retention_days: 14
  ignore: [keepassxc]
  redact: [thunderbird]
  devices:
    laptop: {window: true}
  grants:
    laptop: {workdir: known_keys}
hats:
  privat:
    activities: []
    observe: false
`

func TestObserveParseConfig(t *testing.T) {
	c, err := ParseConfig([]byte(observeYAML))
	if err != nil {
		t.Fatal(err)
	}
	o := c.Observe
	if o.RetentionDays != 14 {
		t.Errorf("RetentionDays = %d, want 14", o.RetentionDays)
	}
	if len(o.Ignore) != 1 || o.Ignore[0] != "keepassxc" {
		t.Errorf("Ignore = %v, want [keepassxc]", o.Ignore)
	}
	if len(o.Redact) != 1 || o.Redact[0] != "thunderbird" {
		t.Errorf("Redact = %v, want [thunderbird]", o.Redact)
	}
	if !o.Enabled("laptop", "window") {
		t.Errorf("laptop window should be enabled: %+v", o.Devices)
	}
	h, ok := c.Hat("privat")
	if !ok {
		t.Fatal("hat privat not found")
	}
	if h.Observes() {
		t.Errorf("hat privat: Observes() = true, want false")
	}
}

func TestObserveParseConfigRejectsRetention(t *testing.T) {
	y := strings.Replace(observeYAML, "retention_days: 14", "retention_days: 400", 1)
	if _, err := ParseConfig([]byte(y)); err == nil {
		t.Fatal("retention_days 400: expected an error")
	}
}

func TestObserveExportRoundTrip(t *testing.T) {
	c, err := ParseConfig([]byte(observeYAML))
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.Export()
	if err != nil {
		t.Fatal(err)
	}
	c2, err := ParseConfig(out)
	if err != nil {
		t.Fatalf("re-import: %v\n%s", err, out)
	}
	if !sameJSON(c.Observe, c2.Observe) {
		t.Errorf("observe settings changed in round trip:\n%s", out)
	}
	if c2.Observe.Granted("laptop", GrantWorkdir) != LevelKnownKeys {
		t.Errorf("grant lost in round trip:\n%s", out)
	}
	h, _ := c2.Hat("privat")
	if h.Observes() {
		t.Errorf("hat privat observe lost in round trip:\n%s", out)
	}
}

func TestObserveNotExportedWhenUnset(t *testing.T) {
	c, err := ParseConfig([]byte("version: 1\nhats:\n  a:\n    activities: []\n"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.Export()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "observe") {
		t.Errorf("unset observe settings exported:\n%s", out)
	}
}

func TestMergeObserveKeepsCurrentWhenImportEmpty(t *testing.T) {
	cur, err := ParseConfig([]byte("version: 1\nobserve:\n  ignore: [a]\nhats: {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	in, err := ParseConfig([]byte("version: 1\nhats: {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	m, changes := Merge(cur, in, false)
	if len(m.Observe.Ignore) != 1 || m.Observe.Ignore[0] != "a" {
		t.Errorf("Ignore = %v, want [a]", m.Observe.Ignore)
	}
	for _, ch := range changes {
		if strings.Contains(ch, "observe") {
			t.Errorf("unexpected change mentioning observe: %q", ch)
		}
	}
}

func TestMergeObserveReplacesWithImport(t *testing.T) {
	cur, err := ParseConfig([]byte("version: 1\nobserve:\n  ignore: [a]\nhats: {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	in, err := ParseConfig([]byte("version: 1\nobserve:\n  ignore: [b]\nhats: {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	m, changes := Merge(cur, in, false)
	if len(m.Observe.Ignore) != 1 || m.Observe.Ignore[0] != "b" {
		t.Errorf("Ignore = %v, want [b]", m.Observe.Ignore)
	}
	found := false
	for _, ch := range changes {
		if strings.Contains(ch, "observe settings") {
			found = true
		}
	}
	if !found {
		t.Errorf("changes = %v, want one mentioning observe settings", changes)
	}
}

func TestMergeHatObserveFalseFromImport(t *testing.T) {
	cur, err := ParseConfig([]byte("version: 1\nhats:\n  privat:\n    activities: []\n"))
	if err != nil {
		t.Fatal(err)
	}
	in, err := ParseConfig([]byte("version: 1\nhats:\n  privat:\n    activities: []\n    observe: false\n"))
	if err != nil {
		t.Fatal(err)
	}
	m, _ := Merge(cur, in, false)
	h, ok := m.Hat("privat")
	if !ok {
		t.Fatal("hat privat missing after merge")
	}
	if h.Observes() {
		t.Errorf("hat privat: Observes() = true after importing observe: false")
	}
}
