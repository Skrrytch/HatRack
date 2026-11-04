package server

import (
	"embed"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"time"

	"hatrack/internal/core"
	"hatrack/internal/i18n"
)

//go:embed templates/*.html
var templateFS embed.FS

var nowTmpl = template.Must(template.New("now.html").Funcs(template.FuncMap{
	"t": func(lang, key string, args ...any) string { return i18n.In(lang, key, args...) },
}).ParseFS(templateFS, "templates/now.html"))

type nowView struct {
	Lang     string
	Mode     string
	Hat      string
	Activity string
	Color    string
	Since    string
	Duration string
	Devices  []nowDevice
	Denied   bool
}

type nowDevice struct {
	Name   string
	Online bool
	Seen   string
}

const (
	tokenCookie = "hatrack_token"
	langCookie  = "hatrack_lang"
)

// nowPage is the read-only "Now" page. It needs a device token, given once
// as ?token=… and then kept in a cookie.
func (s *Server) nowPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if lang := i18n.Normalize(q.Get("lang")); lang != "" {
		http.SetCookie(w, &http.Cookie{Name: langCookie, Value: lang, Path: "/", MaxAge: 365 * 86400, SameSite: http.SameSiteLaxMode})
	}
	if tok := q.Get("token"); tok != "" {
		http.SetCookie(w, &http.Cookie{Name: tokenCookie, Value: tok, Path: "/", MaxAge: 365 * 86400,
			HttpOnly: true, SameSite: http.SameSiteStrictMode})
		dest := url.Values{}
		if l := q.Get("lang"); l != "" {
			dest.Set("lang", l)
		}
		http.Redirect(w, r, "/?"+dest.Encode(), http.StatusSeeOther)
		return
	}
	lang := ""
	if c, err := r.Cookie(langCookie); err == nil {
		lang = i18n.Normalize(c.Value)
	}
	if lang == "" {
		lang = i18n.FromAcceptLanguage(r.Header.Get("Accept-Language"))
	}
	view := nowView{Lang: lang}
	c, err := r.Cookie(tokenCookie)
	if err == nil {
		_, err = s.st.DeviceByToken(r.Context(), c.Value)
	}
	if err != nil {
		view.Denied = true
		w.WriteHeader(http.StatusUnauthorized)
		render(w, view)
		return
	}
	st, err := s.st.CurrentState(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	cfg, _, _ := s.st.GetConfig(r.Context())
	view.Mode = string(st.Mode)
	if h, ok := cfg.Hat(st.Hat); ok {
		view.Hat, view.Color = h.DisplayName(), h.Color
	} else {
		view.Hat = st.Hat
	}
	view.Activity = st.Activity
	if st.Mode != core.ModeNone {
		local := st.Since.Local()
		view.Since = local.Format("15:04")
		if st.Mode == core.ModeWearing {
			view.Duration = core.FormatDuration(s.Now().Sub(st.Since))
		}
	}
	devs, _ := s.st.Devices(r.Context())
	for _, d := range devs {
		nd := nowDevice{Name: d.Name}
		if !d.LastSeen.IsZero() {
			nd.Online = s.Now().Sub(d.LastSeen) < time.Minute
			nd.Seen = d.LastSeen.Local().Format("15:04")
		}
		view.Devices = append(view.Devices, nd)
	}
	render(w, view)
}

func render(w http.ResponseWriter, v nowView) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := nowTmpl.Execute(w, v); err != nil {
		log.Printf("render: %v", err)
	}
}
