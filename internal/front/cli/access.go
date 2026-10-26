// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strings"

	"github.com/gate4ai/gate4ai/sdk-go/access"
)

const accessUsage = `usage:
  gate4ai access                                    the Browser and Files access lists
  gate4ai access sites                              sites and their access
  gate4ai access sites set <site> <access>          allow | read | connector | ask | deny; site: app.example.org, *.example.org (also the apex), *
  gate4ai access sites remove <site>
  gate4ai access folders                            folders and their access
  gate4ai access folders set <folder> <access>      allow | append | read | connector | ask | deny; folder: ~/notes, /data, ~/notes/**/*.md
  gate4ai access folders remove <folder>
  gate4ai sites allow <site>...                     the same as: access sites set <site> read
  gate4ai sites lookup <domain>                     what is known about a site: connectors, store, knowledge folder, sign-in, next step`

// accessCmd — списки доступа Браузера и Файлов без правки YAML (команда владельца).
func (e *Env) accessCmd(ctx context.Context, args []string) int {
	c := e.Client()
	var res struct {
		Sites   []access.Site   `json:"sites"`
		Folders []access.Folder `json:"folders"`
		Version int             `json:"version"`
	}
	show := func(kind string) int {
		if err := c.Do(ctx, "GET", "/v1/access", nil, &res); err != nil {
			return e.fail(err)
		}
		out := map[string]any{}
		if kind != "folders" {
			out["sites"] = res.Sites
		}
		if kind != "sites" {
			out["folders"] = res.Folders
		}
		e.out(out, func(w io.Writer) {
			if kind != "folders" {
				fmt.Fprintln(w, "Browser — sites:")
				for _, s := range res.Sites {
					fmt.Fprintf(w, "  %-40s %s\n", s.Domain, s.Access)
				}
			}
			if kind != "sites" {
				fmt.Fprintln(w, "Files — folders:")
				for _, f := range res.Folders {
					fmt.Fprintf(w, "  %-40s %s\n", f.Path, f.Access)
				}
			}
		})
		return ExitOK
	}
	if len(args) == 0 {
		return show("")
	}
	kind, rest := args[0], args[1:]
	if kind != "sites" && kind != "folders" {
		fmt.Fprintln(e.Stderr, accessUsage)
		return ExitUsage
	}
	if len(rest) == 0 {
		return show(kind)
	}
	var entries []map[string]any
	switch {
	case rest[0] == "set" && len(rest) == 3:
		mode := rest[2]
		modes := access.SiteModes
		if kind == "folders" {
			modes = access.FolderModes
		}
		if !slices.Contains(modes, mode) {
			fmt.Fprintf(e.Stderr, "gate4ai: access must be one of %s\n", strings.Join(modes, ", "))
			return ExitUsage
		}
		entries = append(entries, entry(kind, rest[1], mode))
	case (rest[0] == "remove" || rest[0] == "rm") && len(rest) >= 2:
		for _, v := range rest[1:] {
			entries = append(entries, entry(kind, v, ""))
		}
	default:
		fmt.Fprintln(e.Stderr, accessUsage)
		return ExitUsage
	}
	if err := c.Do(ctx, "POST", "/v1/access", map[string]any{kind: entries}, &res); err != nil {
		return e.fail(err)
	}
	e.say(fmt.Sprintf("Access lists updated (policy version %d)", res.Version))
	e.out(map[string]any{"status": "ok", "version": res.Version, "sites": res.Sites, "folders": res.Folders}, nil)
	return ExitOK
}

func entry(kind, value, mode string) map[string]any {
	if kind == "sites" {
		value = strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(value, "https://"), "http://"), "/"))
		return map[string]any{"domain": value, "access": mode}
	}
	return map[string]any{"path": value, "access": mode}
}

// sites — прежняя команда: gate4ai sites allow <site>… — чтение без вопроса.
func (e *Env) sites(ctx context.Context, args []string) int {
	if len(args) == 0 {
		return e.accessCmd(ctx, []string{"sites"})
	}
	switch args[0] {
	case "allow", "add":
		code := ExitOK
		for _, s := range args[1:] {
			if code = e.accessCmd(ctx, []string{"sites", "set", s, access.Read}); code != ExitOK {
				return code
			}
		}
		return code
	case "remove", "rm":
		return e.accessCmd(ctx, append([]string{"sites", "remove"}, args[1:]...))
	case "lookup":
		if len(args) != 2 {
			fmt.Fprintln(e.Stderr, accessUsage)
			return ExitUsage
		}
		// Агент — действие встроенного коннектора gate4ai под своим профилем;
		// владелец — тот же ответ без фильтра профиля.
		if e.AgentMode() {
			e.ownerCmd = false
			return e.action(ctx, "gate4ai", []string{"sites_lookup", "--domain", args[1]})
		}
		return e.call(ctx, e.Client(), "GET", "/v1/sites/lookup?domain="+url.QueryEscape(args[1]), nil)
	}
	fmt.Fprintln(e.Stderr, accessUsage)
	return ExitUsage
}
