package core

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"
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
	// Devices switches sensors on or off per device.
	Devices map[string]map[string]bool `yaml:"devices,omitempty" json:"devices,omitempty"`
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
	}{{"ignore", o.Ignore}, {"redact", o.Redact}} {
		for _, a := range l.list {
			if strings.TrimSpace(a) == "" {
				return fmt.Errorf("config: observe.%s: empty entry", l.name)
			}
		}
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
	return nil
}
