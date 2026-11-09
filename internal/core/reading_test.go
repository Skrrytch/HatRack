package core

import (
	"slices"
	"testing"
	"time"
)

func TestReadDay(t *testing.T) {
	cfg := clsCfg()
	hatRule(&cfg, "venom", Rule{Match: map[string]string{MatchTitle: "venom"}})
	cfg.Defaults.Rules = []Rule{
		{Match: map[string]string{MatchApp: "^code$"}, Activity: Names{"Entwicklung"}},
		{Match: map[string]string{MatchApp: "spotify"}, Ignore: true},
	}
	mustValid(t, cfg)
	cls := NewClassifier(cfg, Knowledge{}, Tickets{})

	t0 := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }
	span := func(sensor string, from, to int, a Attrs) Observation {
		return Observation{Sensor: sensor, Device: "laptop", Start: at(from), End: at(to), Attrs: a}
	}
	obs := []Observation{
		span("window", 30, 40, win("Spotify", "Song")),
		span("window", 0, 20, win("code", "venom-api - Visual Studio Code")),
		span("idle", 10, 15, Attrs{"state": "away"}), // cuts the first span in two
		span("window", 20, 30, win("code", "billing - Visual Studio Code")),
		span("window", 40, 50, win("firefox", "Stack Overflow")),
	}
	iv := []Interval{
		// fleeting (30 s) around the middle of the first span: it would win
		// if it counted, since it comes first
		{Hat: "office", Start: at(5).Add(-15 * time.Second), End: at(5).Add(15 * time.Second)},
		{Hat: "venom", Start: at(0), End: at(25)},
		{Hat: "atlas", Start: at(25), Open: true},
	}
	rs := ReadDay(obs, iv, cls)

	var got []string
	for _, r := range rs {
		got = append(got, r.Start.Format("15:04")+" "+r.Cand.Kind()+" "+r.Cand.Level()+" "+r.Booked)
	}
	want := []string{
		"09:00 hat sure venom",
		"09:15 hat sure venom",
		"09:20 part  atlas", // activity only: no hat, no level; its middle 09:25 starts atlas
		"09:30 noise  atlas",
		"09:40 unknown  atlas",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got\n%q\nwant\n%q", got, want)
	}
	if rs[0].Differs() {
		t.Error("venom booked as venom differs")
	}

	// the same hat found as booked does not differ; another one does
	r := ReadSpan{Cand: Candidate{Hat: "venom"}, Booked: "atlas"}
	if !r.Differs() || (ReadSpan{Cand: Candidate{Hat: "venom"}}).Differs() {
		t.Error("Differs")
	}

	cov := CoverageOf(rs)
	if cov[KindHat] != 15*time.Minute || cov[KindPart] != 10*time.Minute || cov[KindNoise] != 10*time.Minute || cov[KindUnknown] != 10*time.Minute {
		t.Errorf("coverage %v", cov)
	}
	if p := cov.Percent(KindHat, KindPart, KindUnknown, KindNoise); !slices.Equal(p, []int{34, 22, 22, 22}) {
		t.Errorf("percent %v", p)
	}
	// a working day: the shares stay shares (8 h would give minutes, not percent, with ApportionTo)
	day := Coverage{KindHat: 5 * time.Hour, KindPart: 2 * time.Hour, KindUnknown: time.Hour}
	if p := day.Percent(KindHat, KindPart, KindUnknown, KindNoise); !slices.Equal(p, []int{63, 25, 12, 0}) {
		t.Errorf("working day percent %v", p)
	}
	if (Coverage{}).Percent(KindHat) != nil {
		t.Error("empty coverage has no shares")
	}
}

func TestCandidateLevel(t *testing.T) {
	for _, c := range []struct {
		cand Candidate
		want string
	}{
		{Candidate{}, ""},
		{Candidate{Hat: "x", Conf: ConfRule}, LevelSure},
		{Candidate{Hat: "x", Conf: ConfTicket}, LevelLikely},
		{Candidate{Hat: "x", Conf: ConfPart}, LevelMaybe},
	} {
		if got := c.cand.Level(); got != c.want {
			t.Errorf("%+v: %q, want %q", c.cand, got, c.want)
		}
	}
}
