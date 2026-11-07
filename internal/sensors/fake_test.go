package sensors

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"hatrack/internal/core"
)

// fakeAppend appends raw text to the file.
func fakeAppend(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
}

// fakeNext waits for the next emitted attrs, failing after 2 s.
func fakeNext(t *testing.T, got <-chan core.Attrs) core.Attrs {
	t.Helper()
	select {
	case a := <-got:
		return a
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for emit")
		return nil
	}
}

// fakeQuiet checks that nothing is emitted for a short while.
func fakeQuiet(t *testing.T, got <-chan core.Attrs) {
	t.Helper()
	select {
	case a := <-got:
		t.Fatalf("unexpected emit: %v", a)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestFakeDetect(t *testing.T) {
	ok, reason := (&fakeBackend{sensor: "window"}).Detect(context.Background())
	if ok || reason != FakeEnv+" not set" {
		t.Fatalf("empty path: got %v, %q", ok, reason)
	}
	ok, _ = (&fakeBackend{sensor: "window", path: "x"}).Detect(context.Background())
	if !ok {
		t.Fatal("with path: want ok")
	}
}

func TestFakeRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "states.jsonl")
	fakeAppend(t, path,
		`{"sensor":"window","attrs":{"app":"code","title":"a"}}`+"\n"+
			`{"sensor":"idle","attrs":{"state":"idle"}}`+"\n"+
			`{"sensor":"window","attrs":{"app":"web","title":"b"}}`+"\n")

	b := &fakeBackend{sensor: "window", path: path, poll: 10 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan core.Attrs, 16)
	errc := make(chan error, 1)
	go func() {
		errc <- b.Run(ctx, func(_ time.Time, a core.Attrs) { got <- a })
	}()

	want := func(a core.Attrs) {
		t.Helper()
		if g := fakeNext(t, got); !reflect.DeepEqual(g, a) {
			t.Fatalf("got %v, want %v", g, a)
		}
	}

	// the two window lines from the file, in order; the idle line is ignored
	want(core.Attrs{"app": "code", "title": "a"})
	want(core.Attrs{"app": "web", "title": "b"})
	fakeQuiet(t, got)

	// a line split over two writes arrives once, complete
	fakeAppend(t, path, `{"sensor":"window","attrs":{"app":"mail"`)
	fakeQuiet(t, got)
	fakeAppend(t, path, `,"title":"c"}}`+"\n")
	want(core.Attrs{"app": "mail", "title": "c"})
	fakeQuiet(t, got)

	// null means nothing to report
	fakeAppend(t, path, `{"sensor":"window","attrs":null}`+"\n")
	if g := fakeNext(t, got); g != nil {
		t.Fatalf("null attrs: got %v, want nil", g)
	}

	// an invalid line is skipped
	fakeAppend(t, path, "not json\n"+`{"sensor":"window","attrs":{"app":"last"}}`+"\n")
	want(core.Attrs{"app": "last"})
	fakeQuiet(t, got)

	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestBuiltinFake(t *testing.T) {
	path := filepath.Join(t.TempDir(), "states.jsonl")
	t.Setenv(FakeEnv, path)
	r := Builtin()
	for _, id := range []string{IdleSensor, "window"} {
		s, ok := r.Get(id)
		if !ok || len(s.Backends) == 0 {
			t.Fatalf("%s: no backends", id)
		}
		if name := s.Backends[0].Name(); name != "fake" {
			t.Fatalf("%s: first backend %q, want fake", id, name)
		}
	}

	os.Unsetenv(FakeEnv) // restored by t.Setenv at cleanup
	r = Builtin()
	for _, id := range []string{IdleSensor, "window"} {
		s, ok := r.Get(id)
		want := 0
		if id == IdleSensor {
			want = 4 // x11, mutter, kde, logind
		}
		if !ok || len(s.Backends) != want {
			t.Fatalf("%s without %s: %d backends, want %d", id, FakeEnv, len(s.Backends), want)
		}
		if id == IdleSensor {
			var names []string
			for _, b := range s.Backends {
				names = append(names, b.Name())
			}
			if got := strings.Join(names, ","); got != "x11,mutter,kde,logind" {
				t.Fatalf("idle backends %s", got)
			}
		}
	}
}
