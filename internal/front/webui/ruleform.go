// SPDX-License-Identifier: AGPL-3.0-only

package webui

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Форма правила «одной фразой»: «Когда агент вызывает [действие], [решение]
// [условие ×] [+ условие]». Условия — чипы; добавление и удаление условия —
// отправка формы без сохранения (add_cond, drop), поэтому форма работает и без JS.

// RuleChip — условие правила в фразе.
type RuleChip struct {
	Text string
	Code bool   // текст — выражение CEL (человеческого описания нет)
	Drop string // значение кнопки «×»: w<i> — часть when, c<i> — блок условия
}

// condText — блок условия человеческим языком: «path внутри папки ~/notes».
func condText(c Cond) string {
	yes := map[string]string{"in_list": "из списка", "eq": "равно", "contains": "содержит", "in_folder": "внутри папки", "domain": "— сайт из",
		"lte": "не больше", "gte": "не меньше", "untrusted": "взят из входящего письма или страницы"}
	no := map[string]string{"in_list": "не из списка", "eq": "не равно", "contains": "не содержит", "in_folder": "вне папки", "domain": "— сайт не из",
		"lte": "больше", "gte": "меньше", "untrusted": "не из входящего письма или страницы"}
	op := yes[c.Kind]
	if c.Not {
		op = no[c.Kind]
	}
	return strings.TrimSpace(c.Arg + " " + or(op, c.Kind) + " " + strings.TrimSpace(c.Value))
}

// splitAnd делит выражение CEL по «&&» верхнего уровня (вне скобок и строк).
func splitAnd(when string) []string {
	var out []string
	depth, start := 0, 0
	var quote byte
	for i := 0; i < len(when); i++ {
		ch := when[i]
		switch {
		case quote != 0:
			if ch == '\\' {
				i++
			} else if ch == quote {
				quote = 0
			}
		case ch == '"' || ch == '\'':
			quote = ch
		case ch == '(' || ch == '[' || ch == '{':
			depth++
		case ch == ')' || ch == ']' || ch == '}':
			depth--
		case ch == '&' && depth == 0 && i+1 < len(when) && when[i+1] == '&':
			out = append(out, strings.TrimSpace(when[start:i]))
			start = i + 2
			i++
		}
	}
	if s := strings.TrimSpace(when[start:]); s != "" {
		out = append(out, s)
	}
	return out
}

// joinAnd собирает части условия обратно; части с «||» — в скобках.
func joinAnd(parts []string) string {
	var out []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		if strings.Contains(p, "||") && !(strings.HasPrefix(p, "(") && strings.HasSuffix(p, ")")) {
			p = "(" + p + ")"
		}
		out = append(out, p)
	}
	return strings.Join(out, " && ")
}

// parseRuleForm — форма правила из POST: части when (уже заданные условия CEL) и блоки.
func parseRuleForm(f url.Values) ruleForm {
	form := ruleForm{Profile: f.Get("profile"), ID: strings.TrimSpace(f.Get("id")), Action: f.Get("action"), Effect: f.Get("effect"),
		Reason: strings.TrimSpace(f.Get("reason"))}
	for _, p := range f["when_part"] {
		if strings.TrimSpace(p) != "" {
			form.WhenParts = append(form.WhenParts, p)
		}
	}
	kinds, args, vals := f["cond_kind"], f["cond_arg"], f["cond_value"]
	for i := range kinds {
		if kinds[i] == "" {
			continue
		}
		c := Cond{Kind: kinds[i]}
		if i < len(args) {
			c.Arg = args[i]
		}
		if i < len(vals) {
			c.Value = vals[i]
		}
		c.Not = f.Get(fmt.Sprintf("cond_not_%d", i)) == "1"
		c.Items = f.Get(fmt.Sprintf("list_items_%d", i))
		form.Conds = append(form.Conds, c)
	}
	return form
}

// editRuleForm — «+ условие» или «×»: меняет условия формы без сохранения.
// Ошибка — новое условие не годится (страница показывает её, условие не теряется).
func editRuleForm(form *ruleForm, f url.Values, args []argInfo) error {
	if d := f.Get("drop"); d != "" {
		i, err := strconv.Atoi(d[1:])
		switch {
		case err != nil:
		case d[0] == 'w' && i < len(form.WhenParts):
			form.WhenParts = append(form.WhenParts[:i], form.WhenParts[i+1:]...)
		case d[0] == 'c' && i < len(form.Conds):
			form.Conds = append(form.Conds[:i], form.Conds[i+1:]...)
		}
		return nil
	}
	if raw := strings.TrimSpace(f.Get("when")); raw != "" {
		form.WhenParts = append(form.WhenParts, splitAnd(raw)...)
	}
	if f.Get("new_kind") == "" {
		return nil
	}
	c := Cond{Kind: f.Get("new_kind"), Arg: f.Get("new_arg"), Value: f.Get("new_value"), Not: f.Get("new_not") == "1", Items: f.Get("new_items")}
	arr := false
	for _, a := range args {
		if a.Name == c.Arg {
			arr = a.Array
		}
	}
	if _, err := c.CEL(arr); err != nil {
		form.New = c
		return err
	}
	form.Conds = append(form.Conds, c)
	return nil
}

// ruleChips — условия формы чипами.
func ruleChips(form ruleForm) []RuleChip {
	var out []RuleChip
	for i, p := range form.WhenParts {
		text, ok := DescribeWhen(p)
		if !ok {
			text = p
		}
		out = append(out, RuleChip{Text: text, Code: !ok, Drop: fmt.Sprintf("w%d", i)})
	}
	for i, c := range form.Conds {
		out = append(out, RuleChip{Text: condText(c), Drop: fmt.Sprintf("c%d", i)})
	}
	return out
}
