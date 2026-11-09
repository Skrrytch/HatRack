package sensors

import (
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"hatrack/internal/core"
)

// wdFixture is a fake home with Claude sessions and repositories, and a fake
// /proc. Process tree: 100 (terminal) -> 101 (shell) -> 102 (claude in shop);
// 200 (other terminal) -> 201 (claude in blog).
type wdFixture struct {
	t    *testing.T
	home string
	proc string
	now  time.Time
}

func newWdFixture(t *testing.T) *wdFixture {
	f := &wdFixture{t: t, home: t.TempDir(), proc: t.TempDir(), now: time.Date(2026, 11, 9, 19, 0, 0, 0, time.UTC)}
	f.repo("work/shop", "ref: refs/heads/feature/ACME-101-login\n")
	f.repo("blog", "ref: refs/heads/main\n")
	f.proc_(100, 1, "guake", "/")
	f.proc_(101, 100, "bash", filepath.Join(f.home, "work/shop"))
	f.proc_(102, 101, "claude", filepath.Join(f.home, "work/shop"))
	f.proc_(200, 1, "kitty", "/")
	f.proc_(201, 200, "claude", filepath.Join(f.home, "blog"))
	return f
}

func (f *wdFixture) mkdir(p string) {
	if err := os.MkdirAll(p, 0o755); err != nil {
		f.t.Fatal(err)
	}
}

func (f *wdFixture) write(p, s string) {
	f.mkdir(filepath.Dir(p))
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *wdFixture) repo(rel, head string) {
	f.write(filepath.Join(f.home, rel, ".git", "HEAD"), head)
}

func (f *wdFixture) proc_(pid, ppid int, comm, cwd string) {
	d := filepath.Join(f.proc, strconv.Itoa(pid))
	f.write(filepath.Join(d, "comm"), comm+"\n")
	f.write(filepath.Join(d, "stat"), strconv.Itoa(pid)+" ("+comm+" x) S "+strconv.Itoa(ppid)+" 1 1 0\n")
	if err := os.Symlink(cwd, filepath.Join(d, "cwd")); err != nil {
		f.t.Fatal(err)
	}
}

// session writes a session file of a project folder, modified ago before now.
func (f *wdFixture) session(configDir, cwd string, ago time.Duration) {
	p := filepath.Join(f.home, configDir, "projects", encodeProject(cwd), "s.jsonl")
	os.Remove(p) // written before and made unreadable
	f.write(p, "{\"secret\":\"never read\"}\n")
	at := f.now.Add(-ago)
	if err := os.Chtimes(p, at, at); err != nil {
		f.t.Fatal(err)
	}
	// unreadable: the reader must work from the modification time and the
	// folder name alone, never from the content
	if err := os.Chmod(p, 0); err != nil {
		f.t.Fatal(err)
	}
}

func (f *wdFixture) reader() Workdir {
	return Workdir{Proc: f.proc, Home: f.home, Now: func() time.Time { return f.now }}
}

func TestEncodeProject(t *testing.T) {
	if got := encodeProject("/home/bespecke/.cache/sprint-tray"); got != "-home-bespecke--cache-sprint-tray" {
		t.Errorf("got %s", got)
	}
}

func TestWorkdirLookup(t *testing.T) {
	f := newWdFixture(t)
	shop, blog := filepath.Join(f.home, "work/shop"), filepath.Join(f.home, "blog")
	want := core.Attrs{core.AttrRepo: "shop", core.AttrBranch: "feature/ACME-101-login", core.AttrBranchSrc: "claude"}

	// nothing written lately: nothing
	f.session(".claude", shop, time.Hour)
	if got := f.reader().Lookup("Guake", 100, nil); got != nil {
		t.Errorf("stale session: %v", got)
	}
	// the shop session is active and runs in the focused terminal
	f.session(".claude", shop, time.Minute)
	if got := f.reader().Lookup("Guake", 100, nil); !maps.Equal(got, want) {
		t.Errorf("terminal: %v", got)
	}
	// another terminal in front: its claude is not the active session
	if got := f.reader().Lookup("kitty", 200, nil); got != nil {
		t.Errorf("other terminal: %v", got)
	}
	// Claude itself in front (remote control, its own sessions)
	if got := f.reader().Lookup("com.anthropic.Claude", 0, nil); !maps.Equal(got, want) {
		t.Errorf("claude desktop: %v", got)
	}
	// a browser in front gets nothing
	if got := f.reader().Lookup("firefox", 300, nil); got != nil {
		t.Errorf("browser: %v", got)
	}
	// ignored folder
	if got := f.reader().Lookup("Guake", 100, []string{"~/work"}); got != nil {
		t.Errorf("ignored folder: %v", got)
	}
	if got := f.reader().Lookup("Guake", 100, []string{"~/wor"}); got == nil {
		t.Error("a folder prefix is not a parent folder")
	}
	// two projects written about the same time (a second config folder):
	// unclear, nothing
	f.session(".claude-private", blog, 30*time.Second)
	if got := f.reader().Lookup("com.anthropic.Claude", 0, nil); got != nil {
		t.Errorf("two active sessions: %v", got)
	}
	// later the blog alone is active, in the kitty window
	f.now = f.now.Add(5 * time.Minute)
	f.session(".claude-private", blog, 0)
	if got := f.reader().Lookup("kitty", 200, nil); got[core.AttrRepo] != "blog" || got[core.AttrBranch] != "main" {
		t.Errorf("blog: %v", got)
	}
}

func TestWorkdirSessionNotRunning(t *testing.T) {
	f := newWdFixture(t)
	gone := filepath.Join(f.home, "gone")
	f.repo("gone", "ref: refs/heads/x\n")
	f.session(".claude", gone, 0) // no claude process works there
	if got := f.reader().Lookup("com.anthropic.Claude", 0, nil); got != nil {
		t.Errorf("session without a process: %v", got)
	}
}

func TestGitHead(t *testing.T) {
	d := t.TempDir()
	write := func(p, s string) {
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(s), 0o644)
	}
	write(filepath.Join(d, "main/.git/HEAD"), "ref: refs/heads/feature/ACME-7\n")
	write(filepath.Join(d, "main/.git/worktrees/wt/HEAD"), "ref: refs/heads/ACME-8-fix\n")
	write(filepath.Join(d, "wt/.git"), "gitdir: ../main/.git/worktrees/wt\n")
	write(filepath.Join(d, "det/.git/HEAD"), "0123456789abcdef0123456789abcdef01234567\n")
	os.MkdirAll(filepath.Join(d, "main/src/deep"), 0o755)
	os.MkdirAll(filepath.Join(d, "plain"), 0o755)
	for _, c := range []struct {
		dir, tree, branch string
		ok                bool
	}{
		{"main/src/deep", "main", "feature/ACME-7", true},
		{"wt", "wt", "ACME-8-fix", true},
		{"det", "det", "", true},
	} {
		tree, branch, ok := gitHead(filepath.Join(d, c.dir))
		if ok != c.ok || filepath.Base(tree) != c.tree || branch != c.branch {
			t.Errorf("%s: %s %q %v", c.dir, tree, branch, ok)
		}
	}
	if _, _, ok := gitHead(filepath.Join(d, "plain")); ok && filepath.Dir(d) == "/" {
		t.Error("no repository found one")
	}
}
