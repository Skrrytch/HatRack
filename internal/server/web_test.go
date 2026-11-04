package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"

	"hatrack/internal/core"
)

type browser struct {
	t *testing.T
	e *env
	c *http.Client
}

func newBrowser(t *testing.T, e *env) *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{t: t, e: e, c: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (b *browser) do(method, path string, form url.Values, hdr ...string) (*http.Response, string) {
	b.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, _ := http.NewRequest(method, b.e.ts.URL+path, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := b.c.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp, string(data)
}

func (b *browser) login(password string) *http.Response {
	r, _ := b.do("POST", "/login", url.Values{"password": {password}})
	return r
}

func withPassword(t *testing.T, e *env, pw string) {
	h, err := HashPassword(pw)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetPasswordHash(context.Background(), h); err != nil {
		t.Fatal(err)
	}
}

func seed(t *testing.T, e *env) {
	t.Helper()
	e.do("PUT", "/api/config", `version: 1
hats:
  venom:
    name: VENOM
    color: "#7a1fa2"
    activities: [development, review]
  tag:
    name: TAG
    activities: [support]
`)
	body, _ := json.Marshal([]core.Event{
		e.ev(-180, core.EvSwitch, "venom", "development"),
		e.ev(-120, core.EvActivity, "venom", "review"),
		e.ev(-60, core.EvSwitch, "tag", ""),
		e.ev(-30, core.EvOff, "tag", ""),
	})
	e.do("POST", "/api/events", string(body))
}

func TestLoginFlow(t *testing.T) {
	e := setup(t)
	b := newBrowser(t, e)

	// not signed in: redirect to the login page
	if r, _ := b.do("GET", "/", nil); r.StatusCode != 303 || r.Header.Get("Location") != "/login" {
		t.Fatalf("anonymous: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
	// no password set yet: the page says how to set one
	if _, out := b.do("GET", "/login", nil); !strings.Contains(out, "hat server user set-password") {
		t.Fatalf("login page without password: %s", out)
	}
	if r := b.login("anything"); r.StatusCode != 401 {
		t.Fatalf("login without a password set: %d", r.StatusCode)
	}

	withPassword(t, e, "s3cret")
	if r := b.login("wrong"); r.StatusCode != 401 {
		t.Fatalf("wrong password: %d", r.StatusCode)
	}
	r := b.login("s3cret")
	if r.StatusCode != 303 || r.Header.Get("Location") != "/" {
		t.Fatalf("login: %d", r.StatusCode)
	}
	var sess *http.Cookie
	for _, c := range r.Cookies() {
		if c.Name == sessionCookie {
			sess = c
		}
	}
	if sess == nil || !sess.HttpOnly || sess.SameSite != http.SameSiteStrictMode {
		t.Fatalf("session cookie: %+v", sess)
	}
	if r, out := b.do("GET", "/", nil); r.StatusCode != 200 || !strings.Contains(out, "HatRack") {
		t.Fatalf("after login: %d", r.StatusCode)
	}
	// logout ends the session
	b.do("POST", "/logout", url.Values{})
	if r, _ := b.do("GET", "/", nil); r.StatusCode != 303 {
		t.Fatalf("after logout: %d", r.StatusCode)
	}
	// a changed password ends other sessions
	b.login("s3cret")
	withPassword(t, e, "new")
	if r, _ := b.do("GET", "/", nil); r.StatusCode != 303 {
		t.Fatalf("session survived a password change: %d", r.StatusCode)
	}
}

func TestNowAndSwitchBar(t *testing.T) {
	e := setup(t)
	seed(t, e)
	withPassword(t, e, "pw")
	b := newBrowser(t, e)
	b.login("pw")

	_, out := b.do("GET", "/", nil)
	for _, want := range []string{"VENOM", "TAG", "paused", `name="op" value="resume"`, "development"} {
		if !strings.Contains(out, want) {
			t.Errorf("now page lacks %q", want)
		}
	}
	// resume, then pick a hat with an activity
	if r, _ := b.do("POST", "/action", url.Values{"op": {"resume"}}); r.StatusCode != 303 {
		t.Fatalf("resume: %d", r.StatusCode)
	}
	b.do("POST", "/action", url.Values{"op": {"on"}, "hat": {"venom"}, "activity": {"review"}})
	st, _ := e.st.CurrentState(context.Background())
	if st.Mode != core.ModeWearing || st.Hat != "venom" || st.Activity != "review" {
		t.Fatalf("state after the switch bar: %+v", st)
	}
	evs, _ := e.st.EventsBetween(context.Background(), time.Time{}, time.Time{})
	if last := evs[len(evs)-1]; last.Device != "web" {
		t.Fatalf("event device: %q", last.Device)
	}
	// errors come back on the page, not as events
	n := len(evs)
	r, _ := b.do("POST", "/action", url.Values{"op": {"on"}, "hat": {"venom"}, "activity": {"zzz"}})
	if !strings.Contains(r.Header.Get("Location"), "e=") {
		t.Fatalf("error redirect: %s", r.Header.Get("Location"))
	}
	if evs, _ = e.st.EventsBetween(context.Background(), time.Time{}, time.Time{}); len(evs) != n {
		t.Fatal("an error wrote an event")
	}
	// the same switch pushes to the agents' stream: covered by the SSE test via broadcastState
	b.do("POST", "/action", url.Values{"op": {"off"}, "neutral": {"1"}})
	if st, _ = e.st.CurrentState(context.Background()); st.Mode != core.ModePaused || !st.Neutral {
		t.Fatalf("off --neutral: %+v", st)
	}
}

func TestPostsNeedSessionAndSameOrigin(t *testing.T) {
	e := setup(t)
	seed(t, e)
	withPassword(t, e, "pw")

	anon := newBrowser(t, e)
	if r, _ := anon.do("POST", "/action", url.Values{"op": {"off"}}); r.StatusCode != 403 {
		t.Fatalf("anonymous post: %d", r.StatusCode)
	}
	b := newBrowser(t, e)
	b.login("pw")
	if r, _ := b.do("POST", "/action", url.Values{"op": {"resume"}}, "Origin", "https://evil.example"); r.StatusCode != 403 {
		t.Fatalf("cross-site post: %d", r.StatusCode)
	}
	if st, _ := e.st.CurrentState(context.Background()); st.Mode != core.ModePaused {
		t.Fatalf("a rejected post changed the state: %+v", st)
	}
	// a device token only gives the read-only view
	ro := newBrowser(t, e)
	if r, _ := ro.do("GET", "/?token="+e.token, nil); r.StatusCode != 303 {
		t.Fatalf("token: %d", r.StatusCode)
	}
	r, out := ro.do("GET", "/", nil)
	if r.StatusCode != 200 || strings.Contains(out, `action="/action"`) {
		t.Fatalf("read-only view: %d (switch bar must not be offered)", r.StatusCode)
	}
	if r, _ := ro.do("POST", "/action", url.Values{"op": {"resume"}}); r.StatusCode != 403 {
		t.Fatalf("read-only post: %d", r.StatusCode)
	}
}

func TestDayWeekAndCSV(t *testing.T) {
	e := setup(t)
	seed(t, e)
	withPassword(t, e, "pw")
	b := newBrowser(t, e)
	b.login("pw")
	date := e.now.Format("2006-01-02") // 2026-11-04, a Wednesday

	_, day := b.do("GET", "/day?date="+date, nil)
	for _, want := range []string{"VENOM", "development", "review", "TAG", "09:00–10:00", "1h 00m", "2h 00m", "2h 30m"} {
		if !strings.Contains(day, want) {
			t.Errorf("day page lacks %q", want)
		}
	}
	// the week page: bars per day, totals, comparison with the previous week
	_, week := b.do("GET", "/week?date="+date, nil)
	for _, want := range []string{"Mon 2026-11-02 – Sun 2026-11-08", "VENOM", "2h 00m", "2h 30m", "CSV", "/week.csv?date=2026-11-02"} {
		if !strings.Contains(week, want) {
			t.Errorf("week page lacks %q", want)
		}
	}
	// empty day
	if _, out := b.do("GET", "/day?date=2026-01-01", nil); !strings.Contains(out, "Nothing logged") {
		t.Fatalf("empty day: %s", out)
	}

	r, csvOut := b.do("GET", "/week.csv?date="+date, nil)
	if r.StatusCode != 200 || !strings.HasPrefix(r.Header.Get("Content-Type"), "text/csv") {
		t.Fatalf("csv: %d %s", r.StatusCode, r.Header.Get("Content-Type"))
	}
	lines := strings.Split(strings.TrimSpace(csvOut), "\n")
	if lines[0] != "date,hat,activity,start,end,minutes,note" || len(lines) != 4 {
		t.Fatalf("csv:\n%s", csvOut)
	}
	if lines[1] != "2026-11-04,VENOM,development,09:00,10:00,60," || lines[3] != "2026-11-04,TAG,,11:00,11:30,30," {
		t.Fatalf("csv rows:\n%s", csvOut)
	}
}

func TestLanguageAndTimeZone(t *testing.T) {
	e := setup(t)
	seed(t, e)
	withPassword(t, e, "pw")
	b := newBrowser(t, e)
	b.login("pw")

	if _, out := b.do("GET", "/", nil, "Accept-Language", "de-DE"); !strings.Contains(out, "pausiert seit") {
		t.Fatalf("german by header: %s", out)
	}
	// ?lang= sets the cookie and redirects without the parameter
	r, _ := b.do("GET", "/week?lang=en&date=2026-11-04", nil, "Accept-Language", "de")
	if r.StatusCode != 303 || r.Header.Get("Location") != "/week?date=2026-11-04" {
		t.Fatalf("lang redirect: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
	if _, out := b.do("GET", "/", nil, "Accept-Language", "de"); !strings.Contains(out, "paused since") {
		t.Fatalf("cookie should win over the header")
	}
	// the browser's time zone shifts the times shown
	u, _ := url.Parse(e.ts.URL)
	b.c.Jar.SetCookies(u, []*http.Cookie{{Name: tzCookie, Value: "Asia/Tokyo", Path: "/"}})
	if _, out := b.do("GET", "/day?date=2026-11-04", nil); !strings.Contains(out, "18:00–19:00") {
		t.Fatalf("time zone not applied:\n%s", out)
	}
}

func TestStaticAndLiveStream(t *testing.T) {
	e := setup(t)
	withPassword(t, e, "pw")
	b := newBrowser(t, e)
	if r, out := b.do("GET", "/static/app.js", nil); r.StatusCode != 200 || !strings.Contains(out, "EventSource") {
		t.Fatalf("app.js: %d", r.StatusCode)
	}
	if r, out := b.do("GET", "/static/hatrack.svg", nil); r.StatusCode != 200 || r.Header.Get("Content-Type") != "image/svg+xml" || !strings.Contains(out, "<svg") {
		t.Fatalf("hatrack.svg: %d %s", r.StatusCode, r.Header.Get("Content-Type"))
	}
	if r, _ := b.do("GET", "/static/nope.js", nil); r.StatusCode != 404 {
		t.Fatalf("missing static file: %d", r.StatusCode)
	}
	if r, _ := b.do("GET", "/ui/stream", nil); r.StatusCode != 303 {
		t.Fatalf("stream without login: %d", r.StatusCode)
	}
}
