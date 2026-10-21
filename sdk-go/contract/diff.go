// SPDX-License-Identifier: Apache-2.0

// Package contract — контракт коннектора для вызовов через service_call:
// действия из manifest.services с их схемами и мажор manifest.contract.
// Diff и Check находят несовместимые изменения (их можно выпустить только с
// повышением contract), Gen генерирует типизированный Go-клиент для
// потребителя — так ошибки изменения поставщика ловит компилятор.
package contract

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/gate4ai/gate4ai/sdk-go/manifest"
)

// Change — изменение контракта.
type Change struct {
	Action   string // действие поставщика
	Path     string // input.<поле>… / output.<поле>…; "" — действие целиком
	What     string
	Breaking bool
}

func (c Change) String() string {
	s := c.Action
	if c.Path != "" {
		s += " " + c.Path
	}
	kind := "compatible"
	if c.Breaking {
		kind = "breaking"
	}
	return fmt.Sprintf("%s: %s (%s)", s, c.What, kind)
}

// Diff — изменения контракта от old к new (только действия из old.services).
func Diff(old, new manifest.Manifest) []Change {
	var out []Change
	for _, name := range old.Services {
		oa, _ := old.Find(name)
		na, ok := new.Find(name)
		if !ok || !new.IsService(name) {
			out = append(out, Change{Action: name, What: "removed from services", Breaking: true})
			continue
		}
		if oa.Class != na.Class {
			out = append(out, Change{Action: name, What: fmt.Sprintf("class %s → %s", oa.Class, na.Class), Breaking: true})
		}
		out = append(out, diffSchema(name, "input", &oa.InputSchema, &na.InputSchema, true)...)
		switch {
		case oa.OutputSchema != nil && na.OutputSchema == nil:
			out = append(out, Change{Action: name, Path: "output", What: "output_schema removed", Breaking: true})
		case oa.OutputSchema != nil:
			out = append(out, diffSchema(name, "output", oa.OutputSchema, na.OutputSchema, false)...)
		}
	}
	for _, name := range new.Services {
		if !old.IsService(name) {
			out = append(out, Change{Action: name, What: "added to services"})
		}
	}
	return out
}

// Check — new можно выпустить после old: несовместимые изменения — только с
// повышением contract; понижать contract нельзя.
func Check(old, new manifest.Manifest) error {
	if new.Contract < old.Contract {
		return fmt.Errorf("contract %d is lower than the published %d", new.Contract, old.Contract)
	}
	if new.Contract > old.Contract {
		return nil
	}
	var bad []string
	for _, c := range Diff(old, new) {
		if c.Breaking {
			bad = append(bad, c.String())
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("breaking changes need contract %d (now %d):\n  %s", old.Contract+1, new.Contract, strings.Join(bad, "\n  "))
	}
	return nil
}

// diffSchema сравнивает схемы. input — схема, которую заполняет потребитель:
// сужение (новое обязательное поле, удалённое поле, меньший диапазон) ломает
// его; output — схема, которую он читает: ломает исчезновение или смена типа.
func diffSchema(action, path string, o, n *manifest.Schema, input bool) []Change {
	var out []Change
	brk := func(what string) { out = append(out, Change{Action: action, Path: path, What: what, Breaking: true}) }
	ok := func(what string) { out = append(out, Change{Action: action, Path: path, What: what}) }
	if typeOf(o) != typeOf(n) {
		brk(fmt.Sprintf("type %s → %s", typeOf(o), typeOf(n)))
		return out
	}
	if o.Format != n.Format {
		if input && n.Format != "" || !input && o.Format != "" {
			brk(fmt.Sprintf("format %q → %q", o.Format, n.Format))
		} else {
			ok(fmt.Sprintf("format %q → %q", o.Format, n.Format))
		}
	}
	if len(o.Enum) > 0 || len(n.Enum) > 0 {
		removed, added := setDiff(o.Enum, n.Enum), setDiff(n.Enum, o.Enum)
		if len(o.Enum) == 0 {
			removed = []string{"(any value)"}
		}
		if len(n.Enum) == 0 {
			added = []string{"(any value)"}
		}
		switch {
		case input && len(removed) > 0:
			brk("enum values removed: " + strings.Join(removed, ", "))
		case !input && len(added) > 0:
			brk("enum values added: " + strings.Join(added, ", "))
		case len(removed)+len(added) > 0:
			ok("enum changed")
		}
	}
	if input {
		if tighter(o.Minimum, n.Minimum, true) || tighter(o.Maximum, n.Maximum, false) ||
			tighterInt(o.MaxLength, n.MaxLength, false) || tighterInt(o.MinItems, n.MinItems, true) || tighterInt(o.MaxItems, n.MaxItems, false) {
			brk("range narrowed")
		}
	}
	if o.Items != nil && n.Items != nil {
		out = append(out, diffSchema(action, path+"[]", o.Items, n.Items, input)...)
	}
	if typeOf(o) != manifest.TObject {
		return out
	}
	for _, p := range o.Properties {
		ns, found := n.Properties.Get(p.Name)
		sub := path + "." + p.Name
		if !found {
			out = append(out, Change{Action: action, Path: sub, What: "removed", Breaking: true})
			continue
		}
		if !input && o.IsRequired(p.Name) && !n.IsRequired(p.Name) {
			out = append(out, Change{Action: action, Path: sub, What: "no longer required", Breaking: true})
		}
		if input && !o.IsRequired(p.Name) && n.IsRequired(p.Name) {
			out = append(out, Change{Action: action, Path: sub, What: "became required", Breaking: true})
		}
		out = append(out, diffSchema(action, sub, p.Schema, ns, input)...)
	}
	for _, p := range n.Properties {
		if _, found := o.Properties.Get(p.Name); found {
			continue
		}
		sub := path + "." + p.Name
		if input && n.IsRequired(p.Name) {
			out = append(out, Change{Action: action, Path: sub, What: "added as required", Breaking: true})
		} else {
			out = append(out, Change{Action: action, Path: sub, What: "added"})
		}
	}
	return out
}

func typeOf(s *manifest.Schema) string {
	if s == nil {
		return ""
	}
	if s.Type == "" && len(s.Properties) > 0 {
		return manifest.TObject
	}
	return s.Type
}

func setDiff(a, b []string) []string {
	var out []string
	for _, x := range a {
		if !slices.Contains(b, x) {
			out = append(out, x)
		}
	}
	sort.Strings(out)
	return out
}

// tighter — граница стала строже (min выросла / max уменьшилась или появилась).
func tighter(o, n *float64, isMin bool) bool {
	switch {
	case n == nil:
		return false
	case o == nil:
		return true
	case isMin:
		return *n > *o
	}
	return *n < *o
}

func tighterInt(o, n *int, isMin bool) bool {
	var of, nf *float64
	if o != nil {
		v := float64(*o)
		of = &v
	}
	if n != nil {
		v := float64(*n)
		nf = &v
	}
	return tighter(of, nf, isMin)
}
