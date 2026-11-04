package i18n

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func TestCatalogsHaveSameKeys(t *testing.T) {
	en, de := Keys("en"), Keys("de")
	sort.Strings(en)
	sort.Strings(de)
	if strings.Join(en, " ") != strings.Join(de, " ") {
		have := map[string]bool{}
		for _, k := range en {
			have[k] = true
		}
		for _, k := range de {
			if !have[k] {
				t.Errorf("only in de: %s", k)
			}
			delete(have, k)
		}
		for k := range have {
			t.Errorf("only in en: %s", k)
		}
	}
}

// Every message key used in the source must exist in the English catalog.
func TestKeysInSourceExist(t *testing.T) {
	re := regexp.MustCompile(`"((?:cli|err|web|notify|panel|ui|tray)\.[a-z_.]+)"`)
	known := map[string]bool{}
	for _, k := range Keys("en") {
		known[k] = true
	}
	filepath.Walk("..", func(path string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() || !(strings.HasSuffix(path, ".go") || strings.HasSuffix(path, ".html")) || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, _ := os.ReadFile(path)
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			if !known[m[1]] && m[1] != "tray.sock" { // a file name, not a message
				t.Errorf("%s: unknown message key %s", path, m[1])
			}
		}
		return nil
	})
	// keys that are built at run time
	for _, k := range []string{"err.hat.notfound", "err.hat.none", "err.hat.ambiguous", "err.activity.notfound", "err.activity.none", "err.activity.ambiguous"} {
		if !known[k] {
			t.Errorf("missing %s", k)
		}
	}
}

func TestResolveAndFallback(t *testing.T) {
	t.Setenv("LC_ALL", "de_DE.UTF-8")
	if Resolve("auto") != "de" || Resolve("en") != "en" {
		t.Fatal("language resolution")
	}
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_MESSAGES", "")
	t.Setenv("LANG", "fr_FR.UTF-8")
	if Resolve("") != "en" {
		t.Fatal("unsupported locale should fall back to English")
	}
	if FromAcceptLanguage("fr;q=0.9, de-AT;q=0.8, en;q=0.5") != "de" {
		t.Fatal("accept-language")
	}
	if In("de", "nonexistent.key") != "nonexistent.key" {
		t.Fatal("unknown key is returned as is")
	}
	if In("de", "cli.nohat") != "Kein Hut auf" || In("en", "cli.nohat") != "No hat on" {
		t.Fatal("translation")
	}
}
