package server

import (
	"net/http"
	"net/url"
	"strings"

	"hatrack/internal/i18n"
)

// Setup → AI access: MCP tokens for assistants such as Claude Code.

type aiView struct {
	Tokens   []aiToken
	Added    *addedDevice // name, token and the command to connect
	Endpoint string
}

type aiToken struct {
	ID, Name, Created, Used string
}

func (s *Server) aiView(r *http.Request, acc access) (view, error) {
	lang, loc := s.langOf(r), s.locOf(r)
	v := s.base(r, "ai", acc)
	v.AI.Endpoint = serverURL(r) + "/mcp"
	list, err := s.st.MCPTokens(r.Context())
	if err != nil {
		return v, err
	}
	for _, t := range list {
		a := aiToken{ID: t.ID, Name: t.Name, Created: dayLabel(lang, t.Created.In(loc), true)}
		if !t.LastUsed.IsZero() {
			a.Used = dayLabel(lang, t.LastUsed.In(loc), true) + " " + t.LastUsed.In(loc).Format("15:04")
		}
		v.AI.Tokens = append(v.AI.Tokens, a)
	}
	return v, nil
}

func (s *Server) aiPage(w http.ResponseWriter, r *http.Request, acc access) {
	v, err := s.aiView(r, acc)
	if err != nil {
		serverError(w, err)
		return
	}
	s.render(w, v, http.StatusOK)
}

func (s *Server) aiAction(w http.ResponseWriter, r *http.Request, acc access) {
	lang := s.langOf(r)
	fail := func(msg string) { http.Redirect(w, r, "/setup/ai?e="+url.QueryEscape(msg), http.StatusSeeOther) }
	switch r.PostFormValue("op") {
	case "add":
		name := strings.TrimSpace(r.PostFormValue("name"))
		if name == "" {
			fail(i18n.In(lang, "web.setup.needname"))
			return
		}
		_, token, err := s.st.AddMCPToken(r.Context(), name, []string{"read"})
		if err != nil {
			fail(i18n.In(lang, "web.dev.exists", name))
			return
		}
		// shown once, in this answer only (see devicesAction)
		v, err := s.aiView(r, acc)
		if err != nil {
			serverError(w, err)
			return
		}
		v.AI.Added = &addedDevice{Name: name, Token: token,
			Command: `claude mcp add --scope user --transport http hatrack ` + v.AI.Endpoint + ` --header "Authorization: Bearer ` + token + `"`}
		s.render(w, v, http.StatusOK)
	case "delete":
		if err := s.st.DeleteMCPToken(r.Context(), r.PostFormValue("id")); err != nil {
			serverError(w, err)
			return
		}
		http.Redirect(w, r, "/setup/ai", http.StatusSeeOther)
	default:
		fail("unknown action")
	}
}
