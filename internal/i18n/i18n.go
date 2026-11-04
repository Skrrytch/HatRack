// Package i18n provides the message catalogs. Every user-visible text goes
// through T; English is the fallback for strings missing in another language.
package i18n

import (
	"embed"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/goccy/go-yaml"
)

//go:embed locales/*.yaml
var files embed.FS

var (
	mu       sync.RWMutex
	catalogs = map[string]map[string]string{}
	current  = "en"
)

func init() {
	for _, lang := range []string{"en", "de"} {
		b, err := files.ReadFile("locales/" + lang + ".yaml")
		if err != nil {
			panic(err)
		}
		m := map[string]string{}
		if err := yaml.Unmarshal(b, &m); err != nil {
			panic(fmt.Sprintf("locale %s: %v", lang, err))
		}
		catalogs[lang] = m
	}
}

// Languages lists the supported language codes.
func Languages() []string { return []string{"en", "de"} }

// Set selects the language for T. Unknown codes select English.
func Set(lang string) {
	mu.Lock()
	defer mu.Unlock()
	if _, ok := catalogs[lang]; ok {
		current = lang
	} else {
		current = "en"
	}
}

// Current returns the selected language.
func Current() string {
	mu.RLock()
	defer mu.RUnlock()
	return current
}

// Normalize maps a locale such as de_DE.UTF-8 or de-AT to a supported code,
// or returns "" if there is none.
func Normalize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if i := strings.IndexAny(s, "_-.@"); i >= 0 {
		s = s[:i]
	}
	if _, ok := catalogs[s]; ok {
		return s
	}
	return ""
}

// Resolve picks the language: the configured value (auto, en, de), then
// LC_ALL, LC_MESSAGES and LANG, then English.
func Resolve(configured string) string {
	if l := Normalize(configured); l != "" {
		return l
	}
	for _, env := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if l := Normalize(os.Getenv(env)); l != "" {
			return l
		}
	}
	return "en"
}

// FromAcceptLanguage picks the best supported language of an
// Accept-Language header, or "en".
func FromAcceptLanguage(h string) string {
	for _, part := range strings.Split(h, ",") {
		tag := strings.TrimSpace(strings.SplitN(part, ";", 2)[0])
		if l := Normalize(tag); l != "" {
			return l
		}
	}
	return "en"
}

// In translates key into lang, falling back to English and then to the key.
func In(lang, key string, args ...any) string {
	mu.RLock()
	defer mu.RUnlock()
	s, ok := catalogs[lang][key]
	if !ok {
		s, ok = catalogs["en"][key]
	}
	if !ok {
		return key
	}
	if len(args) == 0 {
		return s
	}
	return fmt.Sprintf(s, args...)
}

// T translates key into the current language.
func T(key string, args ...any) string { return In(Current(), key, args...) }

// Keys returns the keys of a catalog (used by tests).
func Keys(lang string) []string {
	mu.RLock()
	defer mu.RUnlock()
	var out []string
	for k := range catalogs[lang] {
		out = append(out, k)
	}
	return out
}
