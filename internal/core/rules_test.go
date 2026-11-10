package core

import (
	"strings"
	"testing"
)

func TestMatchSpan(t *testing.T) {
	if s, e, ok, err := MatchSpan("Bill", "the billing service"); !ok || err != nil || s != 4 || e != 8 {
		t.Errorf("got %d %d %v %v", s, e, ok, err)
	}
	if _, _, ok, _ := MatchSpan("x", ""); ok {
		t.Error("empty value matched")
	}
	if _, _, _, err := MatchSpan("(", "a"); err == nil {
		t.Error("invalid pattern")
	}
}

func TestAssignRuleIDs(t *testing.T) {
	cfg := Config{Hats: []Hat{{Key: "a", Rules: []Rule{{ID: "own"}, {}}}, {Key: "b"}}, Defaults: Defaults{Rules: []Rule{{}, {}}}}
	orig := cfg.Hats[0].Rules
	if !AssignRuleIDs(&cfg) {
		t.Fatal("nothing assigned")
	}
	if orig[1].ID != "" {
		t.Error("changed the caller's list")
	}
	ids := []string{cfg.Hats[0].Rules[0].ID, cfg.Hats[0].Rules[1].ID, cfg.Defaults.Rules[0].ID, cfg.Defaults.Rules[1].ID}
	if ids[0] != "own" {
		t.Errorf("own id replaced: %v", ids)
	}
	seen := map[string]bool{}
	for _, id := range ids[1:] {
		if len(id) != 3 || strings.ContainsAny(id, "oli") || !ruleIDRe.MatchString(id) || seen[id] {
			t.Errorf("bad id %q in %v", id, ids)
		}
		seen[id] = true
	}
	if AssignRuleIDs(&cfg) {
		t.Error("assigned twice")
	}
	// when three characters run out, ids get longer
	used := map[string]bool{}
	for _, a := range ruleIDChars {
		for _, b := range ruleIDChars {
			for _, c := range ruleIDChars {
				used[string([]rune{a, b, c})] = true
			}
		}
	}
	if id := NewRuleID(used); len(id) != 4 {
		t.Errorf("full: %q", id)
	}
}
