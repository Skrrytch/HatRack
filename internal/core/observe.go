package core

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Attrs are the attributes of an observation, e.g. app, title, state.
type Attrs map[string]string

// AttrPrivate with value "1" marks an ignored app; such a span has no other
// attributes.
const AttrPrivate = "private"

// Observation is a span of one sensor with constant attributes. Device is set
// by the server from the token, never trusted from the agent. ID is a UUID
// from the agent and the basis for upserts.
type Observation struct {
	ID     string    `json:"id"`
	Device string    `json:"device,omitempty"`
	Sensor string    `json:"sensor"`
	Start  time.Time `json:"start"`
	End    time.Time `json:"end"`
	Attrs  Attrs     `json:"attrs"`
}

// Retention limits in days.
const (
	DefaultRetentionDays = 28
	MaxRetentionDays     = 365
)

// Observe are the settings for recording observations.
type Observe struct {
	// RetentionDays 0 (unset) means DefaultRetentionDays.
	RetentionDays int `yaml:"retention_days,omitempty" json:"retention_days,omitempty"`
	// Ignore lists apps that are recorded as private spans without details.
	Ignore []string `yaml:"ignore,omitempty" json:"ignore,omitempty"`
	// Redact lists apps whose window title is not recorded.
	Redact []string `yaml:"redact,omitempty" json:"redact,omitempty"`
	// IgnorePaths are folders (and everything below) the workdir reader
	// never looks into; "~" is the home folder.
	IgnorePaths []string `yaml:"ignore_paths,omitempty" json:"ignore_paths,omitempty"`
	// Devices switches sensors on or off per device.
	Devices map[string]map[string]bool `yaml:"devices,omitempty" json:"devices,omitempty"`
	// Grants gives a device more than app, title and absence: grant id ->
	// level (grants.go). A missing entry is off.
	Grants map[string]map[string]string `yaml:"grants,omitempty" json:"grants,omitempty"`
	// Idle sets when an absence stops the clock and when it is asked about.
	Idle Idle `yaml:"idle,omitempty" json:"idle,omitzero"`
	// Helpers lists apps that only serve the work around them (password
	// prompt, file dialog); their spans count for the window before. nil
	// means DefaultHelpers, an empty list none (absorb.go).
	Helpers *[]string `yaml:"helpers,omitempty" json:"helpers,omitempty"`
	// GlanceSeconds: a short side trip between two spans of the same window
	// counts for that window if it is shorter than this; 0 is the default.
	GlanceSeconds int `yaml:"glance_seconds,omitempty" json:"glance_seconds,omitempty"`
}

// Idle are the absence settings, in minutes; 0 selects the default.
type Idle struct {
	After     int   `yaml:"after,omitempty" json:"after,omitempty"`           // minutes without input = away; default 5
	AskAfter  int   `yaml:"ask_after,omitempty" json:"ask_after,omitempty"`   // default 10
	StopAfter int   `yaml:"stop_after,omitempty" json:"stop_after,omitempty"` // default 15
	Ask       *bool `yaml:"ask,omitempty" json:"ask,omitempty"`               // default true
}

// Defaults of the absence settings, in minutes, and their upper limit.
const (
	DefaultIdleAfter     = 5
	DefaultIdleAskAfter  = 10
	DefaultIdleStopAfter = 15
	MaxIdleMinutes       = 1440
)

func idleMinutes(v, def int) time.Duration {
	if v == 0 {
		v = def
	}
	return time.Duration(v) * time.Minute
}

// AfterD is the inactivity after which the user counts as away.
func (i Idle) AfterD() time.Duration { return idleMinutes(i.After, DefaultIdleAfter) }

// AskAfterD is the absence from which the return is asked about.
func (i Idle) AskAfterD() time.Duration { return idleMinutes(i.AskAfter, DefaultIdleAskAfter) }

// StopAfterD is the absence after which the clock is stopped.
func (i Idle) StopAfterD() time.Duration { return idleMinutes(i.StopAfter, DefaultIdleStopAfter) }

// Asks reports whether a return is asked about (default yes).
func (i Idle) Asks() bool { return i.Ask == nil || *i.Ask }

// Validate checks the ranges and that After is not later than the others.
func (i Idle) Validate() error {
	for _, v := range []struct {
		name string
		n    int
	}{{"after", i.After}, {"ask_after", i.AskAfter}, {"stop_after", i.StopAfter}} {
		if v.n < 0 || v.n > MaxIdleMinutes {
			return fmt.Errorf("config: observe.idle.%s %d: want 0 or 1 to %d", v.name, v.n, MaxIdleMinutes)
		}
	}
	if i.AfterD() > i.AskAfterD() {
		return fmt.Errorf("config: observe.idle.after must not be later than ask_after")
	}
	if i.AfterD() > i.StopAfterD() {
		return fmt.Errorf("config: observe.idle.after must not be later than stop_after")
	}
	return nil
}

var sensorRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// Retention returns how long observations are kept.
func (o Observe) Retention() time.Duration {
	d := o.RetentionDays
	if d == 0 {
		d = DefaultRetentionDays
	}
	return time.Duration(d) * 24 * time.Hour
}

// Enabled reports whether a sensor records on a device. The idle sensor is on
// unless switched off; every other sensor is on only if switched on.
func (o Observe) Enabled(device, sensor string) bool {
	on, set := o.Devices[device][sensor]
	if sensor == "idle" {
		return !set || on
	}
	return set && on
}

// Ignored reports whether the app is on the ignore list.
func (o Observe) Ignored(app string) bool { return listHas(o.Ignore, app) }

// Redacted reports whether the app is on the redact list.
func (o Observe) Redacted(app string) bool { return listHas(o.Redact, app) }

func listHas(list []string, app string) bool {
	app = strings.TrimSpace(app)
	if app == "" {
		return false
	}
	for _, x := range list {
		if strings.EqualFold(strings.TrimSpace(x), app) {
			return true
		}
	}
	return false
}

// Validate checks the observe settings.
func (o Observe) Validate() error {
	if o.RetentionDays < 0 || o.RetentionDays > MaxRetentionDays {
		return fmt.Errorf("config: observe.retention_days %d: want 0 to %d", o.RetentionDays, MaxRetentionDays)
	}
	for _, l := range []struct {
		name string
		list []string
	}{{"ignore", o.Ignore}, {"redact", o.Redact}, {"ignore_paths", o.IgnorePaths}, {"helpers", o.HelperApps()}} {
		for _, a := range l.list {
			if strings.TrimSpace(a) == "" {
				return fmt.Errorf("config: observe.%s: empty entry", l.name)
			}
		}
	}
	if o.GlanceSeconds < 0 || o.GlanceSeconds > MaxGlanceSeconds {
		return fmt.Errorf("config: observe.glance_seconds %d: want 0 to %d", o.GlanceSeconds, MaxGlanceSeconds)
	}
	if err := o.Idle.Validate(); err != nil {
		return err
	}
	// sorted, so the first error reported is always the same
	devs := slices.Sorted(maps.Keys(o.Devices))
	for _, dev := range devs {
		if strings.TrimSpace(dev) == "" {
			return fmt.Errorf("config: observe.devices: empty device name")
		}
		for _, s := range slices.Sorted(maps.Keys(o.Devices[dev])) {
			if !sensorRe.MatchString(s) {
				return fmt.Errorf("config: observe.devices.%s: sensor %q: use lower case letters, digits and '_'", dev, s)
			}
		}
	}
	return o.validateGrants()
}

// Observation limits.
const (
	MaxObservationBatch  = 500
	MaxObservationSpan   = 24 * time.Hour
	MaxObservationAttrs  = 16
	MaxAttrValue         = 2048 // bytes
	ObservationClockSkew = 10 * time.Minute
)

var (
	obsIDRe      = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	obsAttrKeyRe = regexp.MustCompile(`^[a-z_]{1,32}$`)
)

// Validate checks one span against the limits; now is the receiver's clock.
func (o Observation) Validate(now time.Time) error {
	if !obsIDRe.MatchString(o.ID) {
		return fmt.Errorf("observation %q: id is not a UUID", o.ID)
	}
	if !sensorRe.MatchString(o.Sensor) {
		return fmt.Errorf("observation %s: sensor %q: use lower case letters, digits and '_'", o.ID, o.Sensor)
	}
	if o.End.Before(o.Start) {
		return fmt.Errorf("observation %s: end is before start", o.ID)
	}
	if o.End.Sub(o.Start) > MaxObservationSpan {
		return fmt.Errorf("observation %s: span longer than %s", o.ID, MaxObservationSpan)
	}
	if o.End.After(now.Add(ObservationClockSkew)) {
		return fmt.Errorf("observation %s: end is in the future", o.ID)
	}
	if len(o.Attrs) > MaxObservationAttrs {
		return fmt.Errorf("observation %s: more than %d attrs", o.ID, MaxObservationAttrs)
	}
	for _, k := range slices.Sorted(maps.Keys(o.Attrs)) {
		if !obsAttrKeyRe.MatchString(k) {
			return fmt.Errorf("observation %s: attr key %q: use 1 to 32 lower case letters and '_'", o.ID, k)
		}
		v := o.Attrs[k]
		if len(v) > MaxAttrValue {
			return fmt.Errorf("observation %s: attr %q longer than %d bytes", o.ID, k, MaxAttrValue)
		}
		if !utf8.ValidString(v) {
			return fmt.Errorf("observation %s: attr %q is not valid UTF-8", o.ID, k)
		}
	}
	return nil
}

// titleCounter matches an unread counter in front of a title: "(3) ", "[12] ".
var titleCounter = regexp.MustCompile(`^(\(\d+\)|\[\d+\])\s*`)

// titleNoise reports runes that carry status, not meaning, at the edges of
// a window title: pictographic symbols (spinners, dots, emoji), their
// modifiers, and the usual "unsaved" or bullet marks. Math symbols stay,
// so "C++" keeps its pluses.
func titleNoise(r rune) bool {
	return unicode.IsSpace(r) || unicode.In(r, unicode.So, unicode.Sk, unicode.Mn, unicode.Cf) ||
		strings.ContainsRune("*•·", r)
}

// NormalizeTitle removes what changes in a window title without changing
// its meaning, so "◑ Task" and "✳ Task" are the same title: symbols and
// marks at both ends, a leading unread counter, and repeated spaces.
// Changes inside the title (timers, percentages) stay.
func NormalizeTitle(s string) string {
	for {
		t := strings.TrimFunc(s, titleNoise)
		t = titleCounter.ReplaceAllString(t, "")
		if t == s {
			break
		}
		s = t
	}
	return strings.Join(strings.Fields(s), " ")
}
