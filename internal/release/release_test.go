package release

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
	"strings"
	"testing"
)

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"0.6.1", "0.6.0", true},
		{"v1.0.0", "0.9.9", true},
		{"0.6.0", "0.6.0", false},
		{"0.5.9", "0.6.0", false},
		{"0.10.0", "0.9.0", true},
		{"0.6.0", "dev", true},
		{"0.6.0", "v0.5.0-3-gabc123-dirty", true},
		{"dev", "0.6.0", false},
		{"0.7.0-rc1", "0.6.0", false},
	} {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func tarball(t *testing.T, binary string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range map[string]string{"hatrack_linux_amd64/README.md": "readme", "hatrack_linux_amd64/hat": binary} {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func fakeGitHub(t *testing.T, archive []byte, sum string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/"+Repo+"/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			http.Error(w, "no user agent", 403)
			return
		}
		w.Write([]byte(`{"tag_name":"v0.6.1"}`))
	})
	mux.HandleFunc("/"+Repo+"/releases/download/v0.6.1/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("0000  hatrack_linux_arm64.tar.gz\n" + sum + "  hatrack_linux_amd64.tar.gz\n"))
	})
	mux.HandleFunc("/"+Repo+"/releases/download/v0.6.1/hatrack_linux_amd64.tar.gz", func(w http.ResponseWriter, r *http.Request) {
		w.Write(archive)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	oldAPI, oldWeb := API, Web
	API, Web = srv.URL, srv.URL
	t.Cleanup(func() { API, Web = oldAPI, oldWeb })
}

func TestLatestAndFetch(t *testing.T) {
	archive := tarball(t, "#!new binary")
	h := sha256.Sum256(archive)
	fakeGitHub(t, archive, hex.EncodeToString(h[:]))
	ctx := context.Background()

	rel, err := Latest(ctx, http.DefaultClient)
	if err != nil || rel.Version() != "0.6.1" {
		t.Fatalf("Latest = %+v, %v", rel, err)
	}
	dst := filepath.Join(t.TempDir(), "hat.new")
	if err := Fetch(ctx, http.DefaultClient, rel, "amd64", dst); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(dst)
	if string(b) != "#!new binary" {
		t.Fatalf("extracted %q", b)
	}
	if fi, _ := os.Stat(dst); fi.Mode().Perm()&0o111 == 0 {
		t.Fatal("binary is not executable")
	}
}

func TestFetchRejectsWrongChecksum(t *testing.T) {
	fakeGitHub(t, tarball(t, "evil"), strings.Repeat("ab", 32))
	dst := filepath.Join(t.TempDir(), "hat.new")
	err := Fetch(context.Background(), http.DefaultClient, Release{Tag: "v0.6.1"}, "amd64", dst)
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("err = %v, want a checksum error", err)
	}
	if _, err := os.Stat(dst); err == nil {
		t.Fatal("nothing may be written when the checksum fails")
	}
}
