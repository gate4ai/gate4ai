// SPDX-License-Identifier: Apache-2.0

package manifest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/mail"
	"strings"

	"gopkg.in/yaml.v3"
)

// Типы схемы.
const (
	TObject  = "object"
	TString  = "string"
	TNumber  = "number"
	TInteger = "integer"
	TBoolean = "boolean"
	TArray   = "array"
)

// Форматы строк: email — адрес (приводится к нижнему регистру), url — только
// http(s), path — путь к файлу (в MCP не экспортируется).
const (
	FormatEmail = "email"
	FormatURL   = "url"
	FormatPath  = "path"
)

// Schema — подмножество JSON Schema для аргументов и результата действия (как
// inputSchema / outputSchema инструмента MCP). У объектов лишних полей не
// бывает (additionalProperties: false подразумевается), порядок полей
// сохраняется — по нему строятся флаги CLI, skill и формы Web UI.
type Schema struct {
	Type                 string     `json:"type,omitempty" yaml:"type,omitempty"`
	Description          string     `json:"description,omitempty" yaml:"description,omitempty"`
	Properties           Properties `json:"properties,omitempty" yaml:"properties,omitempty"`
	Required             []string   `json:"required,omitempty" yaml:"required,omitempty"`
	AdditionalProperties *bool      `json:"additionalProperties,omitempty" yaml:"additionalProperties,omitempty"`
	Items                *Schema    `json:"items,omitempty" yaml:"items,omitempty"`
	Enum                 []string   `json:"enum,omitempty" yaml:"enum,omitempty"`
	Format               string     `json:"format,omitempty" yaml:"format,omitempty"`
	Default              any        `json:"default,omitempty" yaml:"default,omitempty"`
	Minimum              *float64   `json:"minimum,omitempty" yaml:"minimum,omitempty"`
	Maximum              *float64   `json:"maximum,omitempty" yaml:"maximum,omitempty"`
	MaxLength            *int       `json:"maxLength,omitempty" yaml:"maxLength,omitempty"`
	MinItems             *int       `json:"minItems,omitempty" yaml:"minItems,omitempty"`
	MaxItems             *int       `json:"maxItems,omitempty" yaml:"maxItems,omitempty"`
}

// Property — поле объекта.
type Property struct {
	Name   string
	Schema *Schema
}

// Properties — поля объекта в порядке объявления.
type Properties []Property

// Get — схема поля.
func (p Properties) Get(name string) (*Schema, bool) {
	for _, x := range p {
		if x.Name == name {
			return x.Schema, true
		}
	}
	return nil, false
}

// UnmarshalYAML читает отображение с сохранением порядка; лишние ключи схемы — ошибка.
func (p *Properties) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: properties must be a mapping", n.Line)
	}
	*p = nil
	for i := 0; i+1 < len(n.Content); i += 2 {
		b, err := yaml.Marshal(n.Content[i+1])
		if err != nil {
			return err
		}
		dec := yaml.NewDecoder(bytes.NewReader(b))
		dec.KnownFields(true)
		s := &Schema{}
		if err := dec.Decode(s); err != nil {
			return fmt.Errorf("line %d: property %s: %w", n.Content[i].Line, n.Content[i].Value, err)
		}
		*p = append(*p, Property{Name: n.Content[i].Value, Schema: s})
	}
	return nil
}

// MarshalJSON пишет объект с сохранением порядка.
func (p Properties) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, x := range p {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(x.Name)
		v, err := json.Marshal(x.Schema)
		if err != nil {
			return nil, err
		}
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// UnmarshalJSON читает объект с сохранением порядка.
func (p *Properties) UnmarshalJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return fmt.Errorf("properties must be an object")
	}
	*p = nil
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return err
		}
		s := &Schema{}
		if err := dec.Decode(s); err != nil {
			return err
		}
		*p = append(*p, Property{Name: t.(string), Schema: s})
	}
	_, err := dec.Token()
	return err
}

// IsObject — схема объекта (пустая схема аргументов — объект без полей).
func (s *Schema) IsObject() bool { return s.Type == TObject || s.Type == "" }

// IsRequired — поле обязательно.
func (s *Schema) IsRequired(name string) bool {
	for _, r := range s.Required {
		if r == name {
			return true
		}
	}
	return false
}

// Names — имена полей объекта.
func (s *Schema) Names() []string {
	out := make([]string, len(s.Properties))
	for i, p := range s.Properties {
		out[i] = p.Name
	}
	return out
}

// validate проверяет саму схему.
func (s *Schema) validate(path string) error {
	at := func(f string, a ...any) error {
		if path == "" {
			return fmt.Errorf(f, a...)
		}
		return fmt.Errorf("%s: "+f, append([]any{path}, a...)...)
	}
	switch s.Type {
	case TObject:
	case TString, TNumber, TInteger, TBoolean, TArray:
		if path == "" {
			return at("schema must be an object")
		}
	case "":
		if path != "" {
			return at("type is required")
		}
	default:
		return at("bad type %q", s.Type)
	}
	if s.AdditionalProperties != nil && *s.AdditionalProperties {
		return at("additionalProperties must be false")
	}
	if !s.IsObject() && (len(s.Properties) > 0 || len(s.Required) > 0) {
		return at("properties are allowed only for objects")
	}
	if (s.Type == TArray) != (s.Items != nil) {
		return at("items are required for arrays and allowed only for them")
	}
	if len(s.Enum) > 0 && s.Type != TString {
		return at("enum is allowed only for strings")
	}
	switch s.Format {
	case "":
	case FormatEmail, FormatURL, FormatPath:
		if s.Type != TString {
			return at("format is allowed only for strings")
		}
	default:
		return at("bad format %q", s.Format)
	}
	seen := map[string]bool{}
	for _, p := range s.Properties {
		if !nameRe.MatchString(p.Name) || seen[p.Name] {
			return at("bad or duplicate property %q", p.Name)
		}
		seen[p.Name] = true
		if err := p.Schema.validate(join(path, p.Name)); err != nil {
			return err
		}
	}
	for _, r := range s.Required {
		if !seen[r] {
			return at("required property %q is not declared", r)
		}
	}
	if s.Items != nil {
		if err := s.Items.validate(path + "[]"); err != nil {
			return err
		}
	}
	return nil
}

func join(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

// Normalize проверяет значение аргументов и приводит его к схеме: неизвестные
// поля — ошибка, обязательные — должны быть, отсутствующие необязательные
// получают значение по умолчанию или нулевое значение типа (чтобы условия CEL
// не падали). Числа — float64, массивы — []any, объекты — map[string]any, строки
// без пробелов по краям, адреса email — в нижнем регистре. Строка вместо
// массива строк — массив из одного элемента.
func (s *Schema) Normalize(in map[string]any) (map[string]any, error) {
	if in == nil {
		in = map[string]any{}
	}
	v, err := s.object("", in)
	if err != nil {
		return nil, err
	}
	return v.(map[string]any), nil
}

func (s *Schema) object(path string, v any) (any, error) {
	in, ok := v.(map[string]any)
	if !ok {
		return nil, argErr("argument %q must be an object", path)
	}
	var unknown []string
	for k := range in {
		if _, ok := s.Properties.Get(k); !ok {
			unknown = append(unknown, join(path, k))
		}
	}
	if len(unknown) > 0 {
		sortStrings(unknown)
		return nil, argErr("unknown argument(s): %s", strings.Join(unknown, ", "))
	}
	out := map[string]any{}
	for _, p := range s.Properties {
		name, req := join(path, p.Name), s.IsRequired(p.Name)
		v, ok := in[p.Name]
		if !ok || v == nil {
			if req {
				return nil, argErr("missing required argument %q", name)
			}
			v = p.Schema.Default
			if v == nil {
				v = p.Schema.zero()
			}
		}
		nv, err := p.Schema.coerce(name, v)
		if err != nil {
			return nil, err
		}
		if req && (nv == "" || isEmptyList(nv)) {
			return nil, argErr("argument %q must not be empty", name)
		}
		out[p.Name] = nv
	}
	return out, nil
}

func (s *Schema) zero() any {
	switch s.Type {
	case TNumber, TInteger:
		return float64(0)
	case TBoolean:
		return false
	case TArray:
		return []any{}
	case TObject:
		return map[string]any{}
	}
	return ""
}

func isEmptyList(v any) bool {
	l, ok := v.([]any)
	return ok && len(l) == 0
}

func (s *Schema) coerce(name string, v any) (any, error) {
	switch s.Type {
	case TString:
		str, ok := v.(string)
		if !ok {
			return nil, argErr("argument %q must be a string", name)
		}
		return s.normString(name, str)
	case TNumber, TInteger:
		var f float64
		switch n := v.(type) {
		case float64:
			f = n
		case int:
			f = float64(n)
		case int64:
			f = float64(n)
		default:
			return nil, argErr("argument %q must be a number", name)
		}
		if s.Type == TInteger && f != float64(int64(f)) {
			return nil, argErr("argument %q must be an integer", name)
		}
		if (s.Minimum != nil && f < *s.Minimum) || (s.Maximum != nil && f > *s.Maximum) {
			return nil, argErr("argument %q is out of range", name)
		}
		return f, nil
	case TBoolean:
		b, ok := v.(bool)
		if !ok {
			return nil, argErr("argument %q must be a boolean", name)
		}
		return b, nil
	case TArray:
		var items []any
		switch l := v.(type) {
		case []any:
			items = l
		case []string:
			for _, x := range l {
				items = append(items, x)
			}
		case string:
			if s.Items.Type != TString {
				return nil, argErr("argument %q must be an array", name)
			}
			items = []any{l}
		default:
			return nil, argErr("argument %q must be an array", name)
		}
		if (s.MinItems != nil && len(items) < *s.MinItems) || (s.MaxItems != nil && len(items) > *s.MaxItems) {
			return nil, argErr("argument %q: wrong number of items", name)
		}
		out := make([]any, 0, len(items))
		for i, it := range items {
			nv, err := s.Items.coerce(fmt.Sprintf("%s[%d]", name, i), it)
			if err != nil {
				return nil, err
			}
			out = append(out, nv)
		}
		return out, nil
	case TObject:
		return s.object(name, v)
	}
	return nil, argErr("argument %q: unsupported type", name)
}

func (s *Schema) normString(name, str string) (any, error) {
	str = strings.TrimSpace(str)
	if s.MaxLength != nil && len([]rune(str)) > *s.MaxLength {
		return nil, argErr("argument %q is too long", name)
	}
	if len(s.Enum) > 0 && str != "" {
		ok := false
		for _, e := range s.Enum {
			ok = ok || e == str
		}
		if !ok {
			return nil, argErr("argument %q must be one of: %s", name, strings.Join(s.Enum, ", "))
		}
	}
	if str == "" {
		return str, nil
	}
	switch s.Format {
	case FormatEmail:
		addr, err := mail.ParseAddress(str)
		if err != nil {
			return nil, argErr("argument %q: invalid email address %q", name, str)
		}
		return strings.ToLower(addr.Address), nil
	case FormatURL:
		if !strings.HasPrefix(str, "https://") && !strings.HasPrefix(str, "http://") {
			return nil, argErr("argument %q: only http(s) URLs are allowed", name)
		}
	}
	return str, nil
}

// Check проверяет значение (после JSON) по схеме без приведения и значений по
// умолчанию — для результата действия (output_schema).
func (s *Schema) Check(v any) error { return s.check("result", v) }

func (s *Schema) check(path string, v any) error {
	bad := func(what string) error { return fmt.Errorf("%s must be %s", path, what) }
	switch s.Type {
	case TObject, "":
		m, ok := v.(map[string]any)
		if !ok {
			return bad("an object")
		}
		for k := range m {
			if _, ok := s.Properties.Get(k); !ok {
				return fmt.Errorf("%s: unknown field %q", path, k)
			}
		}
		for _, r := range s.Required {
			if _, ok := m[r]; !ok {
				return fmt.Errorf("%s: missing field %q", path, r)
			}
		}
		for _, p := range s.Properties {
			if x, ok := m[p.Name]; ok && x != nil {
				if err := p.Schema.check(path+"."+p.Name, x); err != nil {
					return err
				}
			}
		}
	case TString:
		str, ok := v.(string)
		if !ok {
			return bad("a string")
		}
		if len(s.Enum) > 0 {
			for _, e := range s.Enum {
				if e == str {
					return nil
				}
			}
			return bad("one of: " + strings.Join(s.Enum, ", "))
		}
	case TNumber, TInteger:
		f, ok := v.(float64)
		if !ok {
			return bad("a number")
		}
		if s.Type == TInteger && f != float64(int64(f)) {
			return bad("an integer")
		}
	case TBoolean:
		if _, ok := v.(bool); !ok {
			return bad("a boolean")
		}
	case TArray:
		l, ok := v.([]any)
		if !ok {
			return bad("an array")
		}
		for i, x := range l {
			if err := s.Items.check(fmt.Sprintf("%s[%d]", path, i), x); err != nil {
				return err
			}
		}
	}
	return nil
}

// JSONSchema — схема для MCP (inputSchema / outputSchema): объекты закрыты
// (additionalProperties: false), format url → uri, path не экспортируется.
func (s *Schema) JSONSchema() map[string]any {
	out := map[string]any{}
	t := s.Type
	if t == "" {
		t = TObject
	}
	out["type"] = t
	if s.Description != "" {
		out["description"] = s.Description
	}
	if t == TObject {
		props := map[string]any{}
		for _, p := range s.Properties {
			props[p.Name] = p.Schema.JSONSchema()
		}
		req := s.Required
		if req == nil {
			req = []string{}
		}
		out["properties"], out["required"], out["additionalProperties"] = props, req, false
	}
	if s.Items != nil {
		out["items"] = s.Items.JSONSchema()
	}
	if len(s.Enum) > 0 {
		out["enum"] = s.Enum
	}
	switch s.Format {
	case FormatEmail:
		out["format"] = "email"
	case FormatURL:
		out["format"] = "uri"
	}
	if s.Default != nil {
		out["default"] = s.Default
	}
	for k, v := range map[string]any{"minimum": s.Minimum, "maximum": s.Maximum} {
		if p := v.(*float64); p != nil {
			out[k] = *p
		}
	}
	for k, v := range map[string]*int{"maxLength": s.MaxLength, "minItems": s.MinItems, "maxItems": s.MaxItems} {
		if v != nil {
			out[k] = *v
		}
	}
	return out
}

// normalizeDefaults приводит значения по умолчанию к типам JSON (int из YAML →
// float64), чтобы схема из YAML и из JSON совпадала.
func (s *Schema) normalizeDefaults() {
	if s == nil {
		return
	}
	if s.Default != nil {
		b, _ := json.Marshal(s.Default)
		var v any
		_ = json.Unmarshal(b, &v)
		s.Default = v
	}
	for _, p := range s.Properties {
		p.Schema.normalizeDefaults()
	}
	s.Items.normalizeDefaults()
}

// MustSchema разбирает схему из YAML (или JSON) и проверяет её — для встроенных
// коннекторов и тестов; ошибка — паника.
func MustSchema(src string) Schema {
	var s Schema
	dec := yaml.NewDecoder(strings.NewReader(src))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		panic(fmt.Sprintf("manifest: schema: %v", err))
	}
	if err := s.validate(""); err != nil {
		panic(fmt.Sprintf("manifest: schema: %v", err))
	}
	s.normalizeDefaults()
	return s
}
