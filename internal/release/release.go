// Package release finds and downloads HatRack releases on GitHub, for
// `hat update`. A download is only used when its SHA-256 matches the
// release's checksums.txt.
package release

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"runtime"
	"strconv"
	"strings"
)

// Repo is the GitHub repository of HatRack.
const Repo = "Skrrytch/HatRack"

// API and Web are the GitHub addresses; tests point them elsewhere.
var (
	API = "https://api.github.com"
	Web = "https://github.com"
)

// UserAgent identifies the client to GitHub, which requires one.
var UserAgent = "hatrack"

// Release is one published version.
type Release struct {
	Tag string `json:"tag_name"`
}

// Version is the tag without the leading "v".
func (r Release) Version() string { return strings.TrimPrefix(r.Tag, "v") }

// Asset names do not contain the version, so the "latest" links in the
// README and in install.sh never change.
func archiveName(arch string) string { return "hatrack_linux_" + arch + ".tar.gz" }

// DebName is the file name of the Debian package for an architecture.
func DebName(arch string) string { return "hatrack_" + arch + ".deb" }

// DownloadURL is the address of a release file.
func DownloadURL(tag, file string) string {
	return fmt.Sprintf("%s/%s/releases/download/%s/%s", Web, Repo, tag, file)
}

// Arch is this computer's architecture as the release files name it.
func Arch() (string, error) {
	switch runtime.GOARCH {
	case "amd64", "arm64":
		return runtime.GOARCH, nil
	}
	return "", fmt.Errorf("no release for architecture %s", runtime.GOARCH)
}

// Latest asks GitHub for the newest release.
func Latest(ctx context.Context, c *http.Client) (Release, error) {
	var r Release
	body, err := get(ctx, c, fmt.Sprintf("%s/repos/%s/releases/latest", API, Repo))
	if err != nil {
		return r, err
	}
	defer body.Close()
	if err := json.NewDecoder(body).Decode(&r); err != nil {
		return r, err
	}
	if r.Tag == "" {
		return r, errors.New("release without a tag")
	}
	return r, nil
}

// Fetch downloads the release archive for arch, checks it against
// checksums.txt and writes the binary `hat` to dst (mode 0755).
func Fetch(ctx context.Context, c *http.Client, rel Release, arch, dst string) error {
	name := archiveName(arch)
	want, err := checksum(ctx, c, rel.Tag, name)
	if err != nil {
		return err
	}
	body, err := get(ctx, c, DownloadURL(rel.Tag, name))
	if err != nil {
		return err
	}
	defer body.Close()
	tmp, err := os.CreateTemp("", "hatrack-*.tar.gz")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), body); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("checksum of %s does not match (got %s, want %s)", name, got, want)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	return extractHat(tmp, dst)
}

// checksum reads the expected SHA-256 of file from checksums.txt.
func checksum(ctx context.Context, c *http.Client, tag, file string) (string, error) {
	body, err := get(ctx, c, DownloadURL(tag, "checksums.txt"))
	if err != nil {
		return "", err
	}
	defer body.Close()
	sc := bufio.NewScanner(body)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == file {
			return strings.ToLower(f[0]), nil
		}
	}
	return "", fmt.Errorf("%s is not in checksums.txt", file)
}

// extractHat writes the file named "hat" from a .tar.gz to dst.
func extractHat(r io.Reader, dst string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return errors.New("the archive contains no hat binary")
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg || path.Base(hdr.Name) != "hat" {
			continue
		}
		f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, io.LimitReader(tr, 512<<20)); err != nil {
			f.Close()
			return err
		}
		return f.Close()
	}
}

func get(ctx context.Context, c *http.Client, url string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return resp.Body, nil
}

// parse reads "1.2.3" (a "v" in front and anything after "-" or "+" are
// ignored). ok is false for versions that are not releases, such as "dev"
// or a git description of a checkout.
func parse(v string) (n [3]int, ok bool) {
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		return n, false // pre-release or local build
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return n, false
	}
	for i, p := range parts {
		x, err := strconv.Atoi(p)
		if err != nil || x < 0 {
			return n, false
		}
		n[i] = x
	}
	return n, true
}

// IsRelease reports whether v is the version of a published release.
func IsRelease(v string) bool { _, ok := parse(v); return ok }

// Newer reports whether version a is newer than b. Versions that are not
// releases are never newer, and every release is newer than them.
func Newer(a, b string) bool {
	x, okA := parse(a)
	y, okB := parse(b)
	if !okA {
		return false
	}
	if !okB {
		return true
	}
	for i := range x {
		if x[i] != y[i] {
			return x[i] > y[i]
		}
	}
	return false
}
