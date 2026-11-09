package sensors

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"hatrack/internal/core"
)

// The workdir reader finds the repository and branch of the work in front
// (docs/auto-detection.md, 1a and 1b). This first reader covers Claude Code:
// the session written to last is the one being worked with, its folder under
// ~/.claude*/projects names the project, a running claude process confirms
// the working directory, and .git/HEAD gives the branch. It reads metadata
// only: modification times, folder names, /proc/<pid>/{comm,stat,cwd} and the
// one line of HEAD. It never opens a session file.

// AttrPID carries the focused window's process id from the window backend to
// the workdir reader. It is never stored.
const AttrPID = "pid"

// Session freshness: a session counts as active if written within
// sessionFresh; if another project was written within sessionClash of it,
// it is unclear which one is in front, and nothing is reported.
const (
	sessionFresh = 10 * time.Minute
	sessionClash = 2 * time.Minute
)

// claudeAppRe matches a window of Claude itself (Claude Desktop), whose
// sessions may run elsewhere (remote control).
var claudeAppRe = regexp.MustCompile(`(?i)claude`)

// Workdir looks up repository and branch. The fields point at the real
// system; tests set them to fixtures.
type Workdir struct {
	Proc string           // "/proc"
	Home string           // the user's home folder
	Now  func() time.Time // time.Now if nil
}

// NewWorkdir returns a reader for this system.
func NewWorkdir() Workdir {
	home, _ := os.UserHomeDir()
	return Workdir{Proc: "/proc", Home: home}
}

// Lookup returns repo, branch and branch_src for the focused window, nil if
// there is nothing certain. Folders in ignore (and below) are never looked
// into.
func (w Workdir) Lookup(app string, pid int, ignore []string) core.Attrs {
	project, ok := w.activeProject()
	if !ok {
		return nil
	}
	procs := w.claudeProcs()
	var cwd string
	for _, p := range procs {
		if encodeProject(p.cwd) != project {
			continue
		}
		// the session runs in the focused window, or Claude itself is in
		// front (its own sessions, or remote control of this one)
		if (pid > 0 && w.descends(p.pid, pid)) || claudeAppRe.MatchString(app) {
			if cwd != "" && cwd != p.cwd {
				return nil // two folders with the same name: unclear
			}
			cwd = p.cwd
		}
	}
	if cwd == "" || w.ignored(cwd, ignore) {
		return nil
	}
	tree, branch, ok := gitHead(cwd)
	if !ok {
		return nil
	}
	a := core.Attrs{core.AttrRepo: filepath.Base(tree), core.AttrBranchSrc: "claude"}
	if branch != "" {
		a[core.AttrBranch] = branch
	}
	return a
}

// encodeProject is how Claude Code names a project's folder: every character
// that is not a letter or digit becomes '-'.
func encodeProject(path string) string {
	b := []byte(path)
	for i, c := range b {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			b[i] = '-'
		}
	}
	return string(b)
}

// activeProject returns the folder name of the project whose session was
// written last, if that is recent and no other project was written about
// the same time.
func (w Workdir) activeProject() (string, bool) {
	files, _ := filepath.Glob(filepath.Join(w.Home, ".claude*", "projects", "*", "*.jsonl"))
	var best, second time.Time
	var bestProj string
	for _, f := range files {
		fi, err := os.Stat(f)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		proj := filepath.Base(filepath.Dir(f))
		t := fi.ModTime()
		switch {
		case t.After(best):
			if proj != bestProj && !best.IsZero() {
				second = best
			}
			best, bestProj = t, proj
		case proj != bestProj && t.After(second):
			second = t
		}
	}
	now := time.Now
	if w.Now != nil {
		now = w.Now
	}
	if best.IsZero() || now().Sub(best) > sessionFresh {
		return "", false
	}
	if !second.IsZero() && best.Sub(second) < sessionClash {
		return "", false
	}
	return bestProj, true
}

type claudeProc struct {
	pid int
	cwd string
}

// claudeProcs lists the running claude processes of this user with their
// working directory.
func (w Workdir) claudeProcs() []claudeProc {
	ents, err := os.ReadDir(w.Proc)
	if err != nil {
		return nil
	}
	var out []claudeProc
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		comm, err := os.ReadFile(filepath.Join(w.Proc, e.Name(), "comm"))
		if err != nil || strings.TrimSpace(string(comm)) != "claude" {
			continue
		}
		cwd, err := os.Readlink(filepath.Join(w.Proc, e.Name(), "cwd"))
		if err != nil || cwd == "" {
			continue
		}
		out = append(out, claudeProc{pid, cwd})
	}
	return out
}

// ppid reads the parent process id from /proc/<pid>/stat.
func (w Workdir) ppid(pid int) int {
	b, err := os.ReadFile(filepath.Join(w.Proc, strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0
	}
	// the name in parentheses may hold spaces; the fields follow the last ')'
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 2 {
		return 0
	}
	n, _ := strconv.Atoi(f[1])
	return n
}

// descends reports whether pid is anc or one of its descendants.
func (w Workdir) descends(pid, anc int) bool {
	for i := 0; pid > 1 && i < 64; i++ {
		if pid == anc {
			return true
		}
		pid = w.ppid(pid)
	}
	return false
}

// ignored reports whether dir is one of the folders or below one; "~" means
// the home folder.
func (w Workdir) ignored(dir string, ignore []string) bool {
	for _, p := range ignore {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if p == "~" || strings.HasPrefix(p, "~/") {
			p = filepath.Join(w.Home, strings.TrimPrefix(p, "~"))
		}
		p = filepath.Clean(p)
		if dir == p || strings.HasPrefix(dir, p+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// gitHead finds the work tree that holds dir and its checked-out branch ("" if
// HEAD is detached). Only .git (or the gitdir it points to) and HEAD are read.
func gitHead(dir string) (tree, branch string, ok bool) {
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		g := filepath.Join(d, ".git")
		if fi, err := os.Stat(g); err == nil {
			gitdir := g
			if !fi.IsDir() { // a worktree or submodule: "gitdir: <path>"
				b, err := os.ReadFile(g)
				if err != nil {
					return "", "", false
				}
				p, found := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir:")
				if !found {
					return "", "", false
				}
				gitdir = strings.TrimSpace(p)
				if !filepath.IsAbs(gitdir) {
					gitdir = filepath.Join(d, gitdir)
				}
			}
			head, err := os.ReadFile(filepath.Join(gitdir, "HEAD"))
			if err != nil {
				return "", "", false
			}
			ref, found := strings.CutPrefix(strings.TrimSpace(string(head)), "ref: refs/heads/")
			if !found {
				return d, "", true // detached
			}
			return d, ref, true
		}
		if parent := filepath.Dir(d); parent == d {
			return "", "", false
		}
	}
}
