package modules

import (
	"context"
	"encoding/json"
	"fmt"

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

func (*dbusNotify) Snapshot(context.Context) (json.RawMessage, error) { return nil, nil }
