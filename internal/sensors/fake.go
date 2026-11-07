package sensors

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"time"

	"hatrack/internal/core"
)

// FakeEnv names a file of JSON lines; when set, every built-in sensor gets
// a fake backend reading it. For tests that run the agent as a process.
const FakeEnv = "HATRACK_FAKE_SENSOR"

// fakeBackend reports the states written to a file, one JSON object per
// line: {"sensor":"window","attrs":{"app":"code","title":"x"}}. Lines for
// other sensors are ignored; "attrs": null reports "nothing".
type fakeBackend struct {
	sensor string
	path   string
	poll   time.Duration // 100 ms
}

// fakeLine is one line of the fake state file.
type fakeLine struct {
	Sensor string     `json:"sensor"`
	Attrs  core.Attrs `json:"attrs"`
}

func (b *fakeBackend) Name() string { return "fake" }

func (b *fakeBackend) Detect(ctx context.Context) (bool, string) {
	if b.path == "" {
		return false, FakeEnv + " not set"
	}
	return true, ""
}

func (b *fakeBackend) Run(ctx context.Context, emit func(time.Time, core.Attrs)) error {
	f, err := os.OpenFile(b.path, os.O_RDONLY|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()

	poll := b.poll
	if poll <= 0 {
		poll = 100 * time.Millisecond
	}
	var pending []byte // bytes of a line not yet complete
	chunk := make([]byte, 4096)
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		n, _ := f.Read(chunk)
		pending = append(pending, chunk[:n]...)
		for {
			i := bytes.IndexByte(pending, '\n')
			if i < 0 {
				break
			}
			b.handle(pending[:i], emit)
			pending = pending[i+1:]
		}
		if n > 0 {
			continue
		}
		// at end of file (or a read error, retried as well): wait for more
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(poll):
		}
	}
}

// handle emits one complete line if it is valid JSON for this sensor.
func (b *fakeBackend) handle(line []byte, emit func(time.Time, core.Attrs)) {
	var l fakeLine
	if err := json.Unmarshal(line, &l); err != nil || l.Sensor != b.sensor {
		return
	}
	emit(time.Now(), l.Attrs)
}
