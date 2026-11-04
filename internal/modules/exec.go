package modules

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"hatrack/internal/core"
)

// execModule runs a command on enter and leave. It is the universal fallback.
type execModule struct{}

// NewExec returns the exec module.
func NewExec() Module { return execModule{} }

func (execModule) ID() string          { return "exec" }
func (execModule) RunsIn() RunsIn      { return RunsInAgent }
func (execModule) Backends() []Backend { return nil }
func (execModule) ConfigSchema() []byte {
	return []byte(`{"type":"object","additionalProperties":false,"properties":{` + commonProps + `,
	  "enter":{"type":"string"},"leave":{"type":"string"}}}`)
}

func (m execModule) Enter(ctx context.Context, sw Switch, cfg json.RawMessage) error {
	return m.run(ctx, "enter", sw, cfg)
}

func (m execModule) Leave(ctx context.Context, sw Switch, cfg json.RawMessage) error {
	return m.run(ctx, "leave", sw, cfg)
}

func (execModule) run(ctx context.Context, hook string, sw Switch, raw json.RawMessage) error {
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return err
	}
	script, _ := cfg[hook].(string)
	if strings.TrimSpace(script) == "" {
		return nil
	}
	// "~/bin/x.sh" works as the first word of a command.
	if strings.HasPrefix(script, "~/") {
		script = core.ExpandPath(script)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", script)
	cmd.Env = append(os.Environ(),
		"HAT_HOOK="+hook,
		"HAT_KEY="+sw.Hat, "HAT_NAME="+sw.HatName, "HAT_ACTIVITY="+sw.Activity,
		"HAT_PREV="+sw.Prev, "HAT_PREV_NAME="+sw.PrevName, "HAT_COLOR="+sw.Color)
	if out, errOut, err := runCaptured(cmd); err != nil {
		return fmt.Errorf("exec %s: %w: %s", hook, err, strings.TrimSpace(string(out)+" "+string(errOut)))
	}
	return nil
}
