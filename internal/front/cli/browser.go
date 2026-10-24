// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/url"
	"strings"
	"text/tabwriter"
	"time"
)

// Команды владельца для браузера gate4ai: сеансы агентов (sessions, watch)
// и проверка установки (setup). Остальные `gate4ai browser <действие>` — действия
// коннектора для агентов.

const browserUsage = `usage:
  gate4ai browser sessions [--json]       browser sessions of agents: who, which page, what is going on, the link to watch
  gate4ai browser sessions close <id>     close a session of an agent
  gate4ai browser watch <id>              open the live view of a session (on a server without a screen: print its link)
  gate4ai browser setup [--yes] [--dry-run] [--json]
                                          check Chrome, the virtual screen (Xvfb) and fonts; offer the commands that install what is missing`

// browserOwnerCmds — подкоманды владельца (не действия коннектора).
var browserOwnerCmds = map[string]bool{"sessions": true, "watch": true, "setup": true}

// browserSession — сеанс из /v1/browser/sessions.
type browserSession struct {
	ID      string    `json:"id"`
	Who     string    `json:"who"`
	URL     string    `json:"url"`
	State   string    `json:"state"`
	Running string    `json:"running"`
	Since   time.Time `json:"since"`
	ViewURL string    `json:"view_url"`
}

type browserStatus struct {
	Sessions []browserSession `json:"sessions"`
	Display  string           `json:"display"`
	Warning  string           `json:"warning"`
	Running  bool             `json:"running"`
	Missing  bool             `json:"missing"`
}

var stateWords = map[string]string{"working": "the agent is working", "idle": "idle", "waiting": "waits for you (handoff)", "user": "under your control"}

func (e *Env) browserOwner(ctx context.Context, sub string, args []string) int {
	switch sub {
	case "setup":
		return e.browserSetup(ctx, args)
	case "sessions":
		if len(args) == 2 && args[0] == "close" {
			if err := e.Client().Do(ctx, "POST", "/v1/browser/sessions/"+url.PathEscape(args[1])+"/close", nil, nil); err != nil {
				return e.fail(err)
			}
			e.say("Closed browser session " + args[1])
			e.out(map[string]any{"status": "ok", "closed": args[1]}, nil)
			return ExitOK
		}
		var j bool
		if args, j = stripJSONFlag(args); j {
			e.ForceJSON = true
		}
		if len(args) != 0 {
			break
		}
		var st browserStatus
		if err := e.Client().Do(ctx, "GET", "/v1/browser/sessions", nil, &st); err != nil {
			return e.fail(err)
		}
		if st.Warning != "" {
			fmt.Fprintln(e.Stderr, "gate4ai: "+st.Warning)
		}
		if st.Missing {
			fmt.Fprintln(e.Stderr, "gate4ai: the browser package is not installed (gate4ai connectors install browser)")
		}
		e.out(st, func(w io.Writer) {
			if len(st.Sessions) == 0 {
				fmt.Fprintln(w, "No agent has the browser open now.")
				return
			}
			tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "SESSION\tAGENT\tSTATE\tPAGE\tWATCH")
			for _, s := range st.Sessions {
				state := stateWords[s.State]
				if s.Running != "" {
					state += " (browser." + s.Running + ")"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", s.ID, s.Who, state, s.URL, s.ViewURL)
			}
			tw.Flush()
		})
		return ExitOK
	case "watch":
		fs := flag.NewFlagSet("gate4ai browser watch", flag.ContinueOnError)
		fs.SetOutput(e.Stderr)
		remote := fs.Bool("remote", false, "a link for the public address, to open on another device")
		if fs.Parse(args) != nil || fs.NArg() != 1 {
			break
		}
		id := fs.Arg(0)
		var st browserStatus
		if err := e.Client().Do(ctx, "GET", "/v1/browser/sessions", nil, &st); err != nil {
			return e.fail(err)
		}
		var s *browserSession
		for i := range st.Sessions {
			if st.Sessions[i].ID == id {
				s = &st.Sessions[i]
			}
		}
		if s == nil {
			fmt.Fprintln(e.Stderr, "gate4ai: browser session "+id+" is closed or unknown; gate4ai browser sessions lists the open ones")
			return ExitError
		}
		// Вход по одноразовой ссылке — сразу на страницу сеанса.
		var res struct {
			URL string `json:"url"`
		}
		if err := e.Client().Do(ctx, "POST", "/v1/ui/session", map[string]any{"remote": *remote, "next": "/browser/" + id}, &res); err != nil {
			return e.fail(err)
		}
		if !*remote && !headless() {
			if err := OpenLoginURL(res.URL); err == nil {
				e.say("Opened the live view of " + id + " in the browser.")
				e.out(map[string]any{"status": "ok", "session": id, "view_url": s.ViewURL}, nil)
				return ExitOK
			}
		}
		if !*remote && headless() {
			fmt.Fprintln(e.Stderr, "gate4ai: no graphical session here: open the link on this computer, forward the port ("+sshHint(res.URL)+") or use gate4ai browser watch --remote "+id)
		}
		e.say("The session (sign-in link, valid for 2 minutes):")
		fmt.Fprintln(e.Stdout, res.URL)
		e.say("Its permanent address (after you sign in): " + s.ViewURL)
		return ExitOK
	}
	fmt.Fprintln(e.Stderr, browserUsage)
	return ExitUsage
}

// joinWords — список через запятую.
func joinWords(s []string) string { return strings.Join(s, ", ") }
