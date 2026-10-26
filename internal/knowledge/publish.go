// SPDX-License-Identifier: AGPL-3.0-only

package knowledge

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Publish — подсказка владельцу, как опубликовать папку на GitHub руками:
// с нуля (папка без git) или через форк (клон чужого репозитория). gate4ai
// эти команды не выполняет.
type Publish struct {
	Kind     string   `json:"kind"` // scratch | push | fork
	Commands []string `json:"commands"`
	Note     string   `json:"note,omitempty"`
}

// HowToPublish — команды для состояния папки. user — имя на GitHub (пусто — <you>).
func HowToPublish(f Folder, user string) Publish {
	if user == "" {
		user = "<you>"
	}
	repo := f.Name
	if !strings.HasPrefix(repo, RepoPrefix) {
		repo = RepoPrefix + repo
	}
	cd := "cd " + shellPath(f.Path)
	switch f.Git.State() {
	case "files":
		return Publish{Kind: "scratch", Commands: []string{
			"gate4ai knowledge check " + f.Name,
			cd,
			"git init && git add . && git commit -m \"" + f.Connector + " connector for gate4ai\"",
			fmt.Sprintf("gh repo create %s/%s --public --source . --push", user, repo),
			fmt.Sprintf("gh repo edit %s/%s --add-topic %s", user, repo, Topic),
		}, Note: "Next time — one command instead of a browser session, for you and for anyone who finds the repository."}
	case "git":
		return Publish{Kind: "push", Commands: []string{
			"gate4ai knowledge check " + f.Name,
			cd,
			"git add . && git commit -m \"update\"",
			fmt.Sprintf("gh repo create %s/%s --public --source . --push", user, repo),
			fmt.Sprintf("gh repo edit %s/%s --add-topic %s", user, repo, Topic),
		}}
	case "unpublished_changes":
		if isFork(f.Git.Remote, user) {
			return Publish{Kind: "fork", Commands: []string{
				"gate4ai knowledge check " + f.Name,
				cd,
				"gh repo fork --remote",
				"git checkout -b fix && git add . && git commit -m \"fix: …\"",
				"git push -u origin fix",
				"gh pr create --fill",
			}, Note: "The repository belongs to someone else: a fork and a pull request send the fix to its author."}
		}
		return Publish{Kind: "push", Commands: []string{
			"gate4ai knowledge check " + f.Name,
			cd,
			"git add . && git commit -m \"update\" && git push",
		}}
	}
	return Publish{Kind: "published", Commands: nil, Note: "Published: " + f.Git.Remote}
}

// isFork — remote указывает не на репозиторий этого пользователя (или пользователь неизвестен).
func isFork(remote, user string) bool {
	if remote == "" {
		return false
	}
	if user == "<you>" || user == "" {
		return true
	}
	r := strings.ToLower(remote)
	return !strings.Contains(r, "/"+strings.ToLower(user)+"/") && !strings.Contains(r, ":"+strings.ToLower(user)+"/")
}

func shellPath(p string) string {
	if strings.ContainsAny(p, " \t'\"$") {
		return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
	}
	return filepath.ToSlash(p)
}
