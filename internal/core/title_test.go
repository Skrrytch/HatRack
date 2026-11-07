package core

import "testing"

func TestNormalizeTitle(t *testing.T) {
	for in, want := range map[string]string{
		"◑ Automatische Kontexterkennung": "Automatische Kontexterkennung",
		"✳ Automatische Kontexterkennung": "Automatische Kontexterkennung",
		"⠂ Task":                          "Task",
		"● main.go — HatRack":             "main.go — HatRack",
		"*notes.txt - gedit":              "notes.txt - gedit",
		"🔴️ Live":                         "Live",
		"(3) Posteingang - Mail":          "Posteingang - Mail",
		"[12] • Chat":                     "Chat",
		"Notes on C++":                    "Notes on C++",
		"Timer 12:03 – 45 %":              "Timer 12:03 – 45 %",
		"Inbox (2)":                       "Inbox (2)",
		"a   b\tc":                        "a b c",
		"(3)":                             "",
		"◐":                               "",
		"":                                "",
	} {
		if got := NormalizeTitle(in); got != want {
			t.Errorf("NormalizeTitle(%q) = %q, want %q", in, got, want)
		}
	}
}
