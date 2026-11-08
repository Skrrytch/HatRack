package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"hatrack/internal/core"
)

func setupBrowser(t *testing.T) (*env, *browser) {
	e := setup(t)
	seed(t, e)
	withPassword(t, e, "pw")
	b := newBrowser(t, e)
	b.login("pw")
	b.do("GET", "/?lang=en", nil)
	return e, b
}

func config(t *testing.T, e *env) core.Config {
	t.Helper()
	c, _, err := e.st.GetConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSetupHats(t *testing.T) {
	e, b := setupBrowser(t)
	if r, out := b.do("GET", "/setup", nil); r.StatusCode != 200 || !strings.Contains(out, "VENOM") || !strings.Contains(out, "Defaults") {
		t.Fatalf("setup page: %d", r.StatusCode)
	}
	// add: the key comes from the name, the browser goes to the new hat
	r, _ := b.do("POST", "/setup/hats", url.Values{"op": {"add"}, "name": {"Kunde Müller"}, "color": {"#E15759"}})
	if r.StatusCode != 303 || r.Header.Get("Location") != "/setup/hat?key=kunde-mueller" {
		t.Fatalf("add: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
	h, ok := config(t, e).Hat("kunde-mueller")
	if !ok || h.Name != "Kunde Müller" || h.Color != "#e15759" {
		t.Fatalf("added hat: %+v", h)
	}
	b.do("POST", "/setup/hats", url.Values{"op": {"add"}, "name": {"Kunde Müller"}})
	if _, ok := config(t, e).Hat("kunde-mueller-2"); !ok {
		t.Fatal("a second hat with the same name gets its own key")
	}
	// move and archive
	b.do("POST", "/setup/hats", url.Values{"op": {"up"}, "key": {"tag"}})
	if c := config(t, e); c.Hats[0].Key != "tag" {
		t.Fatalf("move up: %v", c.Hats)
	}
	b.do("POST", "/setup/hats", url.Values{"op": {"archive"}, "key": {"tag"}})
	if h, _ := config(t, e).Hat("tag"); !h.Archived {
		t.Fatal("archive")
	}
	if _, out := b.do("GET", "/setup", nil); !strings.Contains(out, "Archived") {
		t.Fatal("archived section")
	}
	b.do("POST", "/setup/hats", url.Values{"op": {"restore"}, "key": {"tag"}})
	if h, _ := config(t, e).Hat("tag"); h.Archived {
		t.Fatal("restore")
	}
	if r, _ := b.do("POST", "/setup/hats", url.Values{"op": {"add"}, "name": {"  "}}); !strings.Contains(r.Header.Get("Location"), "e=") {
		t.Fatal("an empty name is an error")
	}
}

func hatForm(over map[string]string) url.Values {
	f := url.Values{"key": {"venom"}, "name": {"VENOM"}, "color": {"#7a1fa2"}, "resume": {""},
		"act.0": {"development"}, "act.1": {"review"}, "op": {"save"}}
	for k, v := range over {
		f.Set(k, v)
	}
	return f
}

func TestSetupHatForm(t *testing.T) {
	e, b := setupBrowser(t)
	// defaults with an image, so the hat may set only the fit
	e.do("PUT", "/api/config", "version: 1\nmodules:\n  wallpaper: {image: /pics/default.jpg}\nhats: {}\n")
	r, out := b.do("GET", "/setup/hat?key=venom", nil)
	if r.StatusCode != 200 || !strings.Contains(out, "Wallpaper") || !strings.Contains(out, `placeholder="/pics/default.jpg"`) {
		t.Fatalf("hat page: %d\n%s", r.StatusCode, out)
	}
	r, _ = b.do("POST", "/setup/hat", hatForm(map[string]string{
		"name": "Venom", "resume": "last", "act.1": "code review", "newact": "meeting",
		"m/wallpaper/options": "zoom", "m/notify/enabled": "false",
	}))
	if r.StatusCode != 303 {
		t.Fatalf("save: %d", r.StatusCode)
	}
	h, _ := config(t, e).Hat("venom")
	if h.Name != "Venom" || h.ResumeActivity != "last" || strings.Join(h.Activities, ",") != "development,code review,meeting" {
		t.Fatalf("saved hat: %+v", h)
	}
	if h.Modules["wallpaper"]["options"] != "zoom" || h.Modules["wallpaper"]["image"] != nil || h.Modules["notify"]["enabled"] != false {
		t.Fatalf("modules: %+v", h.Modules)
	}
	// activity buttons save the form and act: down, archive, restore
	b.do("POST", "/setup/hat", hatForm(map[string]string{"act.1": "code review", "act.2": "meeting", "op": "down:0"}))
	if h, _ := config(t, e).Hat("venom"); strings.Join(h.Activities, ",") != "code review,development,meeting" {
		t.Fatalf("down: %v", h.Activities)
	}
	b.do("POST", "/setup/hat", hatForm(map[string]string{"act.0": "code review", "act.1": "development", "act.2": "meeting", "op": "archive:1"}))
	h, _ = config(t, e).Hat("venom")
	if strings.Join(h.Activities, ",") != "code review,meeting" || strings.Join(h.ArchivedActivity, ",") != "development" {
		t.Fatalf("archive: %+v", h)
	}
	b.do("POST", "/setup/hat", hatForm(map[string]string{"act.0": "code review", "act.1": "meeting", "op": "restore:development"}))
	if h, _ := config(t, e).Hat("venom"); strings.Join(h.Activities, ",") != "code review,meeting,development" || len(h.ArchivedActivity) != 0 {
		t.Fatalf("restore: %+v", h)
	}
	// errors keep what was typed
	r, out = b.do("POST", "/setup/hat", hatForm(map[string]string{"act.0": "meeting", "act.1": "meeting", "name": "Typed"}))
	if r.StatusCode != 400 || !strings.Contains(out, "Not saved") || !strings.Contains(out, `value="Typed"`) {
		t.Fatalf("duplicate activity: %d", r.StatusCode)
	}
	if h, _ := config(t, e).Hat("venom"); h.Name != "VENOM" {
		t.Fatal("a failed save must not store anything")
	}
	r, _ = b.do("POST", "/setup/hat", hatForm(map[string]string{"m/color_scheme/prefer": "purple"}))
	if r.StatusCode != 400 {
		t.Fatalf("schema violation: %d", r.StatusCode)
	}
}

func TestSetupDefaults(t *testing.T) {
	e, b := setupBrowser(t)
	if r, out := b.do("GET", "/setup/defaults", nil); r.StatusCode != 200 || !strings.Contains(out, "Image file") {
		t.Fatalf("defaults page: %d", r.StatusCode)
	}
	// a fit without an image would leave every hat without one: refused
	if r, out := b.do("POST", "/setup/defaults", url.Values{"resume": {"ask"}, "m/wallpaper/options": {"zoom"}}); r.StatusCode != 400 || !strings.Contains(out, "image") {
		t.Fatalf("defaults without image: %d", r.StatusCode)
	}
	r, _ := b.do("POST", "/setup/defaults", url.Values{"resume": {"ask"}, "m/notify/enabled": {"true"},
		"m/wallpaper/options": {"zoom"}, "m/wallpaper/image": {"/pics/a.jpg"}})
	if r.StatusCode != 303 {
		t.Fatalf("save: %d", r.StatusCode)
	}
	c := config(t, e)
	if c.Defaults.ResumeActivity != "ask" || c.Modules["notify"]["enabled"] != true || c.Modules["wallpaper"]["image"] != "/pics/a.jpg" {
		t.Fatalf("defaults: %+v %+v", c.Defaults, c.Modules)
	}
}

func multipartImport(t *testing.T, b *browser, yaml, op string) (*http.Response, string) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "hats.yaml")
	io.WriteString(fw, yaml)
	mw.WriteField("op", op)
	mw.Close()
	req, _ := http.NewRequest("POST", b.e.ts.URL+"/setup/import", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := b.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, string(out)
}

func TestSetupImportExport(t *testing.T) {
	e, b := setupBrowser(t)
	r, out := b.do("GET", "/setup/export.yaml", nil)
	if r.StatusCode != 200 || !strings.Contains(out, "venom:") {
		t.Fatalf("export: %d %s", r.StatusCode, out)
	}
	y := "version: 1\nhats:\n  new:\n    name: New\n    activities: [a]\n"
	r, out = b.do("POST", "/setup/import", url.Values{"data": {y}, "op": {"preview"}})
	if r.StatusCode != 200 || !strings.Contains(out, "new hat: new") || !strings.Contains(out, `value="apply"`) {
		t.Fatalf("preview: %d", r.StatusCode)
	}
	if _, ok := config(t, e).Hat("new"); ok {
		t.Fatal("preview must not store")
	}
	r, _ = multipartImport(t, b, y, "apply")
	if r.StatusCode != 303 || !strings.Contains(r.Header.Get("Location"), "imported=1") {
		t.Fatalf("apply: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
	if _, ok := config(t, e).Hat("new"); !ok {
		t.Fatal("import did not store")
	}
	if r, out := b.do("POST", "/setup/import", url.Values{"data": {"version: 7"}, "op": {"preview"}}); r.StatusCode != 400 || !strings.Contains(out, "Not saved") {
		t.Fatalf("bad yaml: %d", r.StatusCode)
	}
}

func TestSetupNeedsLogin(t *testing.T) {
	e := setup(t)
	seed(t, e)
	b := newBrowser(t, e)
	b.do("GET", "/?token="+e.token, nil) // read-only view
	if r, _ := b.do("GET", "/setup", nil); r.StatusCode != 303 {
		t.Fatalf("read-only access to setup: %d", r.StatusCode)
	}
	if r, _ := b.do("POST", "/setup/hats", url.Values{"op": {"add"}, "name": {"x"}}); r.StatusCode != 403 {
		t.Fatalf("read-only post: %d", r.StatusCode)
	}
	if _, out := b.do("GET", "/", nil); strings.Contains(out, `href="/setup"`) {
		t.Fatal("no Setup link without login")
	}
}

func TestSetupQuickPaths(t *testing.T) {
	e, b := setupBrowser(t)
	// a new hat with its activities in one step, in a colour nobody uses
	_, out := b.do("GET", "/setup", nil)
	if !strings.Contains(out, `name="color" value="#8c4ea3"`) && !strings.Contains(out, `name="color" value="#ee6d24"`) {
		t.Fatalf("next colour missing")
	}
	// the palette is offered as swatches, one of them picked
	if strings.Count(out, `<button type="button" class="swatch`) != len(core.HatColors) || !strings.Contains(out, `class="swatch on"`) || !strings.Contains(out, `data-name="mint"`) {
		t.Fatalf("swatches missing")
	}
	b.do("POST", "/setup/hats", url.Values{"op": {"add"}, "name": {"Kunde"}, "activities": {"Angebot, Workshop; workshop\nSupport"}})
	h, _ := config(t, e).Hat("kunde")
	if strings.Join(h.Activities, ",") != "Angebot,Workshop,Support" {
		t.Fatalf("activities from the new-hat form: %v", h.Activities)
	}
	// several new activities at once on the hat page, without duplicates
	b.do("POST", "/setup/hat", url.Values{"key": {"kunde"}, "name": {"Kunde"}, "color": {"#4e79a7"},
		"act.0": {"Angebot"}, "act.1": {"Workshop"}, "act.2": {"Support"}, "newact": {"Reise, angebot, Abrechnung"}, "op": {"add"}})
	h, _ = config(t, e).Hat("kunde")
	if strings.Join(h.Activities, ",") != "Angebot,Workshop,Support,Reise,Abrechnung" {
		t.Fatalf("several activities: %v", h.Activities)
	}
	// the module sections say what applies while closed
	e.do("PUT", "/api/config", "version: 1\nmodules:\n  wallpaper: {image: /pics/d.jpg}\nhats:\n  kunde:\n    name: Kunde\n    activities: []\n    modules:\n      wallpaper: {options: zoom}\n")
	_, out = b.do("GET", "/setup/hat?key=kunde", nil)
	if !strings.Contains(out, "Fit: fill · rest from the defaults") || !strings.Contains(out, "not set up") {
		t.Fatalf("module summaries missing:\n%s", out)
	}
	// built-in wallpapers and yes/no read like words, not like values
	if r, o := e.do("PUT", "/api/config", "version: 1\nhats:\n  kunde:\n    name: Kunde\n    activities: []\n    modules:\n      wallpaper: {image: \"hatrack:wp02-purple\"}\n"); r.StatusCode != 200 {
		t.Fatalf("put: %s", o)
	}
	if _, out = b.do("GET", "/setup/hat?key=kunde", nil); !strings.Contains(out, "Image file: Purple 2") || strings.Contains(out, "hatrack:wp02") && strings.Contains(out, "Image file: hatrack") {
		t.Fatal("built-in wallpaper by colour and number in the summary")
	}
}

func TestNowPage(t *testing.T) {
	_, b := setupBrowser(t)
	b.do("POST", "/action", url.Values{"op": {"on"}, "hat": {"venom"}, "activity": {"review"}})
	_, out := b.do("GET", "/", nil)
	for _, want := range []string{"Stop", "Pause", `data-since="`, `href="/setup/hat?key=venom"`, `>Today</a>`, "hatcard cur", "You are working on", `class="stamp num"`} {
		if !strings.Contains(out, want) {
			t.Errorf("Now page lacks %q", want)
		}
	}
	if strings.Contains(out, "laptop") {
		t.Error("devices moved to Setup")
	}
	if _, out := b.do("GET", "/setup", nil); !strings.Contains(out, "laptop") {
		t.Error("Setup lists the devices")
	}
}

func TestSetupChoicesFromDevices(t *testing.T) {
	e, b := setupBrowser(t)
	caps := `{"capabilities":{"color_scheme":{"backend":"cinnamon","available":true,"choices":{"theme":[{"value":"Mint-Y-Dark"},{"value":"Adwaita"}]}},
	  "wallpaper":{"backend":"cinnamon","available":true,"choices":{"image":[{"value":"/pics/a.jpg","label":"a.jpg","thumb":"data:image/jpeg;base64,AAAA"}]}}}}`
	r, body := e.do("POST", "/api/devices/hello", caps)
	if r.StatusCode != 200 {
		t.Fatalf("hello: %d", r.StatusCode)
	}
	if !strings.Contains(body, fmt.Sprintf(`"protocol":%d`, Protocol)) {
		t.Errorf("hello answer lacks the protocol: %s", body)
	}
	_, out := b.do("GET", "/setup/hat?key=venom", nil)
	for _, want := range []string{`list="f-color_scheme-theme-list"`, `<option value="Mint-Y-Dark">`, `class="gallery"`,
		`name="pick/m/wallpaper/image" value="/pics/a.jpg" data-target="f-wallpaper-image"`, `src="data:image/jpeg;base64,AAAA"`} {
		if !strings.Contains(out, want) {
			t.Errorf("hat page lacks %s", want)
		}
	}
	// the picked path arrives through the text field; the radio is ignored
	r, _ = b.do("POST", "/setup/hat", hatForm(map[string]string{"m/wallpaper/image": "/pics/a.jpg", "pick/m/wallpaper/image": "/pics/a.jpg"}))
	if r.StatusCode != 303 {
		t.Fatalf("save: %d", r.StatusCode)
	}
	if h, _ := config(t, e).Hat("venom"); h.Modules["wallpaper"]["image"] != "/pics/a.jpg" {
		t.Fatalf("image: %+v", h.Modules)
	}
}

func TestSetupDeleteUnusedHat(t *testing.T) {
	e, b := setupBrowser(t)
	b.do("POST", "/setup/hats", url.Values{"op": {"add"}, "name": {"Tippfehler"}})
	b.do("POST", "/setup/hats", url.Values{"op": {"archive"}, "key": {"tippfehler"}})
	b.do("POST", "/setup/hats", url.Values{"op": {"archive"}, "key": {"tag"}})
	_, out := b.do("GET", "/setup", nil)
	if strings.Count(out, `name="op" value="delete"`) != 1 {
		t.Fatal("only the unused archived hat offers delete")
	}
	if r, _ := b.do("POST", "/setup/hats", url.Values{"op": {"delete"}, "key": {"tag"}}); !strings.Contains(r.Header.Get("Location"), "e=") {
		t.Fatal("a used hat cannot be deleted")
	}
	b.do("POST", "/setup/hats", url.Values{"op": {"delete"}, "key": {"tippfehler"}})
	if _, ok := config(t, e).Hat("tippfehler"); ok {
		t.Fatal("unused hat still there")
	}
	if _, ok := config(t, e).Hat("tag"); !ok {
		t.Fatal("used hat gone")
	}
}

func TestDevicesPage(t *testing.T) {
	e, b := setupBrowser(t)
	e.do("POST", "/api/devices/hello", `{"capabilities":{"wallpaper":{"backend":"cinnamon","available":true,"choices":{"image":[{"value":"/a.jpg"}]}},"color_scheme":{"available":false}}}`)
	ev := core.NewEvent("laptop", core.EvModuleError, e.now.Add(-time.Hour))
	ev.Note = "wallpaper: /pics/x.jpg not found"
	body, _ := json.Marshal([]core.Event{ev})
	e.do("POST", "/api/events", string(body))
	_, out := b.do("GET", "/setup/devices", nil)
	for _, want := range []string{"laptop", "cinnamon", "1 × Image file", "not available here", "Errors in the last 7 days: 1", "/pics/x.jpg not found"} {
		if !strings.Contains(out, want) {
			t.Errorf("devices page lacks %q", want)
		}
	}
	// adding shows the token once, in the answer, not in an address
	r, out := b.do("POST", "/setup/devices", url.Values{"op": {"add"}, "name": {"desk"}})
	if r.StatusCode != 200 || !strings.Contains(out, "hat setup --server http://") || !strings.Contains(out, "--token hat_") {
		t.Fatalf("add: %d", r.StatusCode)
	}
	if r, _ := b.do("POST", "/setup/devices", url.Values{"op": {"add"}, "name": {"desk"}}); !strings.Contains(r.Header.Get("Location"), "e=") {
		t.Fatal("duplicate name")
	}
	devs, _ := e.st.Devices(context.Background())
	var id string
	for _, d := range devs {
		if d.Name == "desk" {
			id = d.ID
		}
	}
	b.do("POST", "/setup/devices", url.Values{"op": {"delete"}, "id": {id}})
	if devs, _ := e.st.Devices(context.Background()); len(devs) != 1 {
		t.Fatalf("delete: %d devices", len(devs))
	}
}

func TestSetupListField(t *testing.T) {
	e, b := setupBrowser(t)
	e.do("POST", "/api/devices/hello", `{"capabilities":{"launch":{"backend":"xdg","available":true,"choices":{"apps":[{"value":"firefox","label":"Firefox"}]}}}}`)
	_, out := b.do("GET", "/setup/hat?key=venom", nil)
	if !strings.Contains(out, `<textarea name="m/launch/apps"`) || !strings.Contains(out, `data-append="f-launch-apps"`) || !strings.Contains(out, "Firefox (firefox)") {
		t.Fatal("launch form with a program pick list")
	}
	r, _ := b.do("POST", "/setup/hat", hatForm(map[string]string{"m/launch/apps": "firefox\n\n  thunderbird \n", "m/launch/open": "https://x.example"}))
	if r.StatusCode != 303 {
		t.Fatalf("save: %d", r.StatusCode)
	}
	h, _ := config(t, e).Hat("venom")
	apps, _ := h.Modules["launch"]["apps"].([]any)
	if len(apps) != 2 || apps[0] != "firefox" || apps[1] != "thunderbird" {
		t.Fatalf("apps: %#v", h.Modules["launch"])
	}
	_, out = b.do("GET", "/setup/hat?key=venom", nil)
	if !strings.Contains(out, ">firefox\nthunderbird</textarea>") || !strings.Contains(out, "firefox, thunderbird") {
		t.Fatal("the list comes back one per line, and in the summary on one line")
	}
}

func TestSetupTickets(t *testing.T) {
	e, b := setupBrowser(t)
	// register with an issue link: project and base come from it
	r, _ := b.do("POST", "/setup/tickets", url.Values{"op": {"register"}, "link": {"https://jira.example.org/browse/VEN-1"}})
	if r.StatusCode != 303 || strings.Contains(r.Header.Get("Location"), "e=") {
		t.Fatalf("register: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
	if c := config(t, e); len(c.Tickets) != 1 || c.Tickets[0].Project != "VEN" || c.Tickets[0].URL != "https://jira.example.org/browse/" {
		t.Fatalf("tickets: %+v", c.Tickets)
	}
	// register with project and base
	b.do("POST", "/setup/tickets", url.Values{"op": {"register"}, "project": {"atl"}, "link": {"https://other.example.org/browse"}})
	c := config(t, e)
	if len(c.Tickets) != 2 || c.Tickets[1].Project != "ATL" || c.Tickets[1].URL != "https://other.example.org/browse/" {
		t.Fatalf("tickets: %+v", c.Tickets)
	}
	// the list shows both, registered
	if _, out := b.do("GET", "/setup", nil); !strings.Contains(out, "VEN") || !strings.Contains(out, "ATL") || !strings.Contains(out, "registered") {
		t.Fatalf("list:\n%s", out)
	}
	// invalid: no project, no link, bad base
	for _, f := range []url.Values{
		{"op": {"register"}, "link": {"https://jira.example.org/browse/"}},
		{"op": {"register"}, "project": {"VEN"}},
		{"op": {"register"}, "project": {"X"}, "link": {"https://a.example/browse/"}},
		{"op": {"register"}, "project": {"VEN"}, "link": {"ftp://a.example/browse/"}},
	} {
		if r, _ := b.do("POST", "/setup/tickets", f); !strings.Contains(r.Header.Get("Location"), "e=") {
			t.Fatalf("%v: want an error, got %s", f, r.Header.Get("Location"))
		}
	}
	if len(config(t, e).Tickets) != 2 {
		t.Fatal("invalid input changed the configuration")
	}
	// a registered project makes its keys links in the day view
	e.do("POST", "/api/events", mustJSON(t, []core.Event{func() core.Event {
		ev := e.ev(-20, core.EvSwitch, "venom", "development")
		ev.Detail = "VEN-42 fix"
		return ev
	}()}))
	if _, out := b.do("GET", "/day", nil); !strings.Contains(out, `href="https://jira.example.org/browse/VEN-42"`) {
		t.Fatalf("day view without link:\n%s", out)
	}
	// remove
	b.do("POST", "/setup/tickets", url.Values{"op": {"remove"}, "project": {"VEN"}})
	if c := config(t, e); len(c.Tickets) != 1 || c.Tickets[0].Project != "ATL" {
		t.Fatalf("after remove: %+v", c.Tickets)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRenameActivityFollowsRules(t *testing.T) {
	e, b := setupBrowser(t)
	r, _ := e.do("PUT", "/api/config", `version: 1
hats:
  venom:
    name: VENOM
    activities: [development, review]
    rules:
      - match: {title: venom}
        activity: development
`)
	if r.StatusCode != 200 {
		t.Fatalf("config: %d", r.StatusCode)
	}
	resp, _ := b.do("POST", "/setup/hat", hatForm(map[string]string{"act.0": "dev"}))
	if resp.StatusCode != 303 {
		t.Fatalf("rename: %d", resp.StatusCode)
	}
	h, _ := config(t, e).Hat("venom")
	if len(h.Rules) != 1 || len(h.Rules[0].Activity) != 1 || h.Rules[0].Activity[0] != "dev" {
		t.Fatalf("rules after rename: %+v", h.Rules)
	}
}
