package server

import (
	"context"
	"net/http"
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

// addDevice adds a device that never connects and returns its id.
func addDevice(t *testing.T, e *env, name string) string {
	t.Helper()
	if _, _, err := e.st.AddDevice(context.Background(), name); err != nil {
		t.Fatal(err)
	}
	return deviceID(t, e, name)
}

func TestSensorMatrix(t *testing.T) {
	e, b := setupBrowser(t)
	e.do("POST", "/api/devices/hello", sensorCaps)
	desk := addDevice(t, e, "desk")
	laptop := deviceID(t, e, "laptop")
	_, out := b.do("GET", "/setup/grants", nil)
	if strings.Count(out, "<th>")-1 != 2 || !strings.Contains(out, "laptop") || !strings.Contains(out, "desk") {
		t.Fatalf("two columns:\n%s", out)
	}
	if strings.Index(out, "Away") > strings.Index(out, "Active window") || !strings.Contains(out, "lock and inactivity") {
		t.Error("idle first, with subtitle")
	}
	if strings.Count(out, "not reported yet") != 2 {
		t.Errorf("the never connected device is unknown in both rows")
	}
	if !strings.Contains(out, `name="on.`+laptop+`.idle" value="1" checked`) {
		t.Error("idle is on by default")
	}
	if !strings.Contains(out, `name="on.`+laptop+`.window" value="1" disabled`) || !strings.Contains(out, "no xprop found") || !strings.Contains(out, "not available on this device") {
		t.Error("an unavailable sensor has a disabled switch and a reason")
	}
	if strings.Contains(out, `name="on.`+desk+`.window" value="1" disabled`) || strings.Contains(out, `name="on.`+desk+`.window" value="1" checked`) {
		t.Error("an unknown sensor stays usable and off")
	}
	if !strings.Contains(out, "logind") {
		t.Error("backend shown")
	}
	// enabled but unavailable can be switched off
	b.do("POST", "/setup/grants", url.Values{"op": {"sensors"}, "devices": {laptop}, "sensors": {"idle,window"}, "on." + laptop + ".window": {"1"}})
	_, out = b.do("GET", "/setup/grants", nil)
	if strings.Contains(out, `name="on.`+laptop+`.window" value="1" checked disabled`) || strings.Contains(out, `name="on.`+laptop+`.window" value="1" disabled`) {
		t.Error("an enabled sensor is never disabled")
	}
}

func TestSensorMatrixRows(t *testing.T) {
	e, b := setupBrowser(t)
	_, out := b.do("GET", "/setup/grants", nil)
	if !strings.Contains(out, "Away") || !strings.Contains(out, "Active window") || strings.Contains(out, "Microphone") {
		t.Error("without reports: idle and window")
	}
	e.do("POST", "/api/devices/hello", `{"capabilities":{"sensor:media":{"available":false,"reason":"no pw-dump"}}}`)
	_, out = b.do("GET", "/setup/grants", nil)
	if !strings.Contains(out, "Microphone &amp; camera") || !strings.Contains(out, "only whether, never what") {
		t.Errorf("reported sensors join the rows:\n%s", out)
	}
}

func TestSensorsSave(t *testing.T) {
	e, b := setupBrowser(t)
	e.do("POST", "/api/devices/hello", sensorCaps)
	laptop, desk := deviceID(t, e, "laptop"), addDevice(t, e, "desk")
	post := func(on ...string) {
		f := url.Values{"op": {"sensors"}, "devices": {laptop + "," + desk}, "sensors": {"idle,window"}}
		for _, o := range on {
			f.Set("on."+o, "1")
		}
		r, _ := b.do("POST", "/setup/grants", f)
		if r.StatusCode != 303 || r.Header.Get("Location") != "/setup/grants#basics" {
			t.Fatalf("save: %d %s", r.StatusCode, r.Header.Get("Location"))
		}
	}
	post(laptop+".idle", desk+".idle", desk+".window")
	c := config(t, e).Observe
	if len(c.Devices) != 1 || len(c.Devices["desk"]) != 1 || !c.Devices["desk"]["window"] {
		t.Fatalf("window on for the unconnected device: %v", c.Devices)
	}
	post(desk + ".idle") // laptop idle off
	if got := config(t, e).Observe.Devices; len(got["laptop"]) != 1 || got["laptop"]["idle"] || len(got["desk"]) != 0 {
		t.Fatalf("laptop idle off: %v", got)
	}
	post(laptop+".idle", desk+".idle")
	if got := config(t, e).Observe.Devices; got != nil {
		t.Fatalf("defaults leave no devices map: %v", got)
	}
	// unknown device ids are ignored
	r, _ := b.do("POST", "/setup/grants", url.Values{"op": {"sensors"}, "devices": {"nope"}, "sensors": {"window"}, "on.nope.window": {"1"}})
	if r.StatusCode != 303 || config(t, e).Observe.Devices != nil {
		t.Error("unknown device")
	}
	for _, page := range []string{"/setup/detection", "/setup/grants"} {
		if r, _ := b.do("POST", page, url.Values{"op": {"nope"}}); !strings.Contains(r.Header.Get("Location"), "e=") {
			t.Errorf("%s: unknown action", page)
		}
	}
	// the old place takes no sensor settings any more
	if r, _ := b.do("POST", "/setup/detection", url.Values{"op": {"sensors"}, "devices": {laptop}, "sensors": {"window"}, "on." + laptop + ".window": {"1"}}); !strings.Contains(r.Header.Get("Location"), "e=") {
		t.Error("sensors saved on the detection page")
	}
}

func TestDetectionTabs(t *testing.T) {
	_, b := setupBrowser(t)
	for _, q := range []string{"", "?tab=nope"} {
		_, out := b.do("GET", "/setup/detection"+q, nil)
		if strings.Contains(out, `class="matrix"`) || strings.Contains(out, "tab=sensors") || strings.Contains(out, "tab=privacy") {
			t.Errorf("%q: sensors or privacy still on the detection page", q)
		}
		if !strings.Contains(out, `<a href="/setup/detection?tab=idle" class="on"`) {
			t.Errorf("%q: absence is the first tab", q)
		}
	}
	// old links lead to the page Freigaben
	for q, want := range map[string]string{"?tab=sensors": "/setup/grants#basics", "?tab=privacy": "/setup/grants#privacy"} {
		r, _ := b.do("GET", "/setup/detection"+q, nil)
		if r.StatusCode != http.StatusMovedPermanently || r.Header.Get("Location") != want {
			t.Errorf("%s: %d %s", q, r.StatusCode, r.Header.Get("Location"))
		}
	}
	_, out := b.do("GET", "/setup/grants", nil)
	for _, want := range []string{`class="matrix"`, "<textarea", `id="basics"`, `id="privacy"`, "never records what you type"} {
		if !strings.Contains(out, want) {
			t.Errorf("grants page lacks %s", want)
		}
	}
}

func TestDetectionOverview(t *testing.T) {
	e, b := setupBrowser(t)
	e.do("POST", "/api/devices/hello", sensorCaps)
	laptop := deviceID(t, e, "laptop")
	_, out := b.do("GET", "/setup", nil)
	if !strings.Contains(out, "laptop: Away") || !strings.Contains(out, "kept 28 days") || strings.Contains(out, "never recorded") {
		t.Errorf("default summary:\n%s", out)
	}
	b.do("POST", "/setup/grants", url.Values{"op": {"observe"}, "retention": {"7"}, "ignore": {"a\nb"}})
	b.do("POST", "/setup/grants", url.Values{"op": {"sensors"}, "devices": {laptop}, "sensors": {"idle,window"}, "on." + laptop + ".window": {"1"}})
	_, out = b.do("GET", "/setup", nil)
	if !strings.Contains(out, "laptop: Active window") || !strings.Contains(out, "2 apps never recorded") || !strings.Contains(out, "kept 7 days") || !strings.Contains(out, `href="/setup/grants"`) {
		t.Errorf("summary:\n%s", out)
	}
	b.do("POST", "/setup/grants", url.Values{"op": {"sensors"}, "devices": {laptop}, "sensors": {"idle,window"}})
	_, out = b.do("GET", "/setup", nil)
	if !strings.Contains(out, "all off") {
		t.Errorf("nothing on:\n%s", out)
	}
}

func TestDevicesPageSlim(t *testing.T) {
	e, b := setupBrowser(t)
	e.do("POST", "/api/devices/hello", sensorCaps)
	_, out := b.do("GET", "/setup/devices", nil)
	if !strings.Contains(out, `class="devline"`) || !strings.Contains(out, "Detection: Away") || !strings.Contains(out, `href="/setup/grants"`) {
		t.Errorf("devline:\n%s", out)
	}
	for _, not := range []string{`class="sensors"`, `class="privacy"`, `class="sw"`, "sensor:idle"} {
		if strings.Contains(out, not) {
			t.Errorf("devices page still has %s", not)
		}
	}
	for _, op := range []string{"sensors", "observe"} {
		if r, _ := b.do("POST", "/setup/devices", url.Values{"op": {op}}); !strings.Contains(r.Header.Get("Location"), "unknown+action") {
			t.Errorf("op=%s is gone from the devices page: %s", op, r.Header.Get("Location"))
		}
	}
	laptop := deviceID(t, e, "laptop")
	b.do("POST", "/setup/grants", url.Values{"op": {"sensors"}, "devices": {laptop}, "sensors": {"idle,window"}})
	if _, out := b.do("GET", "/setup/devices", nil); !strings.Contains(out, "Detection: all off") {
		t.Error("devline when all is off")
	}
}

func TestObserveSave(t *testing.T) {
	e, b := setupBrowser(t)
	post := func(ret, ignore, redact string) *url.URL {
		r, _ := b.do("POST", "/setup/grants", url.Values{"op": {"observe"}, "retention": {ret}, "ignore": {ignore}, "redact": {redact}})
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
	_, out := b.do("GET", "/setup/grants", nil)
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
	id := deviceID(t, e, "laptop")
	b.do("POST", "/setup/grants", url.Values{"op": {"sensors"}, "devices": {id}, "sensors": {"idle,window"}, "on." + id + ".idle": {"1"}, "on." + id + ".window": {"1"}})
}

func TestSensorToday(t *testing.T) {
	e, b := setupBrowser(t)
	e.do("POST", "/api/devices/hello", `{"capabilities":{"sensor:idle":{"backend":"logind","available":true},"sensor:window":{"backend":"x11","available":true}}}`)
	_, out := b.do("GET", "/setup/grants", nil)
	if strings.Contains(out, "today ") {
		t.Fatal("nothing today, nothing shown")
	}
	enableWindow(t, e, b)
	// e.now is 12:00 UTC; spans of 5 minutes each
	e.postObs(e.obs("idle", -10, nil), e.obs("idle", -30, nil), e.obs("window", -60, core.Attrs{"app": "firefox"}))
	_, out = b.do("GET", "/setup/grants", nil)
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
	b.do("POST", "/setup/grants", url.Values{"op": {"observe"}, "retention": {"28"}, "ignore": {"keepassxc"}, "redact": {"mail"}})
	_, out := b.do("GET", "/setup/grants", nil)
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
	b.do("POST", "/setup/grants", url.Values{"op": {"observe"}, "retention": {"28"}, "ignore": {"FIREFOX"}})
	if _, out := b.do("GET", "/setup/grants", nil); strings.Contains(out, `data-add="firefox"`) {
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

func TestIdleTab(t *testing.T) {
	e, b := setupBrowser(t)
	_, out := b.do("GET", "/setup/detection?tab=idle", nil)
	for _, want := range []string{
		`<a href="/setup/detection?tab=idle" class="on"`,
		`name="after" min="1" max="1440" value="5"`,
		`name="ask_after" min="1" max="1440" value="10"`,
		`name="stop_after" min="1" max="1440" value="15"`,
		`name="ask" value="1" checked`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("idle tab lacks %q", want)
		}
	}
	if strings.Contains(out, `class="matrix"`) || strings.Contains(out, "<textarea") {
		t.Errorf("idle tab shows other tabs")
	}

	post := func(after, ask, stop, checked string) *url.URL {
		v := url.Values{"op": {"idle"}, "after": {after}, "ask_after": {ask}, "stop_after": {stop}}
		if checked != "" {
			v.Set("ask", checked)
		}
		r, _ := b.do("POST", "/setup/detection", v)
		u, _ := url.Parse(r.Header.Get("Location"))
		return u
	}
	// the defaults store nothing
	if u := post("5", "10", "15", "1"); u.Query().Get("e") != "" || u.Query().Get("tab") != "idle" {
		t.Fatalf("defaults: %s", u)
	}
	if i := config(t, e).Observe.Idle; i != (core.Idle{}) {
		t.Fatalf("defaults stored: %+v", i)
	}
	// own values, "ask" off
	if u := post("3", "8", "20", ""); u.Query().Get("e") != "" {
		t.Fatalf("error: %s", u.Query().Get("e"))
	}
	i := config(t, e).Observe.Idle
	if i.After != 3 || i.AskAfter != 8 || i.StopAfter != 20 || i.Ask == nil || *i.Ask {
		t.Fatalf("stored: %+v", i)
	}
	_, out = b.do("GET", "/setup/detection?tab=idle", nil)
	if !strings.Contains(out, `name="after" min="1" max="1440" value="3"`) || strings.Contains(out, `name="ask" value="1" checked`) {
		t.Errorf("form is prefilled:\n%s", out)
	}
	// errors leave the config alone
	for _, c := range [][3]string{{"0", "8", "20"}, {"3", "2000", "20"}, {"3", "8", "x"}, {"", "8", "20"}} {
		u := post(c[0], c[1], c[2], "1")
		// the text comes from web.idle.bad_minutes (the key itself until the catalogs have it)
		if e := u.Query().Get("e"); !strings.Contains(e, "bad_minutes") && !strings.Contains(e, "1440") {
			t.Errorf("%v: %q", c, u.Query().Get("e"))
		}
	}
	u := post("9", "8", "20", "1")
	if u.Query().Get("e") == "" || !strings.Contains(u.Query().Get("e"), "after") {
		t.Errorf("after > ask_after: %q", u.Query().Get("e"))
	}
	if u := post("9", "10", "5", "1"); u.Query().Get("e") == "" {
		t.Errorf("after > stop_after accepted")
	}
	if got := config(t, e).Observe.Idle; got.After != 3 || got.AskAfter != 8 || got.StopAfter != 20 || got.Ask == nil || *got.Ask {
		t.Errorf("config changed by errors: %+v", got)
	}
}
