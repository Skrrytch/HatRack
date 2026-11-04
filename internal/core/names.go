package core

import (
	"fmt"
	"strings"
)

// Named is something that can be addressed by name: a hat (key and display
// name) or an activity (key and name are the same).
type Named struct {
	Key  string
	Name string
}

// NotFoundError is returned when no candidate matches.
type NotFoundError struct {
	Kind      string // "hat" or "activity", set by ResolveHat / ResolveActivity
	Input     string
	Available []string
}

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("%q not found (available: %s)", e.Input, strings.Join(e.Available, ", "))
}

// AmbiguousError is returned when several candidates match a prefix.
type AmbiguousError struct {
	Kind       string // "hat" or "activity"
	Input      string
	Candidates []string
}

func (e *AmbiguousError) Error() string {
	return fmt.Sprintf("%q is ambiguous (%s)", e.Input, strings.Join(e.Candidates, ", "))
}

// Resolve finds the candidate for input. Case is ignored. An exact match on
// key or name wins; otherwise exactly one prefix match wins. Key and name of
// the same candidate count once.
func Resolve(input string, items []Named) (Named, error) {
	in := strings.ToLower(strings.TrimSpace(input))
	var exact, prefix []Named
	for _, it := range items {
		k, n := strings.ToLower(it.Key), strings.ToLower(it.Name)
		switch {
		case in != "" && (k == in || n == in):
			exact = append(exact, it)
		case in != "" && (strings.HasPrefix(k, in) || strings.HasPrefix(n, in)):
			prefix = append(prefix, it)
		}
	}
	pick := func(c []Named) (Named, error, bool) {
		switch len(c) {
		case 0:
			return Named{}, nil, false
		case 1:
			return c[0], nil, true
		}
		names := make([]string, len(c))
		for i, x := range c {
			names[i] = x.Name
		}
		return Named{}, &AmbiguousError{Input: input, Candidates: names}, true
	}
	if n, err, ok := pick(exact); ok {
		return n, err
	}
	if n, err, ok := pick(prefix); ok {
		return n, err
	}
	avail := make([]string, len(items))
	for i, x := range items {
		avail[i] = x.Name
	}
	return Named{}, &NotFoundError{Input: input, Available: avail}
}
