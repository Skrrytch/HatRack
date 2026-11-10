package core

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestAbsorb(t *testing.T) {
	t0 := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	type piece struct {
		dev, app, title string
		secs            int
	}
	// read lays the pieces end to end (a dev "-" leaves a gap of a minute)
	// and returns the read spans as "app secs [absorbed apps]".
	read := func(t *testing.T, cfg Config, ps ...piece) []string {
		t.Helper()
		mustValid(t, cfg)
		var obs []Observation
		at := t0
		for _, p := range ps {
			if p.dev == "-" {
				at = at.Add(time.Minute)
				continue
			}
			d := time.Duration(p.secs) * time.Second
			obs = append(obs, Observation{Sensor: "window", Device: p.dev, Start: at, End: at.Add(d), Attrs: win(p.app, p.title)})
			if p.dev != "b" { // device b runs alongside, it does not move the clock
				at = at.Add(d)
			}
		}
		var got []string
		for _, r := range ReadDay(obs, nil, NewClassifier(cfg, Knowledge{}, Tickets{})) {
			s := r.Attrs["app"] + " " + strings.TrimSuffix(r.End.Sub(r.Start).String(), "s")
			var in []string
			for _, a := range r.Absorbed {
				in = append(in, a.Attrs["app"])
			}
			if len(in) > 0 {
				s += " [" + strings.Join(in, ",") + "]"
			}
			got = append(got, s)
		}
		return got
	}
	check := func(t *testing.T, got []string, want ...string) {
		t.Helper()
		if !slices.Equal(got, want) {
			t.Fatalf("got\n%q\nwant\n%q", got, want)
		}
	}
	a := func(app string, secs int) piece { return piece{"a", app, app + " title", secs} }

	t.Run("helper of any length", func(t *testing.T) {
		check(t, read(t, clsCfg(), a("Guake", 60), a("SshAskpass", 40), a("code", 60)),
			"Guake 1m40 [SshAskpass]", "code 1m0")
	})
	t.Run("window only passed", func(t *testing.T) {
		check(t, read(t, clsCfg(), a("Guake", 60), a("teams", 1), a("code", 60)),
			"Guake 1m1 [teams]", "code 1m0")
	})
	t.Run("side trip and back", func(t *testing.T) {
		check(t, read(t, clsCfg(), a("code", 60), a("teams", 3), a("code", 30)),
			"code 1m3 [teams]", "code 30")
	})
	t.Run("several short pieces and back", func(t *testing.T) {
		check(t, read(t, clsCfg(), a("code", 60), a("teams", 2), a("nemo", 2), a("code", 30)),
			"code 1m4 [teams,nemo]", "code 30")
	})
	t.Run("side trip too long", func(t *testing.T) {
		check(t, read(t, clsCfg(), a("code", 60), a("teams", 3), a("nemo", 3), a("code", 30)),
			"code 1m0", "teams 3", "nemo 3", "code 30")
	})
	t.Run("short but not back", func(t *testing.T) {
		check(t, read(t, clsCfg(), a("code", 60), a("teams", 3), a("firefox", 30)),
			"code 1m0", "teams 3", "firefox 30")
	})
	t.Run("a recognised span stays", func(t *testing.T) {
		cfg := clsCfg()
		hatRule(&cfg, "venom", Rule{Match: map[string]string{MatchTitle: "ticket"}})
		check(t, read(t, cfg, a("code", 60), piece{"a", "firefox", "ticket", 1}, a("SshAskpass", 5), a("code", 30)),
			"code 1m0", "firefox 6 [SshAskpass]", "code 30")
	})
	t.Run("an absence ends it", func(t *testing.T) {
		check(t, read(t, clsCfg(), a("code", 60), piece{dev: "-"}, a("SshAskpass", 5)),
			"code 1m0", "SshAskpass 5")
	})
	t.Run("per device", func(t *testing.T) {
		check(t, read(t, clsCfg(), a("code", 60), piece{"b", "mail", "inbox", 30}, a("teams", 1), a("code", 10)),
			"code 1m1 [teams]", "mail 30", "code 10")
	})
	t.Run("own helper list and glance", func(t *testing.T) {
		cfg := clsCfg()
		cfg.Observe.Helpers = &[]string{"gearlever"}
		cfg.Observe.GlanceSeconds = 10
		check(t, read(t, cfg, a("code", 60), a("gearlever", 30), a("teams", 6), a("code", 10)),
			"code 1m36 [gearlever,teams]", "code 10")
		cfg.Observe.Helpers = &[]string{}
		cfg.Observe.GlanceSeconds = 0
		check(t, read(t, cfg, a("code", 60), a("SshAskpass", 30), a("teams", 3)),
			"code 1m0", "SshAskpass 30", "teams 3")
	})
}

func TestAbsorbSettings(t *testing.T) {
	var o Observe
	if !o.Helper("sshaskpass") || o.Glance() != 5*time.Second {
		t.Fatal("defaults: helper list and 5 s")
	}
	for _, bad := range []Observe{{GlanceSeconds: -1}, {GlanceSeconds: MaxGlanceSeconds + 1}, {Helpers: &[]string{" "}}} {
		if bad.Validate() == nil {
			t.Errorf("%+v: want an error", bad)
		}
	}
}
