// SPDX-License-Identifier: AGPL-3.0-only

// Package knowledge — папка знаний: что агент узнал о сайтах. Каталог папок
// коннекторов (одна папка — один коннектор, по правилу магазина gate4ai-<имя>):
// рецепт RECIPE.md (шаги, ловушки, статус проверенности), манифест и код
// коннектора, фикстуры тестов. Свои папки gate4ai складывает просто файлами —
// git init, коммиты, push и gh делает владелец; чужие попадают сюда git clone
// (connectors install <git-url>). Схема — docs/connector-sharing-ux.md.
package knowledge

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gate4ai/gate4ai/sdk-go/domain"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"
)

// Имена файлов папки коннектора.
const (
	RecipeFile   = "RECIPE.md"
	ManifestFile = "manifest.yaml"
	ReadmeFile   = "README.md"
	LicenseFile  = "LICENSE"
	ExploreDir   = ".explore"
	// RepoPrefix — префикс имени папки и репозитория коннектора (правило магазина).
	RepoPrefix = "gate4ai-"
	// Topic — тема репозитория на GitHub, по которой коннекторы находят.
	Topic = "gate4ai-connector"
	// License — лицензия всей папки (код, рецепт, README): правило магазина.
	License = "AGPL-3.0-only"
)

// DefaultDir — папка знаний по умолчанию: ~/gate4ai-knowledge.
func DefaultDir(userHome string) string {
	if userHome == "" {
		userHome, _ = os.UserHomeDir()
	}
	return filepath.Join(userHome, "gate4ai-knowledge")
}

// Resolve — папка знаний из настройки (пусто — DefaultDir; "~" — домашний каталог).
func Resolve(setting, userHome string) string {
	if setting == "" {
		return DefaultDir(userHome)
	}
	if userHome == "" {
		userHome, _ = os.UserHomeDir()
	}
	if setting == "~" || strings.HasPrefix(setting, "~/") {
		return filepath.Join(userHome, strings.TrimPrefix(setting, "~"))
	}
	return setting
}

// FolderName — имя папки коннектора: gate4ai-<имя> (подчёркивания — дефисами,
// как в именах репозиториев).
func FolderName(connector string) string {
	return RepoPrefix + strings.ReplaceAll(connector, "_", "-")
}

// Recipe — шапка RECIPE.md: что умеет коннектор на сайте и насколько это проверено.
type Recipe struct {
	Site       string         `yaml:"site" json:"site"`
	Domains    []string       `yaml:"domains,omitempty" json:"domains,omitempty"`
	Tasks      []Task         `yaml:"tasks,omitempty" json:"tasks,omitempty"`
	Login      *RecipeLogin   `yaml:"login,omitempty" json:"login,omitempty"`
	HumanSteps []string       `yaml:"human_steps,omitempty" json:"human_steps,omitempty"`
	Extra      map[string]any `yaml:",inline" json:"-"`
}

// Task — задача рецепта (действие коннектора) и её статус проверенности.
type Task struct {
	ID           string `yaml:"id" json:"id"`
	Title        string `yaml:"title,omitempty" json:"title,omitempty"`
	Class        string `yaml:"class,omitempty" json:"class,omitempty"`
	Status       string `yaml:"status,omitempty" json:"status,omitempty"` // draft | verified
	VerifiedRuns int    `yaml:"verified_runs" json:"verified_runs"`
	LastVerified string `yaml:"last_verified,omitempty" json:"last_verified,omitempty"` // YYYY-MM-DD
	Gate4ai      string `yaml:"gate4ai,omitempty" json:"gate4ai,omitempty"`
}

// RecipeLogin — как входят на сайт: origin входа и коннектор входа, если есть.
type RecipeLogin struct {
	Bind string `yaml:"bind,omitempty" json:"bind,omitempty"`
	Via  string `yaml:"via,omitempty" json:"via,omitempty"`
	Kind string `yaml:"kind,omitempty" json:"kind,omitempty"` // form | form+sms | via | human
}

// Статусы задач.
const (
	StatusDraft    = "draft"
	StatusVerified = "verified"
	// VerifiedAfter — столько подтверждённых прогонов делают действие verified.
	VerifiedAfter = 3
)

// ParseRecipe разбирает RECIPE.md: YAML между строками `---` и тело.
func ParseRecipe(raw []byte) (*Recipe, string, error) {
	s := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if !strings.HasPrefix(s, "---\n") {
		return nil, s, errors.New("RECIPE.md: no YAML front matter (--- … ---)")
	}
	rest := s[4:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return nil, s, errors.New("RECIPE.md: front matter is not closed with ---")
	}
	head, body := rest[:end], rest[end+4:]
	var r Recipe
	if err := yaml.Unmarshal([]byte(head), &r); err != nil {
		return nil, body, errors.New("RECIPE.md: " + err.Error())
	}
	for i := range r.Tasks {
		if r.Tasks[i].Status == "" {
			r.Tasks[i].Status = StatusDraft
		}
	}
	return &r, strings.TrimLeft(body, "\n"), nil
}

// FormatRecipe собирает RECIPE.md из шапки и тела.
func FormatRecipe(r *Recipe, body string) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString("---\n")
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(r); err != nil {
		return nil, err
	}
	_ = enc.Close()
	b.WriteString("---\n")
	if body != "" {
		b.WriteString(strings.TrimLeft(body, "\n"))
		if !strings.HasSuffix(body, "\n") {
			b.WriteString("\n")
		}
	}
	return b.Bytes(), nil
}

// Task ищет задачу рецепта.
func (r *Recipe) Task(id string) *Task {
	for i := range r.Tasks {
		if r.Tasks[i].ID == id {
			return &r.Tasks[i]
		}
	}
	return nil
}

// Matches — рецепт относится к этому сайту (домен или шаблон *.example.org).
func (r *Recipe) Matches(host string) bool {
	host = strings.ToLower(host)
	for _, d := range append([]string{r.Site}, r.Domains...) {
		if d != "" && (domain.HostMatches(host, d) || domain.HostMatches(d, host) || strings.HasSuffix(d, "."+host)) {
			return true
		}
	}
	return false
}

// Git — состояние git папки, только чтение: gate4ai ничего не инициализирует
// и не отправляет.
type Git struct {
	Repo   bool   `json:"repo"`             // есть .git
	Remote string `json:"remote,omitempty"` // url origin
	// Dirty — есть незакоммиченные правки; nil — git недоступен или не репозиторий.
	Dirty *bool `json:"dirty,omitempty"`
}

// State — состояние папки для владельца: только файлы, готов к публикации,
// есть git, опубликован, есть неопубликованные правки.
func (g Git) State() string {
	switch {
	case !g.Repo:
		return "files"
	case g.Remote == "":
		return "git"
	case g.Dirty != nil && *g.Dirty:
		return "unpublished_changes"
	default:
		return "published"
	}
}

var remoteURLRe = regexp.MustCompile(`(?m)^\s*url\s*=\s*(\S+)`)

// ReadGit читает состояние git папки: .git/config — origin, `git status` — правки.
func ReadGit(dir string) Git {
	var g Git
	if st, err := os.Stat(filepath.Join(dir, ".git")); err != nil || !st.IsDir() {
		return g
	}
	g.Repo = true
	if cfg, err := os.ReadFile(filepath.Join(dir, ".git", "config")); err == nil {
		g.Remote = originURL(string(cfg))
	}
	if git, err := exec.LookPath("git"); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, git, "-C", dir, "status", "--porcelain")
		cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
		if out, err := cmd.Output(); err == nil {
			dirty := len(bytes.TrimSpace(out)) > 0
			g.Dirty = &dirty
		}
	}
	return g
}

// originURL — url секции [remote "origin"] конфигурации git (первый remote, если origin нет).
func originURL(cfg string) string {
	section, first := "", ""
	sc := bufio.NewScanner(strings.NewReader(cfg))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			section = line
			continue
		}
		if !strings.HasPrefix(section, `[remote `) {
			continue
		}
		if m := remoteURLRe.FindStringSubmatch(line); m != nil {
			if section == `[remote "origin"]` {
				return m[1]
			}
			if first == "" {
				first = m[1]
			}
		}
	}
	return first
}

// Folder — папка коннектора в папке знаний.
type Folder struct {
	Name      string             `json:"name"` // имя папки (gate4ai-rzd)
	Path      string             `json:"path"`
	Connector string             `json:"connector,omitempty"` // из манифеста
	Manifest  *manifest.Manifest `json:"-"`
	Recipe    *Recipe            `json:"recipe,omitempty"`
	Git       Git                `json:"git"`
	State     string             `json:"state"`
	Problems  []string           `json:"problems,omitempty"` // манифест или рецепт не разбираются
}

// Sites — сайты папки: из манифеста и рецепта.
func (f Folder) Sites() []string {
	seen := map[string]bool{}
	var out []string
	add := func(d string) {
		d = strings.ToLower(strings.TrimSpace(d))
		if d != "" && !strings.Contains(d, "${") && !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	if f.Manifest != nil {
		for _, s := range f.Manifest.Sites {
			add(s.Domain)
		}
	}
	if f.Recipe != nil {
		add(f.Recipe.Site)
		for _, d := range f.Recipe.Domains {
			add(d)
		}
	}
	return out
}

// MatchesSite — папка относится к сайту host.
func (f Folder) MatchesSite(host string) bool {
	host = strings.ToLower(host)
	for _, d := range f.Sites() {
		if domain.HostMatches(host, d) || domain.HostMatches(d, host) {
			return true
		}
	}
	return false
}

// Read читает одну папку коннектора.
func Read(path string) Folder {
	f := Folder{Name: filepath.Base(path), Path: path}
	if raw, err := os.ReadFile(filepath.Join(path, ManifestFile)); err == nil {
		m, err := manifest.Parse(raw)
		if err != nil {
			f.Problems = append(f.Problems, ManifestFile+": "+err.Error())
		} else {
			f.Manifest, f.Connector = &m, m.Connector
		}
	}
	if raw, err := os.ReadFile(filepath.Join(path, RecipeFile)); err == nil {
		r, _, err := ParseRecipe(raw)
		if err != nil {
			f.Problems = append(f.Problems, err.Error())
		} else {
			f.Recipe = r
		}
	}
	f.Git = ReadGit(path)
	f.State = f.Git.State()
	return f
}

// List — папки коннекторов в папке знаний (с манифестом или рецептом), по имени.
func List(dir string) ([]Folder, error) {
	ents, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Folder
	for _, e := range ents {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		_, errM := os.Stat(filepath.Join(p, ManifestFile))
		_, errR := os.Stat(filepath.Join(p, RecipeFile))
		if errM != nil && errR != nil {
			continue
		}
		out = append(out, Read(p))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Find — папка коннектора по имени папки или имени коннектора.
func Find(dir, name string) (Folder, bool) {
	list, err := List(dir)
	if err != nil {
		return Folder{}, false
	}
	for _, f := range list {
		if f.Name == name || f.Connector == name || f.Name == FolderName(name) {
			return f, true
		}
	}
	return Folder{}, false
}
