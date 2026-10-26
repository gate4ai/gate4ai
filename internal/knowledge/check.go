// SPDX-License-Identifier: AGPL-3.0-only

package knowledge

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/gate4ai/gate4ai/sdk-go/manifest"
)

// Report — результат проверки папки перед публикацией: ошибки не дают
// публиковать, предупреждения — посмотреть глазами.
type Report struct {
	Folder   string   `json:"folder"`
	OK       bool     `json:"ok"`
	Errors   []string `json:"errors,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
	Files    int      `json:"files"` // проверенных текстовых файлов
}

var (
	emailRe = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	phoneRe = regexp.MustCompile(`(\+7|\b8)[\s\-(]*\d{3}[\s\-)]*\d{3}[\s\-]*\d{2}[\s\-]*\d{2}\b`)
	longRe  = regexp.MustCompile(`\b\d{8,}\b`)
	// secretRe — значение рядом со словом «пароль», «токен», «ключ»: в коде
	// коннектора и рецепте таких быть не должно (секреты — только в Vault по алиасу).
	secretRe = regexp.MustCompile(`(?i)\b(password|passwd|pwd|token|secret|api[_-]?key|authorization|cookie)\b["']?\s*[:=]\s*["']?([^\s"',;)]{6,})`)
	// keyRe — длинная строка, похожая на ключ или токен.
	keyRe = regexp.MustCompile(`\b(?:[A-Za-z0-9+/]{40,}={0,2}|[0-9a-f]{40,}|eyJ[A-Za-z0-9_-]{20,}\.[A-Za-z0-9_-]{10,})\b`)
	// aliasRe — алиас Vault: в коде он допустим (имя), значение рядом — нет.
	aliasRe = regexp.MustCompile(`@[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*`)
)

// textExt — какие файлы читаются как текст.
var textExt = map[string]bool{".md": true, ".go": true, ".yaml": true, ".yml": true, ".json": true, ".txt": true, ".mod": true, ".gitignore": true}

// Check проверяет папку коннектора перед публикацией: манифест, рецепт,
// лицензия, README, .gitignore с сырыми записями, личные данные (почта,
// телефоны, длинные номера), секреты и ключи в текстах. Тестов и сборки не
// запускает — это делает агент (`go test ./...`).
func Check(dir string) Report {
	rep := Report{Folder: dir}
	errf := func(format string, a ...any) { rep.Errors = append(rep.Errors, fmt.Sprintf(format, a...)) }
	warnf := func(format string, a ...any) { rep.Warnings = append(rep.Warnings, fmt.Sprintf(format, a...)) }

	var man *manifest.Manifest
	if raw, err := os.ReadFile(filepath.Join(dir, ManifestFile)); err != nil {
		errf("%s: missing — the folder is not a connector yet (gate4ai connectors new)", ManifestFile)
	} else if m, err := manifest.Parse(raw); err != nil {
		errf("%s: %v", ManifestFile, err)
	} else {
		man = &m
		if m.Runtime != "" && m.Runtime != manifest.RuntimeWasm {
			errf("%s: runtime must be wasm (the only runtime of the store)", ManifestFile)
		}
	}
	if raw, err := os.ReadFile(filepath.Join(dir, RecipeFile)); err != nil {
		warnf("%s: missing — other agents learn the site from it; gate4ai connectors new --from-session writes one", RecipeFile)
	} else if r, _, err := ParseRecipe(raw); err != nil {
		errf("%v", err)
	} else {
		if r.Site == "" {
			errf("%s: site is empty", RecipeFile)
		}
		if man != nil {
			for _, t := range r.Tasks {
				if !hasAction(man, t.ID) {
					warnf("%s: task %s has no action in %s", RecipeFile, t.ID, ManifestFile)
				}
			}
		}
	}
	if _, err := os.Stat(filepath.Join(dir, LicenseFile)); err != nil {
		errf("%s: missing — the store accepts %s or another AGPL-compatible license", LicenseFile, License)
	}
	if _, err := os.Stat(filepath.Join(dir, ReadmeFile)); err != nil {
		warnf("%s: missing — search engines and other agents find the connector by it", ReadmeFile)
	}
	gi, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil || !strings.Contains(string(gi), ExploreDir+"/") {
		errf(".gitignore: must list %s/ — raw session records hold real personal data", ExploreDir)
	}
	if st, err := os.Stat(filepath.Join(dir, ExploreDir)); err == nil && st.IsDir() {
		warnf("%s/: raw session records are in the folder; .gitignore keeps them out of git — delete them when the fixtures are done", ExploreDir)
	}
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if p != dir && (strings.HasPrefix(name, ".") || name == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if name == "go.sum" || (!textExt[filepath.Ext(name)] && name != ".gitignore") {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		rep.Files++
		scanText(p, rel, errf, warnf)
		return nil
	})
	rep.OK = len(rep.Errors) == 0
	return rep
}

func hasAction(m *manifest.Manifest, name string) bool {
	for _, a := range m.Actions {
		if a.Name == name {
			return true
		}
	}
	return false
}

// scanText ищет в файле личные данные и секреты, по строкам.
func scanText(path, rel string, errf, warnf func(string, ...any)) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	n := 0
	emails, phones, longs := 0, 0, 0
	for sc.Scan() {
		n++
		line := sc.Text()
		if m := secretRe.FindStringSubmatch(line); m != nil && !aliasRe.MatchString(m[2]) && !placeholder(m[2]) {
			errf("%s:%d: looks like a secret value next to %q — secrets live in the vault, refer to them by alias", rel, n, m[1])
		}
		if keyRe.MatchString(line) && !strings.Contains(line, "sha256") {
			errf("%s:%d: a long key-like string — a token, key or hash of a real session?", rel, n)
		}
		for _, m := range emailRe.FindAllString(line, -1) {
			if !strings.HasSuffix(strings.ToLower(m), "@example.com") && !strings.HasSuffix(strings.ToLower(m), "@example.org") {
				emails++
				if emails == 1 {
					warnf("%s:%d: an e-mail address (%s) — personal data? gate4ai fixture scrub --auto replaces them", rel, n, m)
				}
			}
		}
		if phoneRe.MatchString(line) && !strings.Contains(line, "+7 900 000-00-") {
			phones++
			if phones == 1 {
				warnf("%s:%d: a phone number — personal data? gate4ai fixture scrub --auto replaces them", rel, n)
			}
		}
		for _, m := range longRe.FindAllString(line, -1) {
			if strings.Trim(m, "0") != "" && len(strings.TrimLeft(m, "0")) > 2 {
				longs++
				if longs == 1 {
					warnf("%s:%d: a long number (%s) — an id, a policy or an order number? gate4ai fixture scrub --auto replaces them", rel, n, m)
				}
			}
		}
	}
}

// placeholder — значение-заглушка, не секрет.
func placeholder(v string) bool {
	l := strings.ToLower(strings.Trim(v, "<>{}[]$"))
	for _, p := range []string{"string", "secret", "password", "token", "xxx", "...", "…", "redacted", "example", "setting:", "env:", "your_", "<"} {
		if strings.HasPrefix(l, p) || l == p {
			return true
		}
	}
	return false
}
