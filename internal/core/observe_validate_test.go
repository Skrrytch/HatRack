package core

import (
	"strings"
	"testing"
	"time"
)

func TestObservationValidate(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	base := func() Observation {
		return Observation{ID: NewID(), Sensor: "window", Start: now.Add(-time.Hour), End: now.Add(-time.Minute), Attrs: Attrs{"app": "x"}}
	}
	manyAttrs := func(n int) Attrs {
		a := Attrs{}
		for i := 0; i < n; i++ {
			a["k"+strings.Repeat("_", i+1)] = "v"
		}
		return a
	}
	tests := []struct {
		name string
		mod  func(o *Observation)
		bad  bool
	}{
		{"valid", func(o *Observation) {}, false},
		{"no attrs", func(o *Observation) { o.Attrs = nil }, false},
		{"bad id", func(o *Observation) { o.ID = "nope" }, true},
		{"empty id", func(o *Observation) { o.ID = "" }, true},
		{"bad sensor", func(o *Observation) { o.Sensor = "Window" }, true},
		{"end before start", func(o *Observation) { o.End = o.Start.Add(-time.Second) }, true},
		{"empty span", func(o *Observation) { o.End = o.Start }, false},
		{"span 24h", func(o *Observation) { o.Start = o.End.Add(-24 * time.Hour) }, false},
		{"span 24h+1s", func(o *Observation) { o.Start = o.End.Add(-24*time.Hour - time.Second) }, true},
		{"end 9min ahead", func(o *Observation) { o.Start, o.End = now, now.Add(9*time.Minute) }, false},
		{"end 11min ahead", func(o *Observation) { o.Start, o.End = now, now.Add(11*time.Minute) }, true},
		{"16 attrs", func(o *Observation) { o.Attrs = manyAttrs(16) }, false},
		{"17 attrs", func(o *Observation) { o.Attrs = manyAttrs(17) }, true},
		{"bad key", func(o *Observation) { o.Attrs = Attrs{"Title": "x"} }, true},
		{"value 2048", func(o *Observation) { o.Attrs = Attrs{"title": strings.Repeat("a", 2048)} }, false},
		{"value 2049", func(o *Observation) { o.Attrs = Attrs{"title": strings.Repeat("a", 2049)} }, true},
		{"invalid utf8", func(o *Observation) { o.Attrs = Attrs{"title": "a\xffb"} }, true},
	}
	for _, tc := range tests {
		o := base()
		tc.mod(&o)
		err := o.Validate(now)
		if (err != nil) != tc.bad {
			t.Errorf("%s: err = %v, want error %v", tc.name, err, tc.bad)
		}
		if err != nil && o.ID != "" && !strings.Contains(err.Error(), o.ID) {
			t.Errorf("%s: message %q does not name the id", tc.name, err)
		}
	}
}
