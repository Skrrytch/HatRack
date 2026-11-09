package core

import (
	"maps"
	"strings"
	"testing"
)

func TestGrantsOffByDefault(t *testing.T) {
	var o Observe
	for _, g := range Grants {
		if lvl := o.Granted("laptop", g.ID); lvl != "" {
			t.Errorf("%s: granted %q without configuration", g.ID, lvl)
		}
	}
}

func TestGrantCatalog(t *testing.T) {
	seen := map[string]bool{}
	for _, g := range Grants {
		if !sensorRe.MatchString(g.ID) || seen[g.ID] {
			t.Errorf("grant id %q invalid or twice", g.ID)
		}
		seen[g.ID] = true
		if len(g.Levels) == 0 {
			t.Errorf("%s: no levels", g.ID)
		}
		if len(g.Attrs) == 0 && g.Sensor == "" {
			t.Errorf("%s: governs nothing", g.ID)
		}
		if g.Needs != "" {
			if _, ok := GrantByID(g.Needs); !ok {
				t.Errorf("%s: needs unknown grant %q", g.ID, g.Needs)
			}
		}
	}
}

func TestGrantedNeeds(t *testing.T) {
	o := Observe{Grants: map[string]map[string]string{"laptop": {GrantAISessions: LevelOn}}}
	if o.Granted("laptop", GrantAISessions) != "" {
		t.Error("ai_sessions counts without workdir")
	}
	o.Grants["laptop"][GrantWorkdir] = LevelKnownKeys
	if o.Granted("laptop", GrantAISessions) != LevelOn {
		t.Error("ai_sessions with workdir not granted")
	}
	if o.Granted("desk", GrantWorkdir) != "" {
		t.Error("a grant applies to another device")
	}
}

func TestGrantsValidate(t *testing.T) {
	for _, c := range []struct {
		grants map[string]map[string]string
		err    string
	}{
		{map[string]map[string]string{"laptop": {GrantWorkdir: LevelAll}}, ""},
		{map[string]map[string]string{"laptop": {"keys": "on"}}, "unknown grant"},
		{map[string]map[string]string{"laptop": {GrantWorkdir: "on"}}, "level"},
		{map[string]map[string]string{" ": {GrantWorkdir: LevelAll}}, "empty device"},
	} {
		err := Observe{Grants: c.grants}.Validate()
		if c.err == "" && err != nil || c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)) {
			t.Errorf("%v: got %v, want %q", c.grants, err, c.err)
		}
	}
}

func TestFilterGranted(t *testing.T) {
	isKey := func(s string) bool { return strings.Contains(s, "ACME-") }
	span := Attrs{"app": "kitty", "title": "zsh", AttrRepo: "shop", AttrBranch: "feature/ACME-101-login", AttrBranchSrc: "proc"}
	session := Attrs{"app": "kitty", AttrRepo: "shop", AttrBranch: "feature/ACME-7", AttrBranchSrc: BranchFromSession}
	other := Attrs{"app": "kitty", AttrRepo: "shop", AttrBranch: "spike/try", AttrBranchSrc: "proc"}
	grants := func(m map[string]string) Observe {
		return Observe{Grants: map[string]map[string]string{"laptop": m}}
	}
	for _, c := range []struct {
		name string
		o    Observe
		in   Attrs
		want Attrs
	}{
		{"no grant", Observe{}, span, Attrs{"app": "kitty", "title": "zsh"}},
		{"all keeps a key branch", grants(map[string]string{GrantWorkdir: LevelAll}), span, span},
		{"all", grants(map[string]string{GrantWorkdir: LevelAll}), other, other},
		{"known key kept", grants(map[string]string{GrantWorkdir: LevelKnownKeys}), span, span},
		{"unknown branch dropped", grants(map[string]string{GrantWorkdir: LevelKnownKeys}), other,
			Attrs{"app": "kitty", AttrRepo: "shop", AttrBranchSrc: "proc"}},
		{"session without its grant", grants(map[string]string{GrantWorkdir: LevelAll}), session, Attrs{"app": "kitty"}},
		{"session with its grant", grants(map[string]string{GrantWorkdir: LevelAll, GrantAISessions: LevelOn}), session, session},
	} {
		dev := "laptop"
		before := maps.Clone(c.in)
		got, keep := c.o.FilterGranted(dev, "window", c.in, isKey)
		if !keep || !maps.Equal(got, c.want) {
			t.Errorf("%s: got %v %v, want %v", c.name, got, keep, c.want)
		}
		if !maps.Equal(c.in, before) {
			t.Errorf("%s: input changed", c.name)
		}
	}
	// the server check is skipped when no key test is given (agent)
	o := grants(map[string]string{GrantWorkdir: LevelKnownKeys})
	if got, _ := o.FilterGranted("laptop", "window", other, nil); !maps.Equal(got, other) {
		t.Errorf("without key test: %v", got)
	}
	if got, _ := o.FilterGranted("desk", "window", span, isKey); got[AttrBranch] != "" || got[AttrRepo] != "" {
		t.Errorf("grant of laptop applied to desk: %v", got)
	}
}

func TestBranchKeys(t *testing.T) {
	tk := NewTickets(nil, []TicketSystem{{Project: "ACME"}, {System: "GitHub", Pattern: `(gh)-[0-9]+`}})
	for branch, want := range map[string]string{
		"feature/ACME-101-login":  "ACME-101",
		"feature/acme-101-login":  "ACME-101",
		"ACME-7":                  "ACME-7",
		"bugfix/ACME-1_ACME-22":   "ACME-1,ACME-22",
		"release/ACME-1/ACME-1-b": "ACME-1",
		"feature/UTF-8-encoding":  "",
		"spike/try":               "",
		"feature/xACME-1":         "",
		"fix/gh-12-crash":         "gh-12",
		"main":                    "",
	} {
		if got := strings.Join(tk.BranchKeys(branch), ","); got != want {
			t.Errorf("%s: %q, want %q", branch, got, want)
		}
	}
}
