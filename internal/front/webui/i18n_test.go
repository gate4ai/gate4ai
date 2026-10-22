// SPDX-License-Identifier: AGPL-3.0-only

package webui

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Строки для перевода: литералы T/TH в шаблонах и M(…), tr(…, …), Word(…),
// redirect(…, "…") и подписи в коде.
var (
	tplKeyRe = regexp.MustCompile(`\{\{-?\s*(?:T|TH)\s+("(?:[^"\\]|\\.)*")`)
	goKeyRes = []*regexp.Regexp{
		regexp.MustCompile(`\bM\(\s*("(?:[^"\\]|\\.)*")`),
		regexp.MustCompile(`\btr\([^,()]+,\s*("(?:[^"\\]|\\.)*")\)`),
		regexp.MustCompile(`\bWord\(("(?:[^"\\]|\\.)*")\)`),
		regexp.MustCompile(`\bredirect\([^,]+,\s*("(?:[^"\\]|\\.)*")\)`),
		regexp.MustCompile(`errors\.New\(("(?:[^"\\]|\\.)*")\)`),
		regexp.MustCompile(`(?:Title|Text|Button|Label|Help|Description|Note|Reason|Restart):\s*("(?:[^"\\]|\\.)*")`),
	}
)

// i18nKeys — все английские строки Web UI, которым нужен перевод.
func i18nKeys(t *testing.T) []string {
	t.Helper()
	keys := map[string]bool{}
	add := func(q string) {
		s, err := strconv.Unquote(q)
		if err != nil || strings.TrimSpace(s) == "" || !regexp.MustCompile(`[A-Za-z]{2}`).MatchString(s) {
			return
		}
		keys[s] = true
	}
	tpls, _ := filepath.Glob("templates/*.html")
	for _, f := range tpls {
		b, _ := os.ReadFile(f)
		for _, m := range tplKeyRe.FindAllStringSubmatch(string(b), -1) {
			add(m[1])
		}
	}
	gos, _ := filepath.Glob("*.go")
	for _, f := range gos {
		if strings.HasSuffix(f, "_test.go") || strings.HasPrefix(f, "i18n") {
			continue
		}
		b, _ := os.ReadFile(f)
		for _, re := range goKeyRes {
			for _, m := range re.FindAllStringSubmatch(string(b), -1) {
				add(m[1])
			}
		}
	}
	// Словари слов и подписей.
	for _, m := range []map[string]string{classWords, classVerbs, effectWords, viaWords, channelWords, titles, durWords} {
		for _, v := range m {
			add(strconv.Quote(v))
		}
	}
	for _, g := range groupWarns {
		for _, v := range g {
			add(strconv.Quote(v))
		}
	}
	for _, g := range groupTitles {
		for _, v := range g {
			add(strconv.Quote(v))
		}
	}
	for _, c := range condKinds {
		add(strconv.Quote(c.Title))
	}
	for _, v := range versionComments {
		add(strconv.Quote(v.f))
	}
	for _, h := range settingHints {
		for _, f := range h {
			add(strconv.Quote(f.Label))
			add(strconv.Quote(f.Help))
		}
	}
	for _, p := range mailProviders {
		add(strconv.Quote(p.Help))
	}
	for _, t := range Templates {
		add(strconv.Quote(t.Title))
		add(strconv.Quote(t.Description))
		for _, p := range t.Params {
			add(strconv.Quote(p.Label))
		}
	}
	out := make([]string, 0, len(keys))
	for k := range keys {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Каждая строка интерфейса переведена на русский, а формат перевода
// совпадает с исходником по числу и видам подстановок.
func TestRussianCatalogComplete(t *testing.T) {
	verbs := regexp.MustCompile(`%[-+# 0-9.]*[a-zA-Z%]`)
	var missing []string
	for _, k := range i18nKeys(t) {
		v, ok := ru[k]
		if !ok {
			missing = append(missing, strconv.Quote(k))
			continue
		}
		a, b := verbs.FindAllString(k, -1), verbs.FindAllString(v, -1)
		if strings.Join(a, " ") != strings.Join(b, " ") {
			t.Errorf("%q → %q: format verbs differ (%v vs %v)", k, v, a, b)
		}
	}
	if len(missing) > 0 {
		if os.Getenv("I18N_DUMP") != "" {
			_ = os.WriteFile(os.Getenv("I18N_DUMP"), []byte(strings.Join(missing, "\n")+"\n"), 0o644)
		}
		t.Fatalf("%d strings have no Russian translation, e.g.:\n%s", len(missing), strings.Join(missing[:min(len(missing), 20)], "\n"))
	}
}

func TestLanguageChoice(t *testing.T) {
	u := &UI{}
	for _, c := range []struct{ cookie, accept, want string }{
		{"", "", "en"},
		{"", "ru-RU,ru;q=0.9,en;q=0.8", "ru"},
		{"", "de-DE,en;q=0.5", "en"},
		{"", "de-DE", "en"},
		{"en", "ru", "en"},
		{"ru", "en-US", "ru"},
	} {
		r, _ := http.NewRequest("GET", "/", nil)
		if c.accept != "" {
			r.Header.Set("Accept-Language", c.accept)
		}
		if c.cookie != "" {
			r.AddCookie(&http.Cookie{Name: langCookie, Value: c.cookie})
		}
		if got := u.lang(r); got != c.want {
			t.Errorf("cookie %q accept %q: %s, want %s", c.cookie, c.accept, got, c.want)
		}
	}
	if got := M("Agent %s added. Now connect it.", "bot").In("en"); got != "Agent bot added. Now connect it." {
		t.Fatal(got)
	}
}
