// Package tray is HatTray: a StatusNotifierItem with a menu to switch hats.
// It is a thin client on the agent socket, like the CLI. The menu is a pure
// function of the agent's snapshot, so it can be tested without a desktop.
package tray

import (
	"time"

	"hatrack/internal/agent"
	"hatrack/internal/core"
	"hatrack/internal/i18n"
)

// Action is what a menu item does when clicked.
type Action struct {
	Request *agent.Request // send to the agent
	Open    bool           // open the web UI in the browser
	Quit    bool           // end the tray
}

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
const Grey = "#8a8f98"

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
	if len(s.Hats) == 0 {
		menu = append(menu, Item{Label: i18n.T("err.nohats"), Disabled: true})
	}
	for _, h := range s.Hats {
		current := st.Mode != core.ModeNone && st.Hat == h.Key
		it := Item{Label: h.Name, Checked: current}
		if len(h.Activities) == 0 {
			it.Action = req(agent.Request{Cmd: "on", Hat: h.Key})
		} else {
			it.Children = append(it.Children, Item{
				Label:   i18n.T("tray.hat_only"),
				Checked: current && st.Mode == core.ModeWearing && st.Activity == "",
				Action:  req(agent.Request{Cmd: "on", Hat: h.Key, Activity: "-", HasActivity: true}),
			})
			for _, a := range h.Activities {
				it.Children = append(it.Children, Item{
					Label:   a,
					Checked: current && st.Mode == core.ModeWearing && st.Activity == a,
					Action:  req(agent.Request{Cmd: "on", Hat: h.Key, Activity: a, HasActivity: true}),
				})
			}
		}
		menu = append(menu, it)
	}
	menu = append(menu, Item{Separator: true})
	switch st.Mode {
	case core.ModeWearing:
		menu = append(menu,
			Item{Label: i18n.T("ui.pause"), Action: req(agent.Request{Cmd: "off"})},
			Item{Label: i18n.T("ui.neutral"), Action: req(agent.Request{Cmd: "off", Neutral: true})})
	case core.ModePaused:
		menu = append(menu, Item{Label: i18n.T("ui.resume"), Action: req(agent.Request{Cmd: "on"})})
		if !st.Neutral {
			menu = append(menu, Item{Label: i18n.T("ui.neutral"), Action: req(agent.Request{Cmd: "off", Neutral: true})})
		}
	}
	if s.Server != "" {
		menu = append(menu, Item{Label: i18n.T("tray.open"), Action: &Action{Open: true}})
	}
	menu = append(menu, Item{Separator: true}, Item{Label: i18n.T("tray.quit"), Action: &Action{Quit: true}})
	return menu
}
