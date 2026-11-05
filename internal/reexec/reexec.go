// Package reexec restarts a long-running process with a new binary when its
// own binary file was replaced (by `hat update`, a new .deb or update.sh).
// Installing a package cannot restart a user's services, so they notice it
// themselves.
package reexec

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"
)

// Every is how often the binary is checked.
var Every = 15 * time.Second

// exec replaces the process; tests swap it.
var exec = syscall.Exec

// Binary is the executable this process was started from.
type Binary struct {
	Path  string
	ino   uint64
	size  int64
	mtime time.Time
}

// Self describes the running executable. The path is resolved now, because
// after a replacement /proc/self/exe points to the deleted old file.
func Self() (*Binary, error) {
	p, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if p, err = filepath.EvalSymlinks(p); err != nil {
		return nil, err
	}
	return Of(p)
}

// Of describes the executable at path.
func Of(path string) (*Binary, error) {
	b := &Binary{Path: path}
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	b.ino, b.size, b.mtime = inode(fi), fi.Size(), fi.ModTime()
	return b, nil
}

// Replaced reports whether another executable file now sits at the path.
// A missing file (in the middle of an update) is not a replacement yet.
func (b *Binary) Replaced() bool {
	fi, err := os.Stat(b.Path)
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
		return false
	}
	return inode(fi) != b.ino || fi.Size() != b.size || !fi.ModTime().Equal(b.mtime)
}

func inode(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Ino
	}
	return 0
}

// Run runs fn until it returns. If the binary is replaced meanwhile, fn's
// context is cancelled, and once fn has returned the process starts again
// from the new binary with the same arguments and environment. If the
// binary cannot be found, fn simply runs.
func Run(ctx context.Context, name string, fn func(context.Context) error) error {
	b, err := Self()
	if err != nil {
		return fn(ctx)
	}
	return run(ctx, b, name, fn)
}

func run(ctx context.Context, b *Binary, name string, fn func(context.Context) error) error {
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var replaced atomic.Bool
	go func() {
		t := time.NewTicker(Every)
		defer t.Stop()
		for {
			select {
			case <-cctx.Done():
				return
			case <-t.C:
				if b.Replaced() {
					log.Printf("%s: new binary at %s, restarting", name, b.Path)
					replaced.Store(true)
					cancel()
					return
				}
			}
		}
	}()
	err := fn(cctx)
	if !replaced.Load() || ctx.Err() != nil {
		return err
	}
	if xerr := exec(b.Path, os.Args, os.Environ()); xerr != nil {
		return errors.Join(err, xerr)
	}
	return nil // not reached: exec does not return on success
}
