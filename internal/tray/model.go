// Package tray is HatTray: a StatusNotifierItem with a menu to switch hats.
// It is a thin client on the agent socket, like the CLI. The menu is a pure
// function of the agent's snapshot, so it can be tested without a desktop.
package tray

import (
	"time"

	"hatrack/internal/agent"
	"hatrack/internal/core"
	"hatrack/internal/i18n"
	"hatrack/internal/icon"
	"hatrack/internal/panel"
)

// Action is what a menu item or panel click does.
type Action = panel.Action

// Item is one entry of the menu.
type Item struct {
	Label     string
	Checked   bool
	Disabled  bool
	Separator bool
	Action    *Action
	Children  []Item
}

// Grey is the frame colour while paused or without a hat.
const Grey = icon.Grey

// IconColor returns the colour of the icon: the hat's colour while it is
// worn, grey when paused or without a hat.
func IconColor(s agent.Snapshot) string {
	st := s.Status.State
	if st.Mode == core.ModeWearing {
		if s.Status.Color != "" {
			return s.Status.Color
		}
		for _, h := range s.Hats {
			if h.Key == st.Hat && h.Color != "" {
				return h.Color
			}
		}
		return "#4e79a7"
	}
	return Grey
}

func req(r agent.Request) *Action { return &Action{Request: &r} }

// BuildMenu turns a snapshot into the menu.
func BuildMenu(s agent.Snapshot, now time.Time) []Item {
	st := s.Status.State
	menu := []Item{
		{Label: agent.StatusLine(s.Status, now), Disabled: true},
		{Separator: true},
	}
	if !s.Status.Connected {
		menu = append(menu[:1:1], Item{Label: i18n.T("tray.offline"), Disabled: true}, Item{Separator: true})
	}
	switch st.Mode {
	case core.ModeWearing:
		menu = append(menu, Item{Label: i18n.T("ui.pause"), Action: req(agent.Request{Cmd: "off"})})
	case core.ModePaused:
		menu = append(menu, Item{Label: i18n.T("ui.resume"), Action: req(agent.Request{Cmd: "on"})})
	}
	if s.Server != "" {
		menu = append(menu, Item{Label: i18n.T("tray.open"), Action: &Action{Open: true}})
	}
	menu = append(menu, Item{Label: i18n.T("tray.about"), Action: &Action{About: true}},
		Item{Separator: true}, Item{Label: i18n.T("tray.quit"), Action: &Action{Quit: true}})
	return menu
}
