// SPDX-License-Identifier: FSL-1.1-ALv2

package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// FieldError — ошибка схемы с путём к полю и строкой файла.
type FieldError struct {
	Path string
	Line int
	Msg  string
}

func (e FieldError) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("line %d: %s: %s", e.Line, e.Path, e.Msg)
	}
	return fmt.Sprintf("%s: %s", e.Path, e.Msg)
}

// Errors — список ошибок схемы.
type Errors []FieldError

func (es Errors) Error() string {
	parts := make([]string, len(es))
	for i, e := range es {
		parts[i] = e.Error()
	}
	return strings.Join(parts, "\n")
}

// ExtraValidator — дополнительная проверка (например, компиляция CEL в policy).
// Возвращает ошибки с путями вида "profiles.assistant.rules[0].when".
type ExtraValidator func(*Config) Errors

// LoadFile читает и проверяет файл конфигурации.
func LoadFile(path string, extra ...ExtraValidator) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(b, extra...)
}

// Parse разбирает YAML, проверяет схему и заполняет значения по умолчанию.
func Parse(b []byte, extra ...ExtraValidator) (*Config, error) {
	var root yaml.Node
	dec := yaml.NewDecoder(bytes.NewReader(b))
	if err := dec.Decode(&root); err != nil {
		return nil, fmt.Errorf("yaml: %w", err)
	}
	var c Config
	dec = yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("schema: %w", err)
	}
	errs := c.Validate()
	for _, v := range extra {
		errs = append(errs, v(&c)...)
	}
	if len(errs) > 0 {
		for i := range errs {
			errs[i].Line = lineOf(&root, errs[i].Path)
		}
		return nil, errs
	}
	c.Defaults()
	return &c, nil
}

// Marshal сериализует конфигурацию в YAML.
func Marshal(c *Config) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(c); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

var (
	idRe     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	actionRe = regexp.MustCompile(`^([a-z0-9_]+|\*)(\.([a-z0-9_]+|\*))?$`)
)

// Validate проверяет схему без CEL.
func (c *Config) Validate() Errors {
	var es Errors
	add := func(path, format string, a ...any) {
		es = append(es, FieldError{Path: path, Msg: fmt.Sprintf(format, a...)})
	}
	if len(c.Profiles) == 0 {
		add("profiles", "at least one profile is required")
	}
	for _, name := range sortedKeys(c.Agents) {
		a := c.Agents[name]
		p := "agents." + name
		if !idRe.MatchString(name) {
			add(p, "agent name must match %s", idRe)
		}
		if a.Profile == "" {
			add(p+".profile", "profile is required")
		} else if _, ok := c.Profiles[a.Profile]; !ok {
			add(p+".profile", "unknown profile %q", a.Profile)
		}
	}
	for _, name := range sortedKeys(c.Lists) {
		if !regexp.MustCompile(`^[a-z_][a-z0-9_]*$`).MatchString(name) {
			add("lists."+name, "list name must be an identifier (a-z, 0-9, _)")
		}
	}
	for _, name := range sortedKeys(c.Profiles) {
		pr := c.Profiles[name]
		p := "profiles." + name
		if !idRe.MatchString(name) {
			add(p, "profile name must match %s", idRe)
		}
		seen := map[string]bool{}
		for _, conn := range pr.Connectors {
			if !regexp.MustCompile(`^[a-z0-9_]+$`).MatchString(conn) {
				add(p+".connectors", "invalid connector name %q", conn)
			}
		}
		for i, r := range pr.Rules {
			rp := fmt.Sprintf("%s.rules[%d]", p, i)
			switch {
			case r.ID == "":
				add(rp+".id", "rule id is required")
			case !idRe.MatchString(r.ID):
				add(rp+".id", "rule id must match %s", idRe)
			case seen[r.ID]:
				add(rp+".id", "duplicate rule id %q", r.ID)
			}
			seen[r.ID] = true
			if r.Action == "" {
				add(rp+".action", "action is required (e.g. email.send or payments.*)")
			} else if !actionRe.MatchString(r.Action) {
				add(rp+".action", "invalid action %q (expected connector.action, connector.* or *)", r.Action)
			}
			switch r.Effect {
			case EffectAllow, EffectDeny, EffectAsk:
			case "":
				add(rp+".effect", "effect is required (allow | deny | ask)")
			default:
				add(rp+".effect", "unknown effect %q (allow | deny | ask)", r.Effect)
			}
			for j, o := range r.Obligations {
				if !KnownObligations[o] {
					add(fmt.Sprintf("%s.obligations[%d]", rp, j), "unknown obligation %q", o)
				}
			}
		}
		for i, l := range pr.Limits {
			lp := fmt.Sprintf("%s.limits[%d]", p, i)
			if l.ID == "" || !idRe.MatchString(l.ID) {
				add(lp+".id", "limit id is required and must match %s", idRe)
			} else if seen[l.ID] {
				add(lp+".id", "duplicate id %q (rules and limits share ids)", l.ID)
			}
			seen[l.ID] = true
			if !actionRe.MatchString(l.Action) {
				add(lp+".action", "invalid action %q", l.Action)
			}
			if l.PerDay <= 0 && l.AmountPerDay <= 0 {
				add(lp, "set per_day or amount_per_day")
			}
		}
	}
	for i, ch := range c.Approvals.Channels {
		switch ch {
		case "desktop", "telegram", "webui", "webpush":
		default:
			add(fmt.Sprintf("approvals.channels[%d]", i), "unknown channel %q (desktop | telegram | webui | webpush)", ch)
		}
	}
	if t := c.Approvals.Telegram; t != nil {
		if !strings.HasPrefix(t.Token, "@") {
			add("approvals.telegram.token", "token must be a vault alias like @telegram.bot, not the secret itself")
		}
		if t.OwnerID == 0 {
			add("approvals.telegram.owner_id", "owner_id is required")
		}
	}
	switch c.Approvals.OnTimeout {
	case "", EffectDeny:
	default:
		add("approvals.on_timeout", "only deny is supported on timeout")
	}
	if c.Approvals.Timeout < 0 {
		add("approvals.timeout", "must be positive")
	}
	for i, s := range c.Egress.Secrets {
		p := fmt.Sprintf("egress.secrets[%d]", i)
		if s.Domain == "" || s.Header == "" || s.Placeholder == "" {
			add(p, "domain, header and placeholder are required")
		}
		if !strings.HasPrefix(s.Secret, "@") {
			add(p+".secret", "secret must be a vault alias like @anthropic.key")
		}
	}
	switch c.Browser.Engine {
	case "", "stagehand", "cdp":
	default:
		add("browser.engine", "must be stagehand or cdp")
	}
	if c.Browser.ModelKey != "" && !strings.HasPrefix(c.Browser.ModelKey, "@") {
		add("browser.model_key", "must be a vault alias like @llm.key")
	}
	for i, k := range append(append([]string{}, c.Store.RegistryKeys...), c.Store.TrustedKeys...) {
		if !strings.HasPrefix(k, "ed25519:") {
			add(fmt.Sprintf("store.keys[%d]", i), "keys must look like ed25519:<base64>")
		}
	}
	if c.Store.Registry != "" && len(c.Store.RegistryKeys) == 0 {
		add("store.registry_keys", "a registry needs the keys its index is signed with")
	}
	return es
}

func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// lineOf ищет строку YAML по пути "a.b[2].c"; возвращает строку ближайшего найденного узла.
func lineOf(root *yaml.Node, path string) int {
	n := root
	if n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		n = n.Content[0]
	}
	line := n.Line
	for _, seg := range splitPath(path) {
		var next *yaml.Node
		if idx, err := strconv.Atoi(seg); err == nil && n.Kind == yaml.SequenceNode {
			if idx < len(n.Content) {
				next = n.Content[idx]
				line = next.Line
			}
		} else if n.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(n.Content); i += 2 {
				if n.Content[i].Value == seg {
					line = n.Content[i].Line
					next = n.Content[i+1]
					break
				}
			}
		}
		if next == nil {
			return line
		}
		n = next
	}
	return line
}

func splitPath(p string) []string {
	var out []string
	for _, part := range strings.Split(p, ".") {
		for part != "" {
			i := strings.IndexByte(part, '[')
			if i < 0 {
				out = append(out, part)
				break
			}
			if i > 0 {
				out = append(out, part[:i])
			}
			j := strings.IndexByte(part, ']')
			if j < i {
				break
			}
			out = append(out, part[i+1:j])
			part = part[j+1:]
		}
	}
	return out
}

// IsSchemaError — является ли err ошибкой схемы.
func IsSchemaError(err error) bool {
	var es Errors
	return errors.As(err, &es)
}
