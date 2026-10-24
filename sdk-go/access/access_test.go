// SPDX-License-Identifier: Apache-2.0

package access

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMatchSiteSpecificity(t *testing.T) {
	sites := []Site{{"*", Ask}, {"*.example.org", Allow}, {"bank.example.org", Deny}, {"shop-*.example.org", Read}, {"evil.example", Deny}}
	for url, want := range map[string]string{
		"https://example.org/":        Allow, // *.example.org покрывает и apex
		"https://a.example.org/x":     Allow,
		"https://bank.example.org/":   Deny,
		"https://shop-1.example.org/": Read, // конкретнее *.example.org
		"https://other.test/":         Ask,
		"https://evil.example/":       Deny,
	} {
		s, ok := MatchSite(sites, url)
		if !ok || s.Access != want {
			t.Errorf("%s: %+v %v, want %s", url, s, ok, want)
		}
	}
	if _, ok := MatchSite(sites, "ftp://x"); ok {
		t.Fatal("not http")
	}
	if _, ok := MatchSite([]Site{{"a.example", Allow}}, "https://b.example/"); ok {
		t.Fatal("no match")
	}
	// Равная конкретность — строже.
	s, _ := MatchSite([]Site{{"a.example", Allow}, {"a.example", Deny}}, "https://a.example/")
	if s.Access != Deny {
		t.Fatal(s)
	}
}

func TestMatchFolder(t *testing.T) {
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, "notes", "work"), 0o755)
	folders := []Folder{{"~/notes", Append}, {"~/notes/work", Read}, {"~/notes/**/*.secret", Deny}, {"~", Ask}, {"/etc", Deny}}
	for p, want := range map[string]string{
		"~/notes/a.md":           Append,
		"~/notes/work/b.md":      Read,
		"~/notes/work/x.secret":  Deny,
		"~/other/c":              Ask,
		filepath.Join(home, "d"): Ask,
		"/etc/passwd":            Deny,
		"~/notes/../.ssh/id":     Ask, // .. схлопывается до сравнения
		"~/notesx/a":             Ask, // не ~/notes
	} {
		f, ok := MatchFolder(folders, p, home)
		if !ok || f.Access != want {
			t.Errorf("%s: %+v %v, want %s", p, f, ok, want)
		}
	}
	if _, ok := MatchFolder(folders, "relative/x", home); ok {
		t.Fatal("relative path matched")
	}
	if _, ok := MatchFolder(folders, "/opt/x", home); ok {
		t.Fatal("outside matched")
	}
}

func TestEffects(t *testing.T) {
	cases := []struct {
		mode string
		via  bool
		want string
	}{
		{Allow, false, Allow}, {Connector, false, Deny}, {Connector, true, Allow}, {Ask, false, Ask}, {Deny, true, Deny},
		{Read, false, Deny}, // у сайтов режима read нет
	}
	for _, c := range cases {
		if got := Effect(c.mode, c.via); got != c.want {
			t.Errorf("%+v: %s", c, got)
		}
	}
	// read — только у папок: сайту его задать нельзя, ни в списке Браузера, ни у коннектора.
	if err := CheckSite(Site{Domain: "a.example", Access: Read}, SiteModes); err == nil {
		t.Fatal("read accepted for a site")
	}
	if err := CheckSite(Site{Domain: "a.example", Access: Read}, ConnectorSiteModes); err == nil {
		t.Fatal("read accepted for a connector site")
	}
	if err := CheckFolder(Folder{Path: "~/x", Access: Read}, FolderModes); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ mode, op, want string }{
		{Allow, OpWrite, Allow}, {Append, OpAppend, Allow}, {Append, OpWrite, Ask}, {Read, OpList, Allow}, {Read, OpAppend, Ask},
		{Connector, OpRead, Deny}, {Ask, OpRead, Ask}, {Deny, OpRead, Deny},
	} {
		if got := FolderEffect(c.mode, c.op, false); got != c.want {
			t.Errorf("%+v: %s", c, got)
		}
	}
	if OpOf("files.append") != OpAppend || OpOf("files.read") != OpRead || OpOf("files.list") != OpList || OpOf("files.write") != OpWrite {
		t.Fatal("OpOf")
	}
}

func TestOpenAndSet(t *testing.T) {
	d, b, bl := OpenSites([]Site{{"*", Ask}, {"evil.example", Deny}, {"a.example", Allow}})
	if !bl || len(d) != 0 || len(b) != 1 || b[0] != "evil.example" {
		t.Fatal(d, b, bl)
	}
	d, b, bl = OpenSites([]Site{{"a.example", Read}, {"x.example", Deny}})
	if bl || len(d) != 1 || len(b) != 0 {
		t.Fatal(d, b, bl)
	}
	if f := OpenFolders([]Folder{{"~/a", Deny}, {"~/b", Read}}); len(f) != 1 || f[0] != "~/b" {
		t.Fatal(f)
	}
	s := SetSite([]Site{{"a.example", Read}}, "A.example", Deny)
	if len(s) != 1 || s[0].Access != Deny {
		t.Fatal(s)
	}
	if s = SetSite(s, "a.example", ""); len(s) != 0 {
		t.Fatal(s)
	}
	if err := CheckSite(Site{"bad host!", Allow}, SiteModes); err == nil {
		t.Fatal("bad domain accepted")
	}
	if err := CheckSite(Site{"a.example", Connector}, ConnectorSiteModes); err == nil {
		t.Fatal("connector mode for a connector accepted")
	}
	if err := CheckFolder(Folder{"relative", Read}, FolderModes); err == nil {
		t.Fatal("relative folder accepted")
	}
	parsed, err := ParseSites(SitesAny([]Site{{"a.example", Ask}}))
	if err != nil || len(parsed) != 1 || parsed[0].Domain != "a.example" {
		t.Fatal(parsed, err)
	}
}
