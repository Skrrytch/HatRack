package core

import (
	"regexp"
	"sort"
	"strings"
	"time"
)

// Ticket keys (learned links), as in Ruumble: a detail with a link to an
// issue, https://jira.example/browse/TAG-1366, teaches that project TAG lives
// at https://jira.example/browse/. After that a plain "TAG-1366" is a link.
// Only the shape of the URL counts; nothing is ever fetched, Jira needs no
// configuration.

// TicketLinks maps a project to its base URL ending in "/browse/".
type TicketLinks map[string]string

var (
	// a link to an issue; no ? or # before /browse/
	issueURL = regexp.MustCompile("https?://[^\\s<>()\\[\\]{}\"'`?#]+?/browse/([A-Z][A-Z0-9]{1,9})-[1-9][0-9]{0,6}")
	// a key candidate; its surroundings are checked by keyAt
	ticketKeyRe = regexp.MustCompile(`([A-Z][A-Z0-9]{1,9})-([1-9][0-9]{0,6})`)
)

func wordByte(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

// endsKey reports whether a key or link ending at i is not followed by more
// of a word, key or file name ("TAG-1-2", "TAG-12x", "TAG-1.txt").
func endsKey(s string, i int) bool {
	if i >= len(s) {
		return true
	}
	if wordByte(s[i]) || s[i] == '-' {
		return false
	}
	return !(s[i] == '.' && i+1 < len(s) && wordByte(s[i+1]))
}

// keys finds the plain keys in a text: [start, end, project end] each, not
// inside a word, path or longer key ("x.TAG-1", "/browse/TAG-1" do not count).
func keys(s string) [][3]int {
	var out [][3]int
	for _, m := range ticketKeyRe.FindAllStringSubmatchIndex(s, -1) {
		if m[0] > 0 {
			if p := s[m[0]-1]; wordByte(p) || p == '.' || p == '/' || p == '-' {
				continue
			}
		}
		if !endsKey(s, m[1]) {
			continue
		}
		out = append(out, [3]int{m[0], m[1], m[3]})
	}
	return out
}

// LearnTicketLinks collects issue links in a text; the first base per
// project wins, and projects already known are kept.
func LearnTicketLinks(text string, into TicketLinks) TicketLinks {
	if into == nil {
		into = TicketLinks{}
	}
	for _, m := range issueURL.FindAllStringSubmatchIndex(text, -1) {
		if !endsKey(text, m[1]) {
			continue
		}
		project := text[m[2]:m[3]]
		if _, ok := into[project]; ok {
			continue
		}
		u := text[m[0]:m[1]]
		into[project] = u[:strings.LastIndex(u, "/browse/")+len("/browse/")]
	}
	return into
}

// ShortDetail shows a detail with issue links reduced to their keys:
// "https://jira.example/browse/TAG-1366 review" -> "TAG-1366 review".
func ShortDetail(text string) string {
	return issueURL.ReplaceAllStringFunc(text, func(u string) string {
		return u[strings.LastIndex(u, "/browse/")+len("/browse/"):]
	})
}

// DetailPart is a piece of a detail; Href is set for a key of a known
// project.
type DetailPart struct {
	Text, Href string
}

// SplitDetail shortens a detail (ShortDetail) and splits it into plain parts
// and linked keys of known projects.
func SplitDetail(text string, links TicketLinks) []DetailPart {
	text = ShortDetail(text)
	var parts []DetailPart
	last := 0
	for _, k := range keys(text) {
		base, ok := links[text[k[0]:k[2]]]
		if !ok {
			continue
		}
		if k[0] > last {
			parts = append(parts, DetailPart{Text: text[last:k[0]]})
		}
		parts = append(parts, DetailPart{Text: text[k[0]:k[1]], Href: base + text[k[0]:k[1]]})
		last = k[1]
	}
	if last < len(text) {
		parts = append(parts, DetailPart{Text: text[last:]})
	}
	return parts
}

// DetailURL is the link of the first known key in a detail, or "".
func DetailURL(text string, links TicketLinks) string {
	for _, p := range SplitDetail(text, links) {
		if p.Href != "" {
			return p.Href
		}
	}
	return ""
}

// RecentDetails lists, per hat and activity, the last details used since a
// time, newest first, at most n each, in their short form. The map is
// hat -> activity -> details.
func RecentDetails(events []Event, since time.Time, n int) map[string]map[string][]string {
	type use struct {
		hat, act, detail string
		at               time.Time
	}
	var uses []use
	for _, iv := range Intervals(events, time.Now()) {
		if iv.Detail != "" && !iv.End.Before(since) {
			uses = append(uses, use{iv.Hat, iv.Activity, ShortDetail(iv.Detail), iv.Start})
		}
	}
	sort.SliceStable(uses, func(a, b int) bool { return uses[a].at.After(uses[b].at) })
	out := map[string]map[string][]string{}
	for _, u := range uses {
		if out[u.hat] == nil {
			out[u.hat] = map[string][]string{}
		}
		list := out[u.hat][u.act]
		dup := false
		for _, d := range list {
			dup = dup || strings.EqualFold(d, u.detail)
		}
		if !dup && len(list) < n {
			out[u.hat][u.act] = append(list, u.detail)
		}
	}
	return out
}
