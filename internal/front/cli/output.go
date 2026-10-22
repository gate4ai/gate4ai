// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
)

// Вывод для человека (спецификация, «Машиночитаемый вывод»): если stdout —
// терминал, списки и состояние печатаются таблицей, ошибки — текстом с
// подсказкой. --json или GATE4AI_OUTPUT=json возвращают JSON; агенты и скрипты
// (stdout — pipe или файл) всегда получают JSON. GATE4AI_OUTPUT=table — таблица
// и вне терминала.

// tableCommands — команды владельца с табличным выводом; --json у них снимается в Run.
var tableCommands = map[string]bool{"agents": true, "approvals": true, "connectors": true, "vault": true,
	"policy": true, "status": true, "daemon": true, "service": true, "relay": true}

// human — вывод для человека, а не JSON.
func (e *Env) human() bool {
	if e.ForceTable {
		return true
	}
	if e.ForceJSON {
		return false
	}
	switch strings.ToLower(e.Getenv("GATE4AI_OUTPUT")) {
	case "json":
		return false
	case "table", "text":
		return true
	}
	return e.StdoutTTY
}

// stripJSONFlag снимает --json с аргументов табличной команды.
func stripJSONFlag(args []string) ([]string, bool) {
	var out []string
	found := false
	for i, a := range args {
		if a == "--" {
			return append(out, args[i:]...), found
		}
		if a == "--json" || a == "-json" {
			found = true
			continue
		}
		out = append(out, a)
	}
	return out, found
}

// out печатает v: человеку — table (nil — ничего, сообщения уже в stderr), иначе JSON.
func (e *Env) out(v any, table func(w io.Writer)) {
	if !e.human() {
		e.printJSON(v)
		return
	}
	if table != nil {
		tw := tabwriter.NewWriter(e.Stdout, 0, 4, 2, ' ', 0)
		table(tw)
		tw.Flush()
	}
}

// as перекладывает JSON-ответ в структуру для таблицы.
func as[T any](v any) T {
	var t T
	b, _ := json.Marshal(v)
	_ = json.Unmarshal(b, &t)
	return t
}

func agentsTable(v any) func(io.Writer) {
	return func(w io.Writer) {
		r := as[struct {
			Agents map[string]struct {
				Profile     string `json:"profile"`
				Description string `json:"description"`
			} `json:"agents"`
			Tokens []struct {
				Agent   string `json:"agent"`
				Revoked bool   `json:"revoked"`
			} `json:"tokens"`
		}](v)
		active := map[string]int{}
		for _, t := range r.Tokens {
			if !t.Revoked {
				active[t.Agent]++
			}
		}
		if len(r.Agents) == 0 {
			return
		}
		fmt.Fprintln(w, "AGENT\tPROFILE\tTOKENS\tDESCRIPTION")
		for _, n := range slices.Sorted(maps.Keys(r.Agents)) {
			a := r.Agents[n]
			fmt.Fprintf(w, "%s\t%s\t%d\t%s\n", n, a.Profile, active[n], a.Description)
		}
	}
}

func connectorsTable(v any) func(io.Writer) {
	return func(w io.Writer) {
		r := as[struct {
			Connectors []struct {
				Connector string `json:"connector"`
				Version   string `json:"version"`
				Trust     string `json:"trust"`
				Publisher string `json:"publisher"`
				Loaded    bool   `json:"loaded"`
			} `json:"connectors"`
		}](v)
		if len(r.Connectors) == 0 {
			return
		}
		fmt.Fprintln(w, "CONNECTOR\tVERSION\tTRUST\tPUBLISHER\tLOADED")
		for _, c := range r.Connectors {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", c.Connector, c.Version, c.Trust, c.Publisher, yesNo(c.Loaded))
		}
	}
}

func approvalsTable(v []map[string]any) func(io.Writer) {
	return func(w io.Writer) {
		if len(v) == 0 {
			return
		}
		fmt.Fprintln(w, "ID\tAGENT\tACTION\tWHAT\tWHY")
		for _, a := range v {
			d, _ := a["decision"].(map[string]any)
			fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\n", a["id"], a["agent"], a["action"], oneLine(a["preview"]), oneLine(d["reason"]))
		}
		fmt.Fprintln(w, "\nDecide: gate4ai approvals approve <id> | reject <id>")
	}
}

func versionsTable(v any) func(io.Writer) {
	return func(w io.Writer) {
		r := as[[]struct {
			N       int    `json:"n"`
			At      string `json:"at"`
			Author  string `json:"author"`
			Comment string `json:"comment"`
		}](v)
		fmt.Fprintln(w, "VERSION\tAT\tAUTHOR\tCOMMENT")
		for _, x := range r {
			at := x.At
			if len(at) >= 19 {
				at = strings.Replace(at[:19], "T", " ", 1)
			}
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\n", x.N, at, x.Author, x.Comment)
		}
	}
}

// kvTable — ключ: значение (состояние daemon, relay, автозапуска).
func kvTable(v map[string]any, skip ...string) func(io.Writer) {
	return func(w io.Writer) {
		keys := make([]string, 0, len(v))
		for k := range v {
			if k == "status" || slices.Contains(skip, k) {
				continue
			}
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(w, "%s:\t%s\n", strings.ReplaceAll(k, "_", " "), plain(v[k]))
		}
	}
}

// plain — значение для таблицы: булевы как yes/no, списки и объекты одной строкой.
func plain(v any) string {
	switch x := v.(type) {
	case bool:
		return yesNo(x)
	case nil:
		return "-"
	case []any:
		var s []string
		for _, e := range x {
			s = append(s, plain(e))
		}
		return strings.Join(s, ", ")
	case []string:
		return strings.Join(x, ", ")
	case map[string]any:
		var s []string
		for _, k := range slices.Sorted(maps.Keys(x)) {
			s = append(s, k+" "+plain(x[k]))
		}
		return strings.Join(s, ", ")
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return fmt.Sprint(v)
}

func oneLine(v any) string {
	if v == nil {
		return ""
	}
	s := strings.Join(strings.Fields(fmt.Sprint(v)), " ")
	if len(s) > 60 {
		s = s[:57] + "..."
	}
	return s
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
