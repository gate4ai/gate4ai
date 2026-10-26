// SPDX-License-Identifier: AGPL-3.0-only

package webui

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/gate4ai/gate4ai/internal/knowledge"
)

// ---- Знания: папка знаний владельца ----

// knowledgeItem — папка коннектора на странице «Знания».
type knowledgeItem struct {
	knowledge.Folder
	Installed bool
	Publish   knowledge.Publish
	Tasks     []knowledge.Task
	SiteList  []string
}

func (u *UI) knowledgePage(r *http.Request) (string, map[string]any, error) {
	list, err := u.D.Knowledge()
	data := map[string]any{"Dir": u.D.KnowledgeDir(), "Topic": knowledge.Topic}
	if err != nil {
		return "knowledge", data, err
	}
	var items []knowledgeItem
	for _, f := range list {
		it := knowledgeItem{Folder: f, Publish: knowledge.HowToPublish(f, ""), SiteList: f.Sites()}
		if f.Connector != "" {
			_, it.Installed = u.D.Registry.Get(f.Connector)
		}
		if f.Recipe != nil {
			it.Tasks = f.Recipe.Tasks
		}
		items = append(items, it)
	}
	data["Items"] = items
	if name := strings.TrimSpace(r.URL.Query().Get("check")); name != "" {
		if f, ok := knowledge.Find(u.D.KnowledgeDir(), name); ok {
			data["Check"] = knowledge.Check(f.Path)
			data["CheckName"] = f.Name
		}
	}
	return "knowledge", data, nil
}

// checkKnowledge — кнопка «Проверить перед публикацией»: отчёт на той же странице.
func (u *UI) checkKnowledge(w http.ResponseWriter, r *http.Request) map[string]any {
	name := strings.TrimSpace(r.PostForm.Get("name"))
	if _, ok := knowledge.Find(u.D.KnowledgeDir(), name); !ok {
		return redirectErr("/knowledge", M("%s: no such folder in %s", name, u.D.KnowledgeDir()))
	}
	return map[string]any{"redirect": "/knowledge?check=" + url.QueryEscape(name) + "#check"}
}
