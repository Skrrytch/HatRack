package core

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/goccy/go-yaml"
)

// ConfigVersion is the only supported configuration format version.
const ConfigVersion = 1

// Resume modes for a hat's activity.
const (
	ResumeNever = "never"
	ResumeLast  = "last"
	ResumeAsk   = "ask"
)

// ModuleCfg is the settings of one module, as written in the YAML file.
type ModuleCfg map[string]any

// Defaults are the global settings that hats fall back to.
type Defaults struct {
	ResumeActivity string `yaml:"resume_activity,omitempty" json:"resume_activity,omitempty"`
}

// Hat is one context. Its key is the stable identifier.
type Hat struct {
	Key              string               `yaml:"-" json:"key"`
	Name             string               `yaml:"name,omitempty" json:"name"`
	Color            string               `yaml:"color,omitempty" json:"color,omitempty"`
	ResumeActivity   string               `yaml:"resume_activity,omitempty" json:"resume_activity,omitempty"`
	Activities       []string             `yaml:"activities" json:"activities"`
	Archived         bool                 `yaml:"archived,omitempty" json:"archived,omitempty"`
	ArchivedActivity []string             `yaml:"archived_activities,omitempty" json:"archived_activities,omitempty"`
	Modules          map[string]ModuleCfg `yaml:"modules,omitempty" json:"modules,omitempty"`
}

// DisplayName returns the name, or the key if no name is set.
func (h Hat) DisplayName() string {
	if h.Name != "" {
		return h.Name
	}
	return h.Key
}

// HatList keeps the order of hats from the file; the order is the sort order.
type HatList []Hat

// MarshalYAML writes the list as a mapping from key to hat.
func (l HatList) MarshalYAML() (interface{}, error) {
	ms := make(yaml.MapSlice, 0, len(l))
	for _, h := range l {
		ms = append(ms, yaml.MapItem{Key: h.Key, Value: h})
	}
	return ms, nil
}

// UnmarshalYAML reads a mapping from key to hat, keeping the order.
func (l *HatList) UnmarshalYAML(b []byte) error {
	var ms yaml.MapSlice
	if err := yaml.UnmarshalWithOptions(b, &ms, yaml.UseOrderedMap()); err != nil {
		return err
	}
	out := make(HatList, 0, len(ms))
	for _, it := range ms {
		key := fmt.Sprint(it.Key)
		raw, err := yaml.Marshal(it.Value)
		if err != nil {
			return err
		}
		var h Hat
		if it.Value != nil {
			if err := yaml.Unmarshal(raw, &h); err != nil {
				return fmt.Errorf("hat %q: %w", key, err)
			}
		}
		h.Key = key
		out = append(out, h)
	}
	*l = out
	return nil
}

// Config is the whole configuration: hats, activities and module settings.
type Config struct {
	Version  int                  `yaml:"version" json:"version"`
	Defaults Defaults             `yaml:"defaults,omitempty" json:"defaults"`
	Modules  map[string]ModuleCfg `yaml:"modules,omitempty" json:"modules,omitempty"`
	Hats     HatList              `yaml:"hats" json:"hats"`
}

// NewConfig returns an empty configuration.
func NewConfig() Config { return Config{Version: ConfigVersion} }

var keyRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
var colorRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// ParseConfig reads and validates a YAML configuration.
func ParseConfig(data []byte) (Config, error) {
	var c Config
	if err := yaml.UnmarshalWithOptions(data, &c, yaml.Strict()); err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Validate checks the structure of the configuration.
func (c Config) Validate() error {
	if c.Version != ConfigVersion {
		return fmt.Errorf("config: unsupported version %d (expected %d)", c.Version, ConfigVersion)
	}
	if !validResume(c.Defaults.ResumeActivity) {
		return fmt.Errorf("config: defaults.resume_activity %q: want never, last or ask", c.Defaults.ResumeActivity)
	}
	seen := map[string]bool{}
	for _, h := range c.Hats {
		if !keyRe.MatchString(h.Key) {
			return fmt.Errorf("config: hat key %q: use lower case letters, digits, '-' and '_'", h.Key)
		}
		if seen[h.Key] {
			return fmt.Errorf("config: hat %q appears twice", h.Key)
		}
		seen[h.Key] = true
		if h.Color != "" && !colorRe.MatchString(h.Color) {
			return fmt.Errorf("config: hat %q: color %q: want #rrggbb", h.Key, h.Color)
		}
		if !validResume(h.ResumeActivity) {
			return fmt.Errorf("config: hat %q: resume_activity %q: want never, last or ask", h.Key, h.ResumeActivity)
		}
		acts := map[string]bool{}
		for _, a := range h.Activities {
			if strings.TrimSpace(a) == "" || a == "-" {
				return fmt.Errorf("config: hat %q: invalid activity %q", h.Key, a)
			}
			if acts[strings.ToLower(a)] {
				return fmt.Errorf("config: hat %q: activity %q appears twice", h.Key, a)
			}
			acts[strings.ToLower(a)] = true
		}
	}
	return nil
}

func validResume(s string) bool {
	return s == "" || s == ResumeNever || s == ResumeLast || s == ResumeAsk
}

// Export writes the configuration as YAML. Export followed by import is
// lossless.
func (c Config) Export() ([]byte, error) {
	if c.Version == 0 {
		c.Version = ConfigVersion
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf, yaml.Indent(2), yaml.IndentSequence(true))
	if err := enc.Encode(c); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Hat looks up a hat by key, archived or not.
func (c Config) Hat(key string) (Hat, bool) {
	for _, h := range c.Hats {
		if h.Key == key {
			return h, true
		}
	}
	return Hat{}, false
}

// ActiveHats returns the hats that are not archived.
func (c Config) ActiveHats() []Hat {
	var out []Hat
	for _, h := range c.Hats {
		if !h.Archived {
			out = append(out, h)
		}
	}
	return out
}

// ResumeMode returns the effective resume mode of a hat.
func (c Config) ResumeMode(h Hat) string {
	if h.ResumeActivity != "" {
		return h.ResumeActivity
	}
	if c.Defaults.ResumeActivity != "" {
		return c.Defaults.ResumeActivity
	}
	return ResumeNever
}

// ModuleConfig merges the module defaults with the hat's settings. The
// second result is false if the module is not configured or disabled.
func (c Config) ModuleConfig(h Hat, module string) (ModuleCfg, bool) {
	def, hasDef := c.Modules[module]
	own, hasOwn := h.Modules[module]
	if !hasDef && !hasOwn {
		return nil, false
	}
	m := ModuleCfg{}
	for k, v := range def {
		m[k] = v
	}
	for k, v := range own {
		m[k] = v
	}
	if en, ok := m["enabled"].(bool); ok && !en {
		return nil, false
	}
	return m, true
}

// ModuleIDs returns all module ids used anywhere in the configuration.
func (c Config) ModuleIDs() []string {
	set := map[string]bool{}
	for id := range c.Modules {
		set[id] = true
	}
	for _, h := range c.Hats {
		for id := range h.Modules {
			set[id] = true
		}
	}
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Merge applies an imported configuration to the current one: new hats and
// activities are added, existing ones updated, missing ones kept. With
// replace, whatever the import does not contain is archived (never deleted,
// because the log refers to it). It returns the result and a list of changes.
func Merge(cur, in Config, replace bool) (Config, []string) {
	var changes []string
	out := Config{Version: ConfigVersion, Defaults: cur.Defaults, Modules: copyModules(cur.Modules)}

	if in.Defaults.ResumeActivity != "" && in.Defaults.ResumeActivity != cur.Defaults.ResumeActivity {
		out.Defaults.ResumeActivity = in.Defaults.ResumeActivity
		changes = append(changes, "defaults.resume_activity: "+in.Defaults.ResumeActivity)
	}
	if replace {
		for id := range out.Modules {
			if _, ok := in.Modules[id]; !ok {
				delete(out.Modules, id)
				changes = append(changes, "module removed: "+id)
			}
		}
	}
	for id, m := range in.Modules {
		if out.Modules == nil {
			out.Modules = map[string]ModuleCfg{}
		}
		if !sameJSON(out.Modules[id], m) {
			changes = append(changes, "module settings: "+id)
		}
		out.Modules[id] = m
	}

	index := map[string]Hat{}
	for _, h := range cur.Hats {
		index[h.Key] = h
	}
	used := map[string]bool{}
	var result HatList
	for _, ih := range in.Hats {
		used[ih.Key] = true
		old, exists := index[ih.Key]
		if !exists {
			ih.ArchivedActivity = nil
			result = append(result, ih)
			changes = append(changes, "hat added: "+ih.Key)
			continue
		}
		merged := mergeHat(old, ih, replace)
		if !sameJSON(old, merged) {
			changes = append(changes, "hat updated: "+ih.Key)
		}
		result = append(result, merged)
	}
	for _, old := range cur.Hats {
		if used[old.Key] {
			continue
		}
		if replace && !old.Archived {
			old.Archived = true
			changes = append(changes, "hat archived: "+old.Key)
		}
		result = append(result, old)
	}
	out.Hats = result
	return out, changes
}

func mergeHat(old, in Hat, replace bool) Hat {
	h := old
	h.Name, h.Color, h.ResumeActivity = in.Name, in.Color, in.ResumeActivity
	h.Archived = in.Archived
	if replace || len(in.Modules) > 0 {
		if replace {
			h.Modules = nil
		}
		for id, m := range in.Modules {
			if h.Modules == nil {
				h.Modules = map[string]ModuleCfg{}
			}
			h.Modules[id] = m
		}
	}
	// Activities: the import's order first, then the ones it does not mention.
	inSet := map[string]bool{}
	var acts []string
	for _, a := range in.Activities {
		inSet[strings.ToLower(a)] = true
		acts = append(acts, a)
	}
	var archived []string
	for _, a := range append(append([]string(nil), old.Activities...), old.ArchivedActivity...) {
		if inSet[strings.ToLower(a)] {
			continue
		}
		wasArchived := false
		for _, x := range old.ArchivedActivity {
			if x == a {
				wasArchived = true
			}
		}
		if replace || wasArchived {
			archived = append(archived, a)
		} else {
			acts = append(acts, a)
		}
	}
	h.Activities, h.ArchivedActivity = acts, archived
	if h.Activities == nil {
		h.Activities = []string{}
	}
	return h
}

func copyModules(m map[string]ModuleCfg) map[string]ModuleCfg {
	if m == nil {
		return nil
	}
	out := make(map[string]ModuleCfg, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// ExpandEnv replaces ${env:NAME} in all strings of a module configuration.
// Values stay references in stored and exported configuration; only the agent
// expands them when it applies a switch.
func ExpandEnv(v any) any {
	switch x := v.(type) {
	case string:
		return envRe.ReplaceAllStringFunc(x, func(m string) string {
			return os.Getenv(envRe.FindStringSubmatch(m)[1])
		})
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = ExpandEnv(e)
		}
		return out
	case ModuleCfg:
		return ModuleCfg(ExpandEnv(map[string]any(x)).(map[string]any))
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = ExpandEnv(e)
		}
		return out
	}
	return v
}

var envRe = regexp.MustCompile(`\$\{env:([A-Za-z_][A-Za-z0-9_]*)\}`)
var secretKeyRe = regexp.MustCompile(`(?i)(token|password|passwd|secret|api_?key)`)

// Warnings returns hints about a configuration that is valid but suspicious:
// secrets in plain text and referenced files that do not exist on this
// machine. Files are checked where the import happens.
func (c Config) Warnings(checkFiles bool) []string {
	var w []string
	walk := func(where string, m ModuleCfg) {
		for _, k := range sortedKeys(m) {
			v := m[k]
			s, ok := v.(string)
			if !ok {
				continue
			}
			if secretKeyRe.MatchString(k) && !envRe.MatchString(s) && s != "" {
				w = append(w, fmt.Sprintf("%s.%s looks like a secret in plain text; use ${env:NAME}", where, k))
			}
			if checkFiles && (k == "image" || k == "file" || k == "path") && !envRe.MatchString(s) {
				if _, err := os.Stat(ExpandPath(s)); err != nil {
					w = append(w, fmt.Sprintf("%s.%s: file not found: %s", where, k, s))
				}
			}
		}
	}
	for _, id := range sortedModuleKeys(c.Modules) {
		walk("modules."+id, c.Modules[id])
	}
	for _, h := range c.Hats {
		for _, id := range sortedModuleKeys(h.Modules) {
			walk("hats."+h.Key+".modules."+id, h.Modules[id])
		}
	}
	return w
}

// ExpandPath replaces a leading ~ with the home directory.
func ExpandPath(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

func sortedKeys(m ModuleCfg) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedModuleKeys(m map[string]ModuleCfg) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
