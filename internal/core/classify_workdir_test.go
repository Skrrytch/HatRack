package core

import "testing"

// Repository and branch from the sensor workdir: rules match them, a ticket
// key in the branch is the detail and beats one in the title, the repository
// counts as a learned title part.
func TestClassifyWorkdir(t *testing.T) {
	tk := NewTickets(nil, []TicketSystem{{Project: "NMS", URL: "https://jira/browse/"}, {Project: "VEN", URL: "https://jira/browse/"}})
	term := func(title, repo, branch string) Attrs {
		a := win("Guake", title)
		if repo != "" {
			a[AttrRepo] = repo
		}
		if branch != "" {
			a[AttrBranch] = branch
		}
		return a
	}

	t.Run("rule on repo and branch", func(t *testing.T) {
		cfg := clsCfg()
		hatRule(&cfg, "venom", Rule{Match: map[string]string{MatchRepo: "^tsobss-"}, Activity: Names{"Entwicklung"}})
		hatRule(&cfg, "atlas", Rule{Match: map[string]string{MatchBranch: `^release/(\d+)`}, Detail: "Release $1"})
		c := classify(t, cfg, Knowledge{}, tk, term("Claude Code", "tsobss-nms", "main"))
		if c.Hat != "venom" || c.Activity != "Entwicklung" || sources(c) != "hat:rule,activity:rule" {
			t.Fatalf("repo rule: %+v", c)
		}
		c = classify(t, cfg, Knowledge{}, tk, term("Claude Code", "atlas", "release/42"))
		if c.Hat != "atlas" || c.Detail != "Release 42" {
			t.Fatalf("branch rule with $1: %+v", c)
		}
		// without the attribute the rule does not match
		if c := classify(t, cfg, Knowledge{}, tk, term("tsobss-nms", "", "")); c.Hat != "" {
			t.Fatalf("a repo rule matched a title: %+v", c)
		}
	})

	t.Run("ticket key in the branch", func(t *testing.T) {
		c := classify(t, clsCfg(), Knowledge{}, tk, term("[VEN-7] Board", "tsobss-nms", "feature/nms-19465-utc-timestamps"))
		if c.Detail != "NMS-19465" || sources(c) != "detail:ticket" {
			t.Fatalf("branch key first: %+v", c)
		}
		// a known project gives the hat it was booked on
		k := Knowledge{Projects: map[string]ProjectUse{"NMS": {Hat: "atlas", Share: 0.9, Count: 12}}}
		c = classify(t, clsCfg(), k, tk, term("Claude Code", "tsobss-nms", "feature/NMS-19465"))
		if c.Hat != "atlas" || c.Conf != ConfTicket || c.Detail != "NMS-19465" {
			t.Fatalf("project hat from the branch: %+v", c)
		}
		// unknown projects stay out
		if c := classify(t, clsCfg(), Knowledge{}, tk, term("x", "r", "feature/abc-12")); c.Detail != "" {
			t.Fatalf("unknown project: %+v", c)
		}
	})

	t.Run("repository as learned part", func(t *testing.T) {
		k := Knowledge{Parts: []LearnedPart{{Part: "tsobss-mesh", Hat: "venom", Share: 0.8, Minutes: 90}}}
		c := classify(t, clsCfg(), k, tk, term("Claude Code", "tsobss-mesh", "main"))
		if c.Hat != "venom" || c.Conf != ConfPart {
			t.Fatalf("repo part: %+v", c)
		}
	})

	t.Run("validation", func(t *testing.T) {
		cfg := clsCfg()
		hatRule(&cfg, "venom", Rule{Match: map[string]string{MatchBranch: "("}})
		if cfg.Validate() == nil {
			t.Error("a bad branch pattern passed")
		}
	})
}
