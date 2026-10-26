// SPDX-License-Identifier: AGPL-3.0-only

// Package gitrepo — то немногое, что gate4ai делает с git сам: читает состояние
// папки (репозиторий, remote, правки, коммит), клонирует публичные репозитории
// коннекторов и подтягивает чистый клон. Коммиты, push, init и gh — дело владельца.
package gitrepo

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// State — состояние папки, только чтение.
type State struct {
	Repo   bool   `json:"repo"`             // есть .git
	Remote string `json:"remote,omitempty"` // url origin (или первого remote)
	Commit string `json:"commit,omitempty"` // HEAD, короткий
	// Dirty — есть незакоммиченные правки; nil — git недоступен или не репозиторий.
	Dirty *bool `json:"dirty,omitempty"`
}

var remoteURLRe = regexp.MustCompile(`(?m)^\s*url\s*=\s*(\S+)`)

// Timeout — предел на одну команду git.
var Timeout = 2 * time.Minute

// Read — состояние папки: .git/config даёт remote, `git status` — правки, `git rev-parse` — коммит.
func Read(dir string) State {
	var s State
	if st, err := os.Stat(filepath.Join(dir, ".git")); err != nil || !st.IsDir() {
		return s
	}
	s.Repo = true
	if cfg, err := os.ReadFile(filepath.Join(dir, ".git", "config")); err == nil {
		s.Remote = originURL(string(cfg))
	}
	if out, err := run(context.Background(), dir, "status", "--porcelain"); err == nil {
		dirty := len(bytes.TrimSpace(out)) > 0
		s.Dirty = &dirty
	}
	if out, err := run(context.Background(), dir, "rev-parse", "--short", "HEAD"); err == nil {
		s.Commit = strings.TrimSpace(string(out))
	}
	return s
}

// originURL — url секции [remote "origin"] (или первого remote).
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

// Source — разобранный адрес репозитория коннектора.
type Source struct {
	URL  string // адрес клонирования (https://github.com/x/gate4ai-rzd.git)
	Ref  string // тег, ветка или коммит после @; пусто — ветка по умолчанию
	Name string // имя репозитория без .git (gate4ai-rzd)
	Web  string // адрес для человека (https://github.com/x/gate4ai-rzd), если известен
}

var (
	scpLikeRe = regexp.MustCompile(`^([A-Za-z0-9_.-]+@)?([A-Za-z0-9.-]+):([^/\s][^\s]*)$`)
	hostRe    = regexp.MustCompile(`^[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)+$`)
)

// IsGit — source похож на адрес git-репозитория, а не на имя из магазина, файл
// .g4c или папку: github.com/x/y, https://github.com/x/y[.git][@ref],
// git@github.com:x/y.git, ssh://…, file://…/repo.git.
func IsGit(source string) bool {
	_, err := Parse(source)
	return err == nil
}

// Parse разбирает адрес репозитория (см. IsGit); ошибка — это не адрес git.
func Parse(source string) (Source, error) {
	s := strings.TrimSpace(source)
	if s == "" || strings.HasSuffix(strings.ToLower(s), ".g4c") || strings.ContainsAny(s, " \t\n") {
		return Source{}, errors.New("not a git source")
	}
	var src Source
	// @ref — только после последнего сегмента пути (не userinfo и не scp-like host).
	base := s
	if i := strings.LastIndex(s, "@"); i > 0 && !strings.Contains(s[i:], "/") && !strings.Contains(s[i:], ":") && strings.Contains(s[:i], "/") {
		base, src.Ref = s[:i], s[i+1:]
	}
	switch {
	case strings.Contains(base, "://"):
		u, err := url.Parse(base)
		if err != nil || u.Host == "" && u.Scheme != "file" {
			return Source{}, errors.New("not a git source")
		}
		switch u.Scheme {
		case "https", "http", "ssh", "git", "file":
		default:
			return Source{}, errors.New("not a git source")
		}
		if u.Scheme == "https" || u.Scheme == "http" {
			// адрес сайта без пути репозитория — не репозиторий
			if strings.Count(strings.Trim(u.Path, "/"), "/") < 1 || !strings.HasSuffix(u.Path, ".git") && !gitHost(u.Host) {
				return Source{}, errors.New("not a git source")
			}
			src.Web = strings.TrimSuffix(base, ".git")
		}
		src.URL = base
		src.Name = repoName(u.Path)
	case scpLikeRe.MatchString(base) && !filepath.IsAbs(base) && !strings.HasPrefix(base, ".") && !winDrive(base):
		m := scpLikeRe.FindStringSubmatch(base)
		if !hostRe.MatchString(m[2]) && m[2] != "localhost" {
			return Source{}, errors.New("not a git source")
		}
		src.URL, src.Name = base, repoName(m[3])
		src.Web = "https://" + m[2] + "/" + strings.TrimSuffix(m[3], ".git")
	default:
		// github.com/x/y[.git]
		parts := strings.Split(strings.Trim(base, "/"), "/")
		if len(parts) < 3 || !hostRe.MatchString(parts[0]) || !gitHost(parts[0]) && !strings.HasSuffix(base, ".git") {
			return Source{}, errors.New("not a git source")
		}
		if _, err := os.Stat(base); err == nil {
			return Source{}, errors.New("not a git source") // одноимённая папка — путь
		}
		src.URL = "https://" + strings.Trim(base, "/")
		src.Web = strings.TrimSuffix(src.URL, ".git")
		src.Name = repoName(base)
	}
	if src.Name == "" {
		return Source{}, errors.New("not a git source")
	}
	return src, nil
}

func winDrive(s string) bool { return len(s) >= 2 && s[1] == ':' && len(s) > 2 && (s[2] == '\\' || s[2] == '/') }

// gitHost — известные хостинги: путь без .git там тоже репозиторий.
func gitHost(host string) bool {
	h := strings.ToLower(host)
	for _, k := range []string{"github.com", "gitlab.com", "bitbucket.org", "codeberg.org", "gitflic.ru", "gitverse.ru", "sr.ht"} {
		if h == k || strings.HasSuffix(h, "."+k) {
			return true
		}
	}
	return false
}

func repoName(p string) string {
	n := path.Base(strings.TrimSuffix(strings.Trim(strings.ReplaceAll(p, "\\", "/"), "/"), ".git"))
	if n == "." || n == "/" || n == "" {
		return ""
	}
	return n
}

// Clone клонирует репозиторий в dest (его ещё нет): неглубоко, ветку или тег
// ref; коммит — полный клон и checkout.
func Clone(ctx context.Context, src Source, dest string) error {
	if _, err := exec.LookPath("git"); err != nil {
		return errors.New("git is not installed: install git or download the repository yourself and install the folder")
	}
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("%s already exists", dest)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	args := []string{"clone", "--depth", "1"}
	isCommit := regexp.MustCompile(`^[0-9a-f]{7,40}$`).MatchString(src.Ref)
	if src.Ref != "" && !isCommit {
		args = append(args, "--branch", src.Ref)
	}
	if isCommit {
		args = []string{"clone"}
	}
	args = append(args, "--", src.URL, dest)
	if _, err := runEnv(ctx, "", args...); err != nil {
		_ = os.RemoveAll(dest)
		return fmt.Errorf("git clone %s: %w", src.URL, err)
	}
	if isCommit {
		if _, err := run(ctx, dest, "checkout", "--detach", src.Ref); err != nil {
			_ = os.RemoveAll(dest)
			return fmt.Errorf("git checkout %s: %w", src.Ref, err)
		}
	}
	return nil
}

// PullIfClean подтягивает remote в чистом клоне (fast-forward); грязный клон не
// трогает (pulled=false, note объясняет). Не репозиторий или без remote — ничего.
func PullIfClean(ctx context.Context, dir string) (pulled bool, note string) {
	st := Read(dir)
	if !st.Repo || st.Remote == "" {
		return false, ""
	}
	if st.Dirty == nil {
		return false, "git is not available: the clone was not updated"
	}
	if *st.Dirty {
		return false, "the folder has local changes: not pulled from " + st.Remote + ", rebuilt as is"
	}
	if _, err := run(ctx, dir, "pull", "--ff-only", "--quiet"); err != nil {
		return false, "git pull failed: " + err.Error()
	}
	return true, ""
}

func run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	return runEnv(ctx, dir, append([]string{"-C", dir}, args...)...)
}

func runEnv(ctx context.Context, dir string, args ...string) ([]byte, error) {
	git, err := exec.LookPath("git")
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, git, args...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, errors.New(msg)
	}
	return out, nil
}
