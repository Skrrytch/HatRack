package reexec

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func writeBin(t *testing.T, path, body string) {
	t.Helper()
	tmp := path + ".new"
	if err := os.WriteFile(tmp, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil { // as update.sh and dpkg do
		t.Fatal(err)
	}
}

func TestReplaced(t *testing.T) {
	p := filepath.Join(t.TempDir(), "hat")
	writeBin(t, p, "old")
	b, err := Of(p)
	if err != nil {
		t.Fatal(err)
	}
	// a running process keeps its old file open, so its inode is not reused
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if b.Replaced() {
		t.Fatal("unchanged binary reported as replaced")
	}
	os.Remove(p)
	if b.Replaced() {
		t.Fatal("a missing binary is not a replacement")
	}
	writeBin(t, p, "new")
	if !b.Replaced() {
		t.Fatal("replacement not noticed")
	}
}

func TestRunRestartsAfterReplacement(t *testing.T) {
	p := filepath.Join(t.TempDir(), "hat")
	writeBin(t, p, "old")
	b, _ := Of(p)

	old := Every
	Every = 10 * time.Millisecond
	defer func() { Every = old }()
	var got string
	exec = func(path string, _ []string, _ []string) error { got = path; return errors.New("stop") }
	defer func() { exec = syscall.Exec }()

	err := run(context.Background(), b, "test", func(ctx context.Context) error {
		writeBin(t, p, "new")
		<-ctx.Done() // cancelled because of the new binary
		return nil
	})
	if got != p {
		t.Fatalf("exec called with %q, want %q", got, p)
	}
	if err == nil || err.Error() != "stop" {
		t.Fatalf("err = %v, want the exec error", err)
	}
}

func TestRunNoRestartWhenStopped(t *testing.T) {
	p := filepath.Join(t.TempDir(), "hat")
	writeBin(t, p, "old")
	b, _ := Of(p)
	called := false
	exec = func(string, []string, []string) error { called = true; return nil }
	defer func() { exec = syscall.Exec }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	run(ctx, b, "test", func(ctx context.Context) error { <-ctx.Done(); return nil })
	if called {
		t.Fatal("a stopped process must not start again")
	}
}
