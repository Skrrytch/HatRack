package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"hatrack/internal/core"
	"hatrack/internal/i18n"
)

// The Setup pages edit the configuration with forms. Every change goes through
// editConfig, so the same checks apply as for `hat config import`.

type option struct {
	Value, Label string
	Selected     bool
}

type setupHat struct {
	Key, Name, Color string
	Activities       int
	Archived         bool
	First, Last      bool
}

type setupView struct {
	Hats      []setupHat
	Archived  []setupHat
	NextColor string // a colour no hat uses yet, for the next new hat
	Devices   []deviceRow
	Import    importView
}

type deviceRow struct {
	Name   string
	Online bool
	Seen   string
}

type importView struct {
	Data     string
	Replace  bool
	Preview  bool
	Changes  []string
	Warnings []string
}

// field is one input of a module form, generated from the JSON Schema.
type field struct {
	Name, Label string
	Kind        string // text, number, bool, enum
	Value       string
	Inherited   string // the value from the defaults (hat page)
	Options     []option
	Required    bool
}

type moduleForm struct {
	ID, Title, Desc string
	Summary         string // what applies, shown while the section is closed
	Fields          []field
	Extra           string // YAML of settings the form cannot show
	ExtraNote       string
}

type hatEdit struct {
	Key, Name, Color string
	Resume           []option
	Activities       []actRow
	Archived         []string
	Modules          []moduleForm
	Saved            bool
}

type actRow struct {
	Index       int
	Name        string
	First, Last bool
}

type defaultsEdit struct {
	Resume    []option
	WorkStart string
	WorkEnd   string
	WorkDays  []option // Monday first
	Modules   []moduleForm
	Saved     bool
}

// ---- helpers ----------------------------------------------------------------

// tr translates key, or returns fallback if the catalog has no such key.
func tr(lang, key, fallback string) string {
	if s := i18n.In(lang, key); s != key {
		return s
	}
	return fallback
}

func resumeOptions(lang, cur string, isDefault bool, def string) []option {
	first := option{Value: "", Label: i18n.In(lang, "web.setup.resume.inherit", tr(lang, "web.setup.resume."+orNever(def), orNever(def)))}
	if isDefault {
		first = option{Value: "", Label: i18n.In(lang, "web.setup.resume.never")}
	}
	opts := []option{first}
	for _, v := range []string{core.ResumeNever, core.ResumeLast, core.ResumeAsk} {
		if isDefault && v == core.ResumeNever {
			continue
		}
		opts = append(opts, option{Value: v, Label: i18n.In(lang, "web.setup.resume."+v)})
	}
	for i := range opts {
		opts[i].Selected = opts[i].Value == cur
	}
	return opts
}

func orNever(s string) string {
	if s == "" {
		return core.ResumeNever
	}
	return s
}

var umlauts = strings.NewReplacer("ä", "ae", "ö", "oe", "ü", "ue", "ß", "ss", "Ä", "ae", "Ö", "oe", "Ü", "ue")

// slug makes a hat key from a name: lower case letters, digits and '-'.
func slug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(umlauts.Replace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case b.Len() > 0 && !dash:
			b.WriteByte('-')
			dash = true
		}
	}
	k := strings.TrimRight(b.String(), "-")
	if k == "" {
		k = "hat"
	}
	return k
}

func hatIndex(c *core.Config, key string) int {
	for i, h := range c.Hats {
		if h.Key == key {
			return i
		}
	}
	return -1
}

// userError turns a configuration error into text for the page.
func userError(lang string, err error) string {
	var bad badConfig
	if errors.As(err, &bad) {
		return i18n.In(lang, "web.setup.invalid", bad.Error())
	}
	return err.Error()
}

// ---- schema forms ---------------------------------------------------------------

type propSchema struct {
	Type        any    `json:"type"`
	Enum        []any  `json:"enum"`
	Description string `json:"description"`
}

type objSchema struct {
	Properties map[string]propSchema `json:"properties"`
	Required   []string              `json:"required"`
}

func (p propSchema) kind() string {
	switch {
	case len(p.Enum) > 0:
		return "enum"
	case p.Type == "boolean":
		return "bool"
	case p.Type == "number", p.Type == "integer":
		return "number"
	case p.Type == "string":
		return "text"
	}
	return "" // not shown as a field
}

func parseSchema(b []byte) objSchema {
	var s objSchema
	json.Unmarshal(b, &s)
	return s
}

// propOrder: "enabled" first, then required ones, then the rest by name.
func propOrder(s objSchema) []string {
	req := map[string]bool{}
	for _, r := range s.Required {
		req[r] = true
	}
	var keys []string
	for k := range s.Properties {
		keys = append(keys, k)
	}
	rank := func(k string) int {
		switch {
		case k == "enabled":
			return 0
		case req[k]:
			return 1
		}
		return 2
	}
	sort.Slice(keys, func(i, j int) bool {
		if rank(keys[i]) != rank(keys[j]) {
			return rank(keys[i]) < rank(keys[j])
		}
		return keys[i] < keys[j]
	})
	return keys
}

func valueString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return fmt.Sprint(v)
}

// moduleForms builds the forms for all known modules. own are the settings
// edited here, inherited (hat page only) the defaults behind them.
func (s *Server) moduleForms(lang string, own, inherited map[string]core.ModuleCfg, hatPage bool) []moduleForm {
	var out []moduleForm
	for _, m := range s.reg.All() {
		id := m.ID()
		sch := parseSchema(m.ConfigSchema())
		req := map[string]bool{}
		for _, r := range sch.Required {
			req[r] = true
		}
		f := moduleForm{ID: id, Title: tr(lang, "web.mod."+id, id), Desc: tr(lang, "web.moddesc."+id, "")}
		extra := core.ModuleCfg{}
		for k, v := range own[id] {
			if p, ok := sch.Properties[k]; !ok || p.kind() == "" {
				extra[k] = v
			}
		}
		for _, k := range propOrder(sch) {
			p := sch.Properties[k]
			kind := p.kind()
			if kind == "" {
				continue
			}
			fl := field{Name: "m/" + id + "/" + k, Label: tr(lang, "web.prop."+k, k), Kind: kind,
				Value: valueString(own[id][k]), Required: req[k] && !hatPage}
			if hatPage {
				fl.Inherited = valueString(inherited[id][k])
			}
			blank := i18n.In(lang, "web.setup.unset")
			if k == "enabled" {
				blank = i18n.In(lang, "web.setup.enabled_auto")
			}
			if fl.Inherited != "" {
				blank = i18n.In(lang, "web.setup.inherit", fl.Inherited)
			}
			switch kind {
			case "bool":
				fl.Options = []option{{Value: "", Label: blank}, {Value: "true", Label: i18n.In(lang, "web.setup.yes")}, {Value: "false", Label: i18n.In(lang, "web.setup.no")}}
			case "enum":
				fl.Options = []option{{Value: "", Label: blank}}
				for _, e := range p.Enum {
					fl.Options = append(fl.Options, option{Value: valueString(e), Label: valueString(e)})
				}
			}
			for i := range fl.Options {
				fl.Options[i].Selected = fl.Options[i].Value == fl.Value
			}
			f.Fields = append(f.Fields, fl)
		}
		f.Summary = summary(lang, f.Fields, hatPage)
		if len(extra) > 0 {
			b, _ := core.Config{Modules: map[string]core.ModuleCfg{id: extra}}.Export()
			f.Extra = string(b)
			f.ExtraNote = i18n.In(lang, "web.setup.extra")
		}
		out = append(out, f)
	}
	return out
}

// summary says in one line what a module section holds.
func summary(lang string, fields []field, hatPage bool) string {
	var own []string
	inherited := false
	for _, f := range fields {
		if f.Value != "" {
			v := f.Value
			for _, o := range f.Options {
				if o.Value == f.Value {
					v = o.Label
				}
			}
			own = append(own, f.Label+": "+v)
		}
		if f.Inherited != "" {
			inherited = true
		}
	}
	switch {
	case len(own) > 0 && hatPage && inherited:
		return strings.Join(own, " · ") + " · " + i18n.In(lang, "web.setup.sum_rest")
	case len(own) > 0:
		return strings.Join(own, " · ")
	case inherited:
		return i18n.In(lang, "web.setup.sum_default")
	}
	return i18n.In(lang, "web.setup.sum_none")
}

// splitList reads several names from one field: separated by commas,
// semicolons or new lines. Duplicates (ignoring case) are dropped.
func splitList(s string) []string {
	seen := map[string]bool{}
	var out []string
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == '\n' || r == '\r' }) {
		p := strings.TrimSpace(part)
		if p != "" && !seen[strings.ToLower(p)] {
			seen[strings.ToLower(p)] = true
			out = append(out, p)
		}
	}
	return out
}

// freeColor returns the first palette colour no hat uses yet.
func freeColor(cfg core.Config) string {
	used := map[string]bool{}
	for _, h := range cfg.Hats {
		used[strings.ToLower(h.Color)] = true
	}
	for _, c := range fallbackColors {
		if !used[c] {
			return c
		}
	}
	return fallbackColors[len(cfg.Hats)%len(fallbackColors)]
}

// readModules applies the module form fields to settings. Modules the server
// does not know (external ones) and values the form does not show are kept.
func (s *Server) readModules(r *http.Request, cur map[string]core.ModuleCfg) (map[string]core.ModuleCfg, error) {
	out := map[string]core.ModuleCfg{}
	for id, m := range cur {
		c := core.ModuleCfg{}
		for k, v := range m {
			c[k] = v
		}
		out[id] = c
	}
	for _, m := range s.reg.All() {
		id := m.ID()
		sch := parseSchema(m.ConfigSchema())
		c := out[id]
		if c == nil {
			c = core.ModuleCfg{}
		}
		for k, p := range sch.Properties {
			kind := p.kind()
			if kind == "" {
				continue
			}
			v := strings.TrimSpace(r.PostFormValue("m/" + id + "/" + k))
			if v == "" {
				delete(c, k)
				continue
			}
			switch {
			case kind == "bool":
				c[k] = v == "true"
			case kind == "number":
				n, err := strconv.ParseFloat(v, 64)
				if err != nil {
					return nil, badConfig{fmt.Errorf("%s.%s: %q is not a number", id, k, v)}
				}
				if p.Type == "integer" && n == float64(int64(n)) {
					c[k] = int64(n)
				} else {
					c[k] = n
				}
			default:
				c[k] = v
			}
		}
		if len(c) == 0 {
			delete(out, id)
		} else {
			out[id] = c
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// ---- pages -----------------------------------------------------------------------

func (s *Server) setupPage(w http.ResponseWriter, r *http.Request, acc access) {
	cfg, _, err := s.st.GetConfig(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	v := s.base(r, "setup", acc)
	if n := r.URL.Query().Get("imported"); n != "" {
		v.Msg = i18n.In(v.Lang, "web.setup.import.done", n)
	}
	s.fillSetup(&v, cfg)
	s.render(w, v, http.StatusOK)
}

func (s *Server) fillSetup(v *view, cfg core.Config) {
	pal := newPalette(cfg)
	active := cfg.ActiveHats()
	for i, h := range active {
		v.Setup.Hats = append(v.Setup.Hats, setupHat{Key: h.Key, Name: h.DisplayName(), Color: pal.color(h.Key),
			Activities: len(h.Activities), First: i == 0, Last: i == len(active)-1})
	}
	v.Setup.NextColor = freeColor(cfg)
	if devs, err := s.st.Devices(context.Background()); err == nil {
		loc := s.Loc
		for _, d := range devs {
			row := deviceRow{Name: d.Name}
			if !d.LastSeen.IsZero() {
				row.Online = s.Now().Sub(d.LastSeen) < time.Minute
				row.Seen = d.LastSeen.In(loc).Format("2006-01-02 15:04")
			}
			v.Setup.Devices = append(v.Setup.Devices, row)
		}
	}
	for _, h := range cfg.Hats {
		if h.Archived {
			v.Setup.Archived = append(v.Setup.Archived, setupHat{Key: h.Key, Name: h.DisplayName(), Color: pal.color(h.Key), Archived: true})
		}
	}
}

// hatsAction changes the list of hats: add, move, archive, restore.
func (s *Server) hatsAction(w http.ResponseWriter, r *http.Request, acc access) {
	lang := s.langOf(r)
	op, key := r.PostFormValue("op"), r.PostFormValue("key")
	dest := "/setup"
	_, err := s.editConfig(r.Context(), false, func(c *core.Config) error {
		if op == "add" {
			name := strings.TrimSpace(r.PostFormValue("name"))
			if name == "" {
				return badConfig{errors.New(i18n.In(lang, "web.setup.needname"))}
			}
			k := slug(name)
			for n := 2; hatIndex(c, k) >= 0; n++ {
				k = fmt.Sprintf("%s-%d", slug(name), n)
			}
			acts := splitList(r.PostFormValue("activities"))
			if acts == nil {
				acts = []string{}
			}
			c.Hats = append(c.Hats, core.Hat{Key: k, Name: name, Color: strings.ToLower(r.PostFormValue("color")), Activities: acts})
			dest = "/setup/hat?key=" + url.QueryEscape(k)
			return nil
		}
		i := hatIndex(c, key)
		if i < 0 {
			return badConfig{fmt.Errorf("unknown hat %q", key)}
		}
		switch op {
		case "archive":
			c.Hats[i].Archived = true
		case "restore":
			c.Hats[i].Archived = false
		case "up", "down":
			// move among the active hats; archived ones keep their place
			step := -1
			if op == "down" {
				step = 1
			}
			for j := i + step; j >= 0 && j < len(c.Hats); j += step {
				if !c.Hats[j].Archived {
					c.Hats[i], c.Hats[j] = c.Hats[j], c.Hats[i]
					break
				}
			}
		default:
			return badConfig{fmt.Errorf("unknown action %q", op)}
		}
		return nil
	})
	if err != nil {
		dest = "/setup?e=" + url.QueryEscape(userError(lang, err))
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

func (s *Server) hatPage(w http.ResponseWriter, r *http.Request, acc access) {
	cfg, _, err := s.st.GetConfig(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	h, ok := cfg.Hat(r.URL.Query().Get("key"))
	if !ok {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	v := s.base(r, "sethat", acc)
	s.fillHat(&v, cfg, h)
	v.Hat.Saved = r.URL.Query().Get("saved") != ""
	s.render(w, v, http.StatusOK)
}

func (s *Server) fillHat(v *view, cfg core.Config, h core.Hat) {
	pal := newPalette(cfg)
	color := h.Color
	if color == "" {
		color = pal.color(h.Key)
	}
	v.Hat = hatEdit{Key: h.Key, Name: h.Name, Color: color,
		Resume:   resumeOptions(v.Lang, h.ResumeActivity, false, cfg.Defaults.ResumeActivity),
		Archived: h.ArchivedActivity,
		Modules:  s.moduleForms(v.Lang, h.Modules, cfg.Modules, true)}
	for i, a := range h.Activities {
		v.Hat.Activities = append(v.Hat.Activities, actRow{i, a, i == 0, i == len(h.Activities)-1})
	}
}

// hatSave stores the hat form. Buttons for single activities (move, archive,
// restore) submit the whole form, so nothing typed is lost.
func (s *Server) hatSave(w http.ResponseWriter, r *http.Request, acc access) {
	lang := s.langOf(r)
	key := r.PostFormValue("key")
	op := r.PostFormValue("op")
	var tried core.Hat
	cfg, err := s.editConfig(r.Context(), false, func(c *core.Config) error {
		i := hatIndex(c, key)
		if i < 0 {
			return badConfig{fmt.Errorf("unknown hat %q", key)}
		}
		h := c.Hats[i]
		h.Name = strings.TrimSpace(r.PostFormValue("name"))
		h.Color = strings.ToLower(r.PostFormValue("color"))
		h.ResumeActivity = r.PostFormValue("resume")
		// activities as typed; an emptied field keeps its old name
		acts := make([]string, 0, len(h.Activities)+1)
		for j, old := range h.Activities {
			a := strings.TrimSpace(r.PostFormValue(fmt.Sprintf("act.%d", j)))
			if a == "" {
				a = old
			}
			acts = append(acts, a)
		}
		have := map[string]bool{}
		for _, a := range acts {
			have[strings.ToLower(a)] = true
		}
		for _, a := range splitList(r.PostFormValue("newact")) {
			if !have[strings.ToLower(a)] {
				acts = append(acts, a)
			}
		}
		archived := append([]string(nil), h.ArchivedActivity...)
		verb, arg, _ := strings.Cut(op, ":")
		if j, err := strconv.Atoi(arg); err == nil && j >= 0 && j < len(acts) {
			switch verb {
			case "up":
				if j > 0 {
					acts[j-1], acts[j] = acts[j], acts[j-1]
				}
			case "down":
				if j < len(acts)-1 {
					acts[j], acts[j+1] = acts[j+1], acts[j]
				}
			case "archive":
				archived = append(archived, acts[j])
				acts = append(acts[:j], acts[j+1:]...)
			}
		}
		if verb == "restore" {
			for j, a := range archived {
				if a == arg {
					archived = append(archived[:j], archived[j+1:]...)
					acts = append(acts, a)
					break
				}
			}
		}
		h.Activities, h.ArchivedActivity = acts, archived
		mods, err := s.readModules(r, h.Modules)
		if err != nil {
			tried = h
			return err
		}
		h.Modules = mods
		tried = h
		c.Hats[i] = h
		return nil
	})
	if err != nil {
		var bad badConfig
		if !errors.As(err, &bad) {
			serverError(w, err)
			return
		}
		// show the form again with what was typed
		v := s.base(r, "sethat", acc)
		s.fillHat(&v, cfg, tried)
		v.Err = userError(lang, err)
		s.render(w, v, http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/setup/hat?saved=1&key="+url.QueryEscape(key), http.StatusSeeOther)
}

func (s *Server) defaultsPage(w http.ResponseWriter, r *http.Request, acc access) {
	cfg, _, err := s.st.GetConfig(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	v := s.base(r, "setdefaults", acc)
	s.fillDefaults(&v, cfg)
	v.Defaults.Saved = r.URL.Query().Get("saved") != ""
	s.render(w, v, http.StatusOK)
}

func (s *Server) fillDefaults(v *view, cfg core.Config) {
	w := cfg.Work()
	v.Defaults = defaultsEdit{Resume: resumeOptions(v.Lang, cfg.Defaults.ResumeActivity, true, ""),
		WorkStart: w.Start, WorkEnd: w.End,
		Modules: s.moduleForms(v.Lang, cfg.Modules, nil, false)}
	for i := 1; i <= 7; i++ {
		d := core.Weekdays[i%7]
		v.Defaults.WorkDays = append(v.Defaults.WorkDays, option{Value: d, Label: i18n.In(v.Lang, "web.wd."+d), Selected: w.IsWorkday(time.Weekday(i % 7))})
	}
}

func (s *Server) defaultsSave(w http.ResponseWriter, r *http.Request, acc access) {
	var tried core.Config
	cfg, err := s.editConfig(r.Context(), false, func(c *core.Config) error {
		c.Defaults.ResumeActivity = r.PostFormValue("resume")
		if ws, we := r.PostFormValue("work_start"), r.PostFormValue("work_end"); ws != "" || we != "" {
			wh := core.WorkHours{Start: ws, End: we, Days: r.Form["work_day"]}
			if len(wh.Days) == 0 {
				return badConfig{errors.New(i18n.In(s.langOf(r), "web.setup.nodays"))}
			}
			c.Defaults.WorkHours = &wh
		}
		mods, err := s.readModules(r, c.Modules)
		if err != nil {
			tried = *c
			return err
		}
		c.Modules = mods
		tried = *c
		return nil
	})
	if err != nil {
		var bad badConfig
		if !errors.As(err, &bad) {
			serverError(w, err)
			return
		}
		_ = cfg
		v := s.base(r, "setdefaults", acc)
		s.fillDefaults(&v, tried)
		v.Err = userError(s.langOf(r), err)
		s.render(w, v, http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/setup/defaults?saved=1", http.StatusSeeOther)
}

// importAction previews or applies a YAML configuration from the form
// (pasted text or an uploaded file).
func (s *Server) importAction(w http.ResponseWriter, r *http.Request, acc access) {
	lang := s.langOf(r)
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
	}
	data := r.FormValue("data")
	if f, _, err := r.FormFile("file"); err == nil {
		b, _ := io.ReadAll(io.LimitReader(f, 1<<20))
		f.Close()
		if len(strings.TrimSpace(string(b))) > 0 {
			data = string(b)
		}
	}
	replace := r.FormValue("replace") != ""
	apply := r.FormValue("op") == "apply"
	cfg, _, err := s.st.GetConfig(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	v := s.base(r, "setup", acc)
	v.Setup.Import = importView{Data: data, Replace: replace}
	if strings.TrimSpace(data) == "" {
		v.Err = i18n.In(lang, "web.setup.import.empty")
		s.fillSetup(&v, cfg)
		s.render(w, v, http.StatusBadRequest)
		return
	}
	res, err := s.importConfig(r.Context(), []byte(data), !apply, replace)
	if err != nil {
		var bad badConfig
		if !errors.As(err, &bad) {
			serverError(w, err)
			return
		}
		v.Err = userError(lang, err)
		s.fillSetup(&v, cfg)
		s.render(w, v, http.StatusBadRequest)
		return
	}
	if apply {
		http.Redirect(w, r, "/setup?imported="+strconv.Itoa(len(res.Changes)), http.StatusSeeOther)
		return
	}
	v.Setup.Import.Preview = true
	v.Setup.Import.Changes, v.Setup.Import.Warnings = res.Changes, res.Warnings
	s.fillSetup(&v, cfg)
	s.render(w, v, http.StatusOK)
}

func (s *Server) exportConfig(w http.ResponseWriter, r *http.Request, _ access) {
	cfg, _, err := s.st.GetConfig(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	b, err := cfg.Export()
	if err != nil {
		serverError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="hatrack.yaml"`)
	w.Write(b)
}
