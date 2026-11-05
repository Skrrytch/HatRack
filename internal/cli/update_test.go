package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"hatrack/internal/release"
)

// updateRig serves a fake release v0.6.1 and installs an "old" binary.
func updateRig(t *testing.T, installed, exeDir string) (exe string, restarted *bool, out *bytes.Buffer) {
	t.Helper()
	for _, v := range []string{"LANGUAGE", "LC_ALL", "LC_MESSAGES", "LANG"} {
		t.Setenv(v, "en_US.UTF-8")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // not the real agent.yaml and its language
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := "new binary"
	tw.WriteHeader(&tar.Header{Name: "hatrack_linux_" + runtime.GOARCH + "/hat", Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
	tw.Write([]byte(body))
	tw.Close()
	gz.Close()
	archive := buf.Bytes()
	sum := sha256.Sum256(archive)

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/"+release.Repo+"/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"tag_name":"v0.6.1"}`))
	})
	dl := "/" + release.Repo + "/releases/download/v0.6.1/"
	mux.HandleFunc(dl+"checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(hex.EncodeToString(sum[:]) + "  hatrack_linux_" + runtime.GOARCH + ".tar.gz\n"))
	})
	mux.HandleFunc(dl+"hatrack_linux_"+runtime.GOARCH+".tar.gz", func(w http.ResponseWriter, _ *http.Request) { w.Write(archive) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	oldAPI, oldWeb, oldVersion, oldExe, oldRestart := release.API, release.Web, Version, updateExe, updateRestart
	t.Cleanup(func() {
		release.API, release.Web, Version, updateExe, updateRestart = oldAPI, oldWeb, oldVersion, oldExe, oldRestart
	})
	release.API, release.Web, Version = srv.URL, srv.URL, installed

	exe = filepath.Join(exeDir, "hat")
	os.MkdirAll(exeDir, 0o755)
	os.WriteFile(exe, []byte("old binary"), 0o755)
	updateExe = func() (string, error) { return exe, nil }
	restarted = new(bool)
	updateRestart = func(string) { *restarted = true }
	out = &bytes.Buffer{}
	return exe, restarted, out
}

func runUpdateCmd(t *testing.T, out *bytes.Buffer, args ...string) error {
	t.Helper()
	a := &App{Out: out, Err: out, In: strings.NewReader("")}
	root := a.Root()
	root.SetArgs(append([]string{"update"}, args...))
	return root.ExecuteContext(context.Background())
}

func TestUpdateReplacesBinary(t *testing.T) {
	exe, restarted, out := updateRig(t, "0.6.0", t.TempDir())
	if err := runUpdateCmd(t, out); err != nil {
		t.Fatal(err, out)
	}
	if b, _ := os.ReadFile(exe); string(b) != "new binary" {
		t.Fatalf("binary is %q", b)
	}
	if !*restarted {
		t.Error("services and tray were not restarted")
	}
	if !strings.Contains(out.String(), "Updated to 0.6.1") {
		t.Errorf("output: %s", out)
	}
}

func TestUpdateCheckAndCurrent(t *testing.T) {
	exe, _, out := updateRig(t, "0.6.0", t.TempDir())
	if err := runUpdateCmd(t, out, "--check"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "old binary" || !strings.Contains(out.String(), "0.6.1 is available") {
		t.Fatalf("--check changed the binary or said nothing: %s", out)
	}

	Version = "0.6.1"
	out.Reset()
	runUpdateCmd(t, out)
	if !strings.Contains(out.String(), "up to date") {
		t.Errorf("output: %s", out)
	}
}

func TestUpdateLeavesPackagesAndSourceBuilds(t *testing.T) {
	// a binary below /usr belongs to the package manager
	exe, _, out := updateRig(t, "0.6.0", t.TempDir())
	updateExe = func() (string, error) { return "/usr/bin/hat", nil }
	if err := runUpdateCmd(t, out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "hatrack_"+runtime.GOARCH+".deb") {
		t.Errorf("no package hint: %s", out)
	}

	// a build from a checkout is updated with update.sh, unless forced
	updateExe = func() (string, error) { return exe, nil }
	Version = "v0.6.0-4-gabc123-dirty"
	out.Reset()
	if err := runUpdateCmd(t, out); err == nil || !strings.Contains(err.Error(), "update.sh") {
		t.Fatalf("err = %v", err)
	}
	if err := runUpdateCmd(t, out, "--force"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "new binary" {
		t.Fatal("--force did not replace the binary")
	}
}
