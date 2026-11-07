package modules

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/godbus/dbus/v5"

	"hatrack/internal/i18n"
)

const (
	notifyName = "org.freedesktop.Notifications"
	notifyPath = "/org/freedesktop/Notifications"
)

// NewNotify returns the notify module (backend: D-Bus notifications).
func NewNotify() Module {
	return &notifyModule{backendModule{
		id:       "notify",
		schema:   `{"type":"object","additionalProperties":false,"properties":{` + commonProps + `}}`,
		backends: []Backend{&dbusNotify{}},
	}}
}

type notifyModule struct{ backendModule }

// Enter confirms the switch with a notification.
func (m *notifyModule) Enter(ctx context.Context, sw Switch, _ json.RawMessage) error {
	b := m.Active(ctx)
	if b == nil {
		return ErrNoBackend
	}
	body := sw.Activity
	return b.Apply(ctx, NotifyMessage(i18n.T("notify.wearing", sw.HatName), body))
}

// Notify shows a notification through the active backend, if there is one.
func (m *notifyModule) Notify(ctx context.Context, summary, body string) error {
	b := m.Active(ctx)
	if b == nil {
		return ErrNoBackend
	}
	return b.Apply(ctx, NotifyMessage(summary, body))
}

// Action is a button of a notification.
type Action struct{ Key, Label string }

// ActionNotifier is implemented by modules that can show a message with
// buttons and report which one was pressed ("" if none: closed or timed out).
type ActionNotifier interface {
	NotifyActions(ctx context.Context, summary, body string, actions []Action) (string, error)
}

// NotifyActions shows a notification with buttons and waits for the answer,
// until ctx ends. ErrNoBackend if there is no notify module or backend.
func (m *notifyModule) NotifyActions(ctx context.Context, summary, body string, actions []Action) (string, error) {
	b := m.Active(ctx)
	if b == nil {
		return "", ErrNoBackend
	}
	an, ok := b.(ActionNotifier)
	if !ok {
		return "", ErrNoBackend
	}
	return an.NotifyActions(ctx, summary, body, actions)
}

// Notifier is implemented by modules that can show a message to the user.
type Notifier interface {
	Notify(ctx context.Context, summary, body string) error
}

// NotifyMessage builds the backend configuration for one notification.
func NotifyMessage(summary, body string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"summary": summary, "body": body})
	return b
}

type dbusNotify struct{}

func (*dbusNotify) Name() string { return "dbus" }

func (*dbusNotify) Detect(ctx context.Context) bool {
	conn, err := dbus.ConnectSessionBus(dbus.WithContext(ctx))
	if err != nil {
		return false
	}
	defer conn.Close()
	var has bool
	if err := conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.NameHasOwner", 0, notifyName).Store(&has); err != nil {
		return false
	}
	return has
}

func (*dbusNotify) Apply(ctx context.Context, raw json.RawMessage) error {
	var m struct{ Summary, Body string }
	var in map[string]string
	if err := json.Unmarshal(raw, &in); err != nil {
		return err
	}
	m.Summary, m.Body = in["summary"], in["body"]
	conn, err := dbus.ConnectSessionBus(dbus.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("notify: %w", err)
	}
	defer conn.Close()
	call := conn.Object(notifyName, notifyPath).CallWithContext(ctx, notifyName+".Notify", 0,
		"HatRack", uint32(0), "", m.Summary, m.Body, []string{}, map[string]dbus.Variant{}, int32(5000))
	if call.Err != nil {
		return fmt.Errorf("notify: %w", call.Err)
	}
	return nil
}

// NotifyActions shows a notification with buttons and returns the key of the
// one that was pressed, or "" when it was closed or ctx ended first.
func (*dbusNotify) NotifyActions(ctx context.Context, summary, body string, actions []Action) (string, error) {
	conn, err := dbus.ConnectSessionBus(dbus.WithContext(ctx))
	if err != nil {
		return "", fmt.Errorf("notify: %w", err)
	}
	defer conn.Close()
	// listen before the notification exists, so no signal is missed
	for _, member := range []string{"ActionInvoked", "NotificationClosed"} {
		if err := conn.AddMatchSignalContext(ctx, dbus.WithMatchInterface(notifyName),
			dbus.WithMatchObjectPath(notifyPath), dbus.WithMatchMember(member)); err != nil {
			return "", fmt.Errorf("notify: %w", err)
		}
	}
	sig := make(chan *dbus.Signal, 16)
	conn.Signal(sig)
	defer conn.RemoveSignal(sig)

	list := make([]string, 0, 2*len(actions))
	for _, a := range actions {
		list = append(list, a.Key, a.Label)
	}
	var id uint32
	call := conn.Object(notifyName, notifyPath).CallWithContext(ctx, notifyName+".Notify", 0,
		"HatRack", uint32(0), "", summary, body, list, map[string]dbus.Variant{}, int32(NotifyActionsTimeout.Milliseconds()))
	if call.Err != nil {
		return "", fmt.Errorf("notify: %w", call.Err)
	}
	if err := call.Store(&id); err != nil {
		return "", fmt.Errorf("notify: %w", err)
	}
	for {
		select {
		case <-ctx.Done():
			conn.Object(notifyName, notifyPath).Call(notifyName+".CloseNotification", 0, id)
			return "", nil
		case s := <-sig:
			if s == nil || len(s.Body) == 0 || s.Path != notifyPath {
				continue
			}
			if got, ok := s.Body[0].(uint32); !ok || got != id {
				continue
			}
			switch s.Name {
			case notifyName + ".ActionInvoked":
				if len(s.Body) > 1 {
					if key, ok := s.Body[1].(string); ok {
						return key, nil
					}
				}
			case notifyName + ".NotificationClosed":
				return "", nil
			}
		}
	}
}

// NotifyActionsTimeout is how long a notification with buttons stays up.
const NotifyActionsTimeout = 10 * time.Minute

func (*dbusNotify) Snapshot(context.Context) (json.RawMessage, error) { return nil, nil }
