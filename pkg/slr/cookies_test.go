package slr

import (
	"testing"
)

func TestSplitIniSections(t *testing.T) {
	ini := `[Profile0]
Name=default
IsRelative=1
Path=Profiles/abc.default
Default=1

[Profile1]
Name=dev
IsRelative=1
Path=Profiles/xyz.dev

[Install4F96D1932A9F858E]
Default=Profiles/abc.default
Locked=1
`

	sections := splitIniSections(ini)
	if len(sections) != 3 {
		t.Fatalf("expected 3 sections, got %d: %#v", len(sections), sections)
	}
	for i, s := range sections {
		if len(s) == 0 || s[0] != '[' {
			t.Errorf("section %d does not start with '[': %q", i, s)
		}
	}
	if !iniDefaultSectionRE.MatchString(sections[0]) {
		t.Errorf("expected Profile0 to be the default section: %q", sections[0])
	}
	if m := iniPathRE.FindStringSubmatch(sections[0]); m == nil || m[1] != "Profiles/abc.default" {
		t.Errorf("could not read Path from Profile0: %#v", m)
	}
	if !iniDefaultRE.MatchString(sections[2]) {
		t.Errorf("expected Install section to carry Default=: %q", sections[2])
	}
}

// Regression test: a Python lookahead pattern used to be compiled with
// MustCompile here, which panicked and reset HTTP connections whenever the
// cookie source was queried.
func TestCookieFunctionsDoNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("cookie lookup panicked: %v", r)
		}
	}()

	_ = CookieConfigKey()
	_ = CookieSource()
	header, _ := CookieHeader()
	_ = header
	_ = ExtraHeaders()
}
