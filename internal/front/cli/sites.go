// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
)

const sitesUsage = `usage:
  gate4ai sites                          sites agents may open in the browser (connectors.browser.allowed_domains)
  gate4ai sites allow <site>...          add sites, e.g. login.mos.ru or *.example.org
  gate4ai sites remove <site>...         remove sites`

// sites — сайты агентов в браузере без правки YAML (команда владельца).
func (e *Env) sites(ctx context.Context, args []string) int {
	sub := "list"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	c := e.Client()
	var res struct {
		Allowed []string `json:"allowed_domains"`
		Blocked []string `json:"blocked"`
		Mode    any      `json:"mode"`
		Version int      `json:"version"`
	}
	switch sub {
	case "list":
		if len(args) != 0 {
			break
		}
		if err := c.Do(ctx, "GET", "/v1/sites", nil, &res); err != nil {
			return e.fail(err)
		}
		if len(res.Allowed) == 0 {
			fmt.Fprintln(e.Stderr, "gate4ai: agents may open no sites yet: gate4ai sites allow <site>")
		}
		e.out(res, func(w io.Writer) {
			for _, s := range res.Allowed {
				fmt.Fprintln(w, s)
			}
		})
		return ExitOK
	case "allow", "add", "remove", "rm":
		if len(args) == 0 {
			break
		}
		var body map[string]any
		sites := make([]string, len(args))
		for i, a := range args {
			sites[i] = strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(a, "https://"), "http://"), "/"))
		}
		if sub == "allow" || sub == "add" {
			body = map[string]any{"add": sites}
		} else {
			body = map[string]any{"remove": sites}
		}
		if err := c.Do(ctx, "POST", "/v1/sites", body, &res); err != nil {
			return e.fail(err)
		}
		e.say(fmt.Sprintf("Sites of agents (policy version %d): %s", res.Version, strings.Join(res.Allowed, ", ")))
		e.out(map[string]any{"status": "ok", "version": res.Version, "allowed_domains": res.Allowed}, nil)
		return ExitOK
	}
	fmt.Fprintln(e.Stderr, sitesUsage)
	return ExitUsage
}
