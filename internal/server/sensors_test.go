package server

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"hatrack/internal/core"
)

const sensorCaps = `{"capabilities":{"launch":{"backend":"xdg","available":true},` +
	`"sensor:idle":{"backend":"logind","available":true},` +
	`"sensor:window":{"available":false,"reason":"no xprop found"}}}`

func deviceID(t *testing.T, e *env, name string) string {
	t.Helper()
	devs, _ := e.st.Devices(context.Background())
	for _, d := range devs {
		if d.Name == name {
			return d.ID
		}
	}
	t.Fatalf("no device %q", name)
	return ""
}

func TestSensorBlock(t *testing.T) {
	e, b := setupBrowser(t)
	e.do("POST", "/api/devices/hello", `{"capabilities":{"launch":{"backend":"xdg","available":true}}}`)
	if _, out := b.do("GET", "/setup/devices", nil); strings.Contains(out, `class="sensors"`) {
		t.Fatal("no sensors reported, no detection block")
	}
	e.do("POST", "/api/devices/hello", sensorCaps)
	_, out := b.do("GET", "/setup/devices", nil)
	if !strings.Contains(out, `class="sensors"`) || !strings.Contains(out, "Away") || !strings.Contains(out, "Active window") || !strings.Contains(out, "no xprop found") {
		t.Fatalf("sensor rows:\n%s", out)
	}
	if strings.Index(out, "Away") > strings.Index(out, "Active window") {
		t.Error("idle comes first")
	}
	if !strings.Contains(out, `name="on.idle" value="1" checked`) {
		t.Error("idle is on by default")
	}
	if !strings.Contains(out, `name="on.window" value="1" disabled`) {
		t.Error("an unavailable, disabled sensor has a disabled switch")
	}
	if strings.Contains(out, `sensor:idle`) {
		t.Error("sensors stay out of the module table")
	}
	// enabled but unavailable can be switched off
	b.do("POST", "/setup/devices", url.Values{"op": {"sensors"}, "id": {deviceID(t, e, "laptop")}, "sensors": {"idle,window"}, "on.window": {"1"}})
	_, out = b.do("GET", "/setup/devices", nil)
	if strings.Contains(out, `name="on.window" value="1" checked disabled`) || strings.Contains(out, `name="on.window" value="1" disabled`) {
		t.Error("an enabled sensor is never disabled")
	}
}

func TestSensorsSave(t *testing.T) {
	e, b := setupBrowser(t)
	e.do("POST", "/api/devices/hello", sensorCaps)
	id := deviceID(t, e, "laptop")
	post := func(on ...string) {
		f := url.Values{"op": {"sensors"}, "id": {id}, "sensors": {"idle,window"}}
		for _, o := range on {
			f.Set("on."+o, "1")
		}
		if r, _ := b.do("POST", "/setup/devices", f); r.StatusCode != 303 {
			t.Fatalf("save: %d", r.StatusCode)
		}
	}
	post("idle", "window")
	if got := config(t, e).Observe.Devices["laptop"]; len(got) != 1 || !got["window"] {
		t.Fatalf("window on: %v", got)
	}
	post("window") // idle off
	if got := config(t, e).Observe.Devices["laptop"]; len(got) != 2 || got["idle"] || !got["window"] {
		t.Fatalf("idle off: %v", got)
	}
	post("idle")
	if got, ok := config(t, e).Observe.Devices["laptop"]; ok {
		t.Fatalf("defaults leave no entry: %v", got)
	}
	if r, _ := b.do("POST", "/setup/devices", url.Values{"op": {"sensors"}, "id": {"nope"}}); !strings.Contains(r.Header.Get("Location"), "e=") {
		t.Error("unknown device")
	}
}

func TestObserveSave(t *testing.T) {
	e, b := setupBrowser(t)
	post := func(ret, ignore, redact string) *url.URL {
		r, _ := b.do("POST", "/setup/devices", url.Values{"op": {"observe"}, "retention": {ret}, "ignore": {ignore}, "redact": {redact}})
		u, _ := url.Parse(r.Header.Get("Location"))
		return u
	}
	if u := post("28", " keepassxc \r\n\r\nSignal\nsignal\n  \nbanking", "thunderbird"); u.Query().Get("e") != "" {
		t.Fatalf("error: %s", u.Query().Get("e"))
	}
	o := config(t, e).Observe
	if strings.Join(o.Ignore, ",") != "keepassxc,Signal,banking" || strings.Join(o.Redact, ",") != "thunderbird" || o.RetentionDays != 0 {
		t.Fatalf("stored: %+v", o)
	}
	post("7", "x", "")
	if o := config(t, e).Observe; o.RetentionDays != 7 || len(o.Redact) != 0 {
		t.Fatalf("7 days: %+v", o)
	}
	_, out := b.do("GET", "/setup/devices", nil)
	if !strings.Contains(out, `name="retention" min="1" max="365" value="7"`) || !strings.Contains(out, ">x</textarea>") {
		t.Errorf("form is prefilled:\n%s", out)
	}
	for _, bad := range []string{"0", "400", "abc", ""} {
		u := post(bad, "changed", "")
		if u.Query().Get("e") != "Retention: please 1 to 365 days." {
			t.Errorf("retention %q: %q", bad, u.Query().Get("e"))
		}
		if o := config(t, e).Observe; o.RetentionDays != 7 || strings.Join(o.Ignore, ",") != "x" {
			t.Errorf("config changed by %q: %+v", bad, o)
		}
	}
}

// enableWindow switches the window sensor on for the laptop, as the page does.
func enableWindow(t *testing.T, e *env, b *browser) {
	t.Helper()
	b.do("POST", "/setup/devices", url.Values{"op": {"sensors"}, "id": {deviceID(t, e, "laptop")}, "sensors": {"idle,window"}, "on.idle": {"1"}, "on.window": {"1"}})
}

func TestSensorToday(t *testing.T) {
	e, b := setupBrowser(t)
	e.do("POST", "/api/devices/hello", `{"capabilities":{"sensor:idle":{"backend":"logind","available":true},"sensor:window":{"backend":"x11","available":true}}}`)
	_, out := b.do("GET", "/setup/devices", nil)
	if strings.Contains(out, "today ") {
		t.Fatal("nothing today, nothing shown")
	}
	enableWindow(t, e, b)
	// e.now is 12:00 UTC; spans of 5 minutes each
	e.postObs(e.obs("idle", -10, nil), e.obs("idle", -30, nil), e.obs("window", -60, core.Attrs{"app": "firefox"}))
	_, out = b.do("GET", "/setup/devices", nil)
	for _, want := range []string{"today 2 breaks, 10m", "today 5m, 1 changes"} {
		if !strings.Contains(out, want) {
			t.Errorf("lacks %q", want)
		}
	}
}

func TestRecentApps(t *testing.T) {
	e, b := setupBrowser(t)
	e.do("POST", "/api/devices/hello", sensorCaps)
	enableWindow(t, e, b)
	win := func(min int, app string) core.Observation { return e.obs("window", min, core.Attrs{"app": app}) }
	old := win(-3*24*60, "oldapp")
	priv := e.obs("window", -20, core.Attrs{core.AttrPrivate: "1"})
	e.postObs(win(-10, "firefox"), win(-20, "firefox"), win(-40, "code"), win(-50, "Signal"), win(-60, "keepassxc"), win(-70, "mail"), old, priv)
	b.do("POST", "/setup/devices", url.Values{"op": {"observe"}, "retention": {"28"}, "ignore": {"keepassxc"}, "redact": {"mail"}})
	_, out := b.do("GET", "/setup/devices", nil)
	for _, want := range []string{`data-add="firefox"`, `data-add="code"`, `data-add="Signal"`, `data-add="oldapp"`} {
		if !strings.Contains(out, want) {
			t.Errorf("lacks chip %s", want)
		}
	}
	for _, not := range []string{`data-add="keepassxc"`, `data-add="mail"`, `data-add=""`} {
		if strings.Contains(out, not) {
			t.Errorf("unexpected chip %s", not)
		}
	}
	if strings.Index(out, `data-add="firefox"`) > strings.Index(out, `data-add="code"`) {
		t.Error("most used first")
	}
	// listed in any case: no chip
	b.do("POST", "/setup/devices", url.Values{"op": {"observe"}, "retention": {"28"}, "ignore": {"FIREFOX"}})
	if _, out := b.do("GET", "/setup/devices", nil); strings.Contains(out, `data-add="firefox"`) {
		t.Error("case-insensitive")
	}
}

func TestHatObserve(t *testing.T) {
	e, b := setupBrowser(t)
	hat := func() core.Hat { h, _ := config(t, e).Hat("venom"); return h }
	checked := func() bool {
		_, out := b.do("GET", "/setup/hat?key=venom", nil)
		i := strings.Index(out, `name="observe"`)
		if i < 0 {
			t.Fatal("no observe checkbox")
		}
		return strings.HasPrefix(out[i:], `name="observe" value="1" checked`)
	}
	if !checked() {
		t.Error("observing by default")
	}
	b.do("POST", "/setup/hat", hatForm(nil))
	if h := hat(); h.Observe == nil || *h.Observe {
		t.Fatalf("unchecked stores false: %v", h.Observe)
	}
	if checked() {
		t.Error("renders unchecked")
	}
	b.do("POST", "/setup/hat", hatForm(map[string]string{"observe": "1"}))
	if h := hat(); h.Observe != nil {
		t.Fatalf("checked removes it: %v", *h.Observe)
	}
	if !checked() {
		t.Error("renders checked")
	}
}
