// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/gate4ai/gate4ai/internal/knowledge"
)

const knowledgeUsage = `usage:
  gate4ai knowledge [list]            the knowledge folder: a folder per connector (recipe, code, fixtures), its state
                                      (files only · git · published · unpublished changes) and how to publish it yourself
  gate4ai knowledge check <name>      check a folder before publishing: personal data, secrets, manifest, recipe, LICENSE
  gate4ai knowledge note <domain> --text "…"
                                      a note in your words into the site's recipe (agents too: the same as
                                      gate4ai gate4ai knowledge_note --domain … --text …) — what the task did, what tripped you
  gate4ai sites lookup <domain>       what is known about a site (the same as: gate4ai gate4ai sites_lookup --domain …)

The recipe of a site (RECIPE.md) is written by gate4ai itself when a browser session on it is closed.
gate4ai writes plain files into the folder and clones public repositories (connectors install <git url>);
git init, commits, push and gh are yours — the commands are printed here and shown in Web UI → Access → Knowledge.`

// knowledgeCmd — команды владельца: папка знаний и проверка перед публикацией.
func (e *Env) knowledgeCmd(ctx context.Context, args []string) int {
	sub := "list"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	if sub == "note" {
		return e.knowledgeNote(ctx, args)
	}
	c := e.Client()
	switch sub {
	case "list":
		var res struct {
			Dir     string `json:"dir"`
			Folders []struct {
				Name      string            `json:"name"`
				Path      string            `json:"path"`
				Connector string            `json:"connector"`
				Sites     []string          `json:"sites"`
				State     string            `json:"state"`
				Installed bool              `json:"installed"`
				Recipe    *knowledge.Recipe `json:"recipe"`
				Publish   knowledge.Publish `json:"publish"`
				Problems  []string          `json:"problems"`
			} `json:"folders"`
		}
		var raw any
		if err := c.Do(ctx, "GET", "/v1/knowledge", nil, &raw); err != nil {
			return e.fail(err)
		}
		res = as[struct {
			Dir     string `json:"dir"`
			Folders []struct {
				Name      string            `json:"name"`
				Path      string            `json:"path"`
				Connector string            `json:"connector"`
				Sites     []string          `json:"sites"`
				State     string            `json:"state"`
				Installed bool              `json:"installed"`
				Recipe    *knowledge.Recipe `json:"recipe"`
				Publish   knowledge.Publish `json:"publish"`
				Problems  []string          `json:"problems"`
			} `json:"folders"`
		}](raw)
		if len(res.Folders) == 0 {
			fmt.Fprintf(e.Stderr, "gate4ai: the knowledge folder %s is empty: it fills up by itself when agents close browser sessions on sites; a connector: gate4ai connectors new <name> --site <domain> [--from-session <steps_file> --task <name>]\n", res.Dir)
		}
		e.out(raw, func(w io.Writer) {
			fmt.Fprintf(w, "knowledge folder: %s\n", res.Dir)
			if len(res.Folders) == 0 {
				return
			}
			fmt.Fprintln(w, "FOLDER\tCONNECTOR\tSITES\tTASKS\tSTATE\tINSTALLED")
			for _, f := range res.Folders {
				var tasks []string
				if f.Recipe != nil {
					for _, t := range f.Recipe.Tasks {
						tasks = append(tasks, fmt.Sprintf("%s (%s, %d)", t.ID, t.Status, t.VerifiedRuns))
					}
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", f.Name, f.Connector, strings.Join(f.Sites, ","), strings.Join(tasks, "; "), stateWord(f.State), yesNo(f.Installed))
			}
			for _, f := range res.Folders {
				for _, p := range f.Problems {
					fmt.Fprintf(w, "\n%s: %s\n", f.Name, p)
				}
			}
			fmt.Fprintln(w, "\nhow to publish (yourself): gate4ai knowledge check <name>, then the commands it prints")
		})
		return ExitOK
	case "check":
		if len(args) != 1 {
			fmt.Fprintln(e.Stderr, knowledgeUsage)
			return ExitUsage
		}
		var res struct {
			Report  knowledge.Report  `json:"report"`
			Publish knowledge.Publish `json:"publish"`
		}
		var raw any
		if err := c.Do(ctx, "POST", "/v1/knowledge/check", map[string]any{"name": args[0]}, &raw); err != nil {
			return e.fail(err)
		}
		res = as[struct {
			Report  knowledge.Report  `json:"report"`
			Publish knowledge.Publish `json:"publish"`
		}](raw)
		e.out(raw, func(w io.Writer) {
			if res.Report.OK {
				fmt.Fprintf(w, "ready to publish: %d files checked, no personal data or secrets found\n", res.Report.Files)
			} else {
				fmt.Fprintln(w, "not ready:")
			}
			for _, x := range res.Report.Errors {
				fmt.Fprintf(w, "  error: %s\n", x)
			}
			for _, x := range res.Report.Warnings {
				fmt.Fprintf(w, "  look: %s\n", x)
			}
			if len(res.Publish.Commands) > 0 {
				fmt.Fprintf(w, "\nto publish (%s), run yourself:\n", res.Publish.Kind)
				for _, cmd := range res.Publish.Commands {
					fmt.Fprintf(w, "  %s\n", cmd)
				}
			} else if res.Publish.Note != "" {
				fmt.Fprintln(w, "\n"+res.Publish.Note)
			}
		})
		if !res.Report.OK {
			return ExitError
		}
		return ExitOK
	}
	fmt.Fprintln(e.Stderr, knowledgeUsage)
	return ExitUsage
}

// knowledgeNote — заметка в рецепт сайта: агент — действие gate4ai.knowledge_note
// под своим профилем; владелец — POST /v1/knowledge/note.
func (e *Env) knowledgeNote(ctx context.Context, args []string) int {
	pos, fl := splitFlags(args, map[string]bool{"text": true})
	fs := flag.NewFlagSet("note", flag.ContinueOnError)
	fs.SetOutput(e.Stderr)
	text := fs.String("text", "", "the note")
	if fs.Parse(fl) != nil || len(pos) != 1 || strings.TrimSpace(*text) == "" {
		fmt.Fprintln(e.Stderr, knowledgeUsage)
		return ExitUsage
	}
	if e.AgentMode() {
		e.ownerCmd = false
		return e.action(ctx, "gate4ai", []string{"knowledge_note", "--domain", pos[0], "--text", *text})
	}
	return e.call(ctx, e.Client(), "POST", "/v1/knowledge/note", map[string]any{"domain": pos[0], "text": *text})
}

func stateWord(s string) string {
	switch s {
	case "files":
		return "files only"
	case "git":
		return "git, not published"
	case "unpublished_changes":
		return "unpublished changes"
	}
	return s
}
