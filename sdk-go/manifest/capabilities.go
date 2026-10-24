// SPDX-License-Identifier: Apache-2.0

package manifest

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Capabilities — возможности коннектора. Всё, что не объявлено, недоступно:
// функция хоста вернёт capability_denied. Значения могут ссылаться на настройки
// коннектора шаблоном ${setting:путь} (путь через точку: imap.host); список
// в настройке раскрывается в несколько записей, пустое значение запись убирает.
type Capabilities struct {
	Files   []string    `json:"files,omitempty" yaml:"files,omitempty"` // "<папка>/**" — дерево, иначе один файл
	Storage *StorageCap `json:"storage,omitempty" yaml:"storage,omitempty"`
	// Services — действия других коннекторов, которые можно вызвать через service_call.
	Services []ServiceCap `json:"services,omitempty" yaml:"services,omitempty"`
	// Reveal — алиасы Vault, значения которых хост передаёт коннектору
	// (только runtime builtin, с отдельным согласием владельца).
	Reveal []string `json:"reveal,omitempty" yaml:"reveal,omitempty"`
}

// ServiceCap — действия поставщика Target с мажором контракта Contract.
// Params — разрешение, которое хост передаёт поставщику при каждом вызове
// (например, сайты браузера); строки могут быть шаблонами ${setting:…}.
type ServiceCap struct {
	Target   string         `json:"target" yaml:"target"`
	Contract int            `json:"contract" yaml:"contract"`
	Actions  []string       `json:"actions" yaml:"actions"`
	Params   map[string]any `json:"params,omitempty" yaml:"params,omitempty"`
}

// StorageCap — собственное KV-хранилище коннектора.
type StorageCap struct {
	QuotaMB int `json:"quota_mb" yaml:"quota_mb"`
}

// Limits — лимиты одного вызова.
type Limits struct {
	MemoryMB        int `json:"memory_mb,omitempty" yaml:"memory_mb,omitempty"`
	TimeoutS        int `json:"timeout_s,omitempty" yaml:"timeout_s,omitempty"`
	VerifyDeadlineS int `json:"verify_deadline_s,omitempty" yaml:"verify_deadline_s,omitempty"`
}

// Значения лимитов по умолчанию.
const (
	DefaultMemoryMB        = 128
	DefaultTimeoutS        = 60
	DefaultVerifyDeadlineS = 86400
)

// Верхние границы лимитов. MaxMemoryMB — предел линейной памяти WASM (65536
// страниц по 64 КиБ); больше манифест объявить не может (Validate). Для новых
// установок действуют более строгие InstallMaxMemoryMB и InstallMaxTimeoutS
// (CheckInstall): уже установленные пакеты с большими значениями загружаются.
const (
	MaxMemoryMB        = 4096
	MaxTimeoutS        = 3600
	MaxVerifyDeadlineS = 30 * 86400
	InstallMaxMemoryMB = 1024
	InstallMaxTimeoutS = 600
)

func (l Limits) validate() error {
	switch {
	case l.MemoryMB < 0 || l.TimeoutS < 0 || l.VerifyDeadlineS < 0:
		return errors.New("negative limits")
	case l.MemoryMB > MaxMemoryMB:
		return fmt.Errorf("limits.memory_mb %d is above %d", l.MemoryMB, MaxMemoryMB)
	case l.TimeoutS > MaxTimeoutS:
		return fmt.Errorf("limits.timeout_s %d is above %d", l.TimeoutS, MaxTimeoutS)
	case l.VerifyDeadlineS > MaxVerifyDeadlineS:
		return fmt.Errorf("limits.verify_deadline_s %d is above %d", l.VerifyDeadlineS, MaxVerifyDeadlineS)
	}
	return nil
}

// CheckInstall — лимиты допустимы для новой установки или обновления
// (строже Validate: память и время вызова одного экземпляра).
func (l Limits) CheckInstall() error {
	if l.MemoryMB > InstallMaxMemoryMB {
		return fmt.Errorf("limits.memory_mb %d is above %d allowed for installation", l.MemoryMB, InstallMaxMemoryMB)
	}
	if l.TimeoutS > InstallMaxTimeoutS {
		return fmt.Errorf("limits.timeout_s %d is above %d allowed for installation", l.TimeoutS, InstallMaxTimeoutS)
	}
	return nil
}

// Summary — лимиты выше значений по умолчанию человеческим языком (входят в
// согласие); память — только для WASM (встроенный коннектор ею не ограничен).
func (l Limits) Summary(runtime string) []string {
	var out []string
	if l.MemoryMB > DefaultMemoryMB && (runtime == RuntimeWasm || runtime == "") {
		out = append(out, fmt.Sprintf("memory up to %d MB per call", l.MemoryMB))
	}
	if l.TimeoutS > DefaultTimeoutS {
		out = append(out, fmt.Sprintf("a call may run up to %d s", l.TimeoutS))
	}
	return out
}

// WithDefaults — лимиты с подставленными значениями по умолчанию.
func (l Limits) WithDefaults() Limits {
	if l.MemoryMB == 0 {
		l.MemoryMB = DefaultMemoryMB
	}
	if l.TimeoutS == 0 {
		l.TimeoutS = DefaultTimeoutS
	}
	if l.VerifyDeadlineS == 0 {
		l.VerifyDeadlineS = DefaultVerifyDeadlineS
	}
	return l
}

var tmplRe = regexp.MustCompile(`\$\{setting:([a-zA-Z0-9_.]+)\}`)

func checkTemplate(field, v string) error {
	if strings.Contains(strings.ReplaceAll(v, "${setting:", ""), "${") {
		return fmt.Errorf("%s: bad template in %q", field, v)
	}
	if n := len(tmplRe.FindAllString(v, -1)); n > 1 {
		return fmt.Errorf("%s: only one ${setting:…} per value (%q)", field, v)
	}
	return nil
}

func (c Capabilities) validate() error {
	seenSvc := map[string]bool{}
	for _, sc := range c.Services {
		if !nameRe.MatchString(sc.Target) || seenSvc[sc.Target] {
			return fmt.Errorf("capabilities.services: bad or duplicate target %q", sc.Target)
		}
		seenSvc[sc.Target] = true
		if sc.Contract < 1 {
			return fmt.Errorf("capabilities.services: %s: contract must be >= 1", sc.Target)
		}
		if len(sc.Actions) == 0 {
			return fmt.Errorf("capabilities.services: %s: no actions", sc.Target)
		}
		for _, a := range sc.Actions {
			if !nameRe.MatchString(a) {
				return fmt.Errorf("capabilities.services: %s: bad action %q", sc.Target, a)
			}
		}
		if err := checkParams("capabilities.services."+sc.Target+".params", sc.Params); err != nil {
			return err
		}
	}
	for _, a := range c.Reveal {
		if !strings.HasPrefix(a, "@") && !tmplRe.MatchString(a) {
			return fmt.Errorf("capabilities.reveal: alias %q must start with @ or be ${setting:…}", a)
		}
		if err := checkTemplate("capabilities.reveal", a); err != nil {
			return err
		}
	}
	for _, f := range c.Files {
		if err := checkTemplate("capabilities.files", f); err != nil {
			return err
		}
		if !strings.HasPrefix(f, "/") && !strings.HasPrefix(f, "~") && !strings.HasPrefix(f, "${setting:") {
			return fmt.Errorf("capabilities.files: %q must be absolute, ~/… or ${setting:…}", f)
		}
	}
	if c.Storage != nil && c.Storage.QuotaMB <= 0 {
		return errors.New("capabilities.storage.quota_mb must be positive")
	}
	return nil
}

// Resolved — возможности с подставленными настройками конкретного экземпляра.
type Resolved struct {
	Files        []string
	StorageBytes int64
	Services     []ServiceRule
	Reveal       []string
}

// ServiceRule — разрешённые действия поставщика и параметры с подставленными настройками.
type ServiceRule struct {
	Target   string
	Contract int
	Actions  []string
	Params   map[string]any
}

// Allows — действие action поставщика разрешено.
func (s ServiceRule) Allows(action string) bool { return slices.Contains(s.Actions, action) }

// checkParams проверяет шаблоны в строках параметров.
func checkParams(field string, v any) error {
	switch t := v.(type) {
	case string:
		return checkTemplate(field, t)
	case map[string]any:
		for k, x := range t {
			if err := checkParams(field+"."+k, x); err != nil {
				return err
			}
		}
	case []any:
		for _, x := range t {
			if err := checkParams(field, x); err != nil {
				return err
			}
		}
	}
	return nil
}

// expandParams подставляет настройки в параметры: шаблон в списке
// раскрывается в несколько элементов, отдельное значение — в одно (пустое —
// "", несколько — список).
func expandParams(v any, settings map[string]any) any {
	switch t := v.(type) {
	case string:
		x := Expand(t, settings)
		switch len(x) {
		case 0:
			return ""
		case 1:
			return x[0]
		}
		out := make([]any, len(x))
		for i := range x {
			out[i] = x[i]
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[k] = expandParams(x, settings)
		}
		return out
	case []any:
		out := []any{}
		for _, x := range t {
			if str, ok := x.(string); ok {
				for _, e := range Expand(str, settings) {
					out = append(out, e)
				}
				continue
			}
			out = append(out, expandParams(x, settings))
		}
		return out
	}
	return v
}

// lookup — значение настройки по пути через точку.
func lookup(settings map[string]any, path string) any {
	var cur any = settings
	for _, k := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[k]
	}
	return cur
}

func scalars(v any) []string {
	switch t := v.(type) {
	case nil:
		return nil
	case string:
		if t == "" {
			return nil
		}
		return []string{t}
	case []any:
		return scalarsOf(t)
	case []string:
		return scalarsOf(t)
	case float64:
		return []string{strconv.FormatFloat(t, 'f', -1, 64)}
	case int:
		return []string{strconv.Itoa(t)}
	case bool:
		return []string{strconv.FormatBool(t)}
	}
	return []string{fmt.Sprint(v)}
}

func scalarsOf[T any](xs []T) []string {
	var out []string
	for _, x := range xs {
		out = append(out, scalars(x)...)
	}
	return out
}

// Expand раскрывает шаблон ${setting:…} по настройкам: ноль, одно или несколько значений.
func Expand(v string, settings map[string]any) []string {
	m := tmplRe.FindStringSubmatchIndex(v)
	if m == nil {
		if v == "" {
			return nil
		}
		return []string{v}
	}
	var out []string
	for _, x := range scalars(lookup(settings, v[m[2]:m[3]])) {
		out = append(out, v[:m[0]]+x+v[m[1]:])
	}
	return out
}

func expandAll(vs []string, settings map[string]any) []string {
	var out []string
	seen := map[string]bool{}
	for _, v := range vs {
		for _, x := range Expand(v, settings) {
			if !seen[x] {
				seen[x] = true
				out = append(out, x)
			}
		}
	}
	return out
}

// Resolve подставляет настройки экземпляра в возможности.
func (c Capabilities) Resolve(settings map[string]any) (Resolved, error) {
	var r Resolved
	for _, sc := range c.Services {
		var p map[string]any
		if sc.Params != nil {
			p = expandParams(sc.Params, settings).(map[string]any)
		}
		r.Services = append(r.Services, ServiceRule{Target: sc.Target, Contract: sc.Contract, Actions: sc.Actions, Params: p})
	}
	for _, a := range expandAll(c.Reveal, settings) {
		if !strings.HasPrefix(a, "@") {
			return r, fmt.Errorf("capabilities.reveal: setting value %q is not a vault alias (@name)", a)
		}
		r.Reveal = append(r.Reveal, a)
	}
	r.Files = expandAll(c.Files, settings)
	if c.Storage != nil {
		r.StorageBytes = int64(c.Storage.QuotaMB) << 20
	}
	return r, nil
}

// Summary — возможности человеческим языком (согласие при установке).
func (c Capabilities) Summary() []string {
	var out []string
	for _, sc := range c.Services {
		line := fmt.Sprintf("call %s (contract %d): %s", sc.Target, sc.Contract, strings.Join(sc.Actions, ", "))
		if len(sc.Params) > 0 {
			b, _ := json.Marshal(sc.Params)
			line += " with " + string(b)
		}
		out = append(out, line)
	}
	for _, a := range c.Reveal {
		out = append(out, "reveal the value of "+a+" to the connector")
	}
	for _, f := range c.Files {
		out = append(out, "files "+f)
	}
	if c.Storage != nil {
		out = append(out, fmt.Sprintf("storage %d MB", c.Storage.QuotaMB))
	}
	sort.Strings(out)
	return out
}

// Parse читает манифест из YAML (или JSON — подмножество YAML) и проверяет его.
func Parse(b []byte) (Manifest, error) {
	var m Manifest
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		return m, fmt.Errorf("manifest: %w", err)
	}
	normalizeDefaults(&m)
	return m, m.Validate()
}

// normalizeDefaults приводит значения по умолчанию аргументов к типам JSON
// (int из YAML → float64), чтобы манифест из YAML и из JSON совпадали.
func normalizeDefaults(m *Manifest) {
	for i := range m.Actions {
		m.Actions[i].InputSchema.normalizeDefaults()
		m.Actions[i].OutputSchema.normalizeDefaults()
	}
}

// Same — два манифеста совпадают по содержанию (манифест модуля и пакета).
func Same(a, b Manifest) bool {
	normalizeDefaults(&a)
	normalizeDefaults(&b)
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
