// SPDX-License-Identifier: AGPL-3.0-only

// Package policy — детерминированный Policy Engine (PDP): правила профиля
// (action + when на CEL + effect), порядок deny > ask > allow, значения по
// умолчанию по классу эффекта, лимиты и объяснение решения.
package policy

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"cel.dev/cel-go/cel"
	celast "cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"

	"github.com/gate4ai/gate4ai/internal/config"
)

// Классы эффекта действий.
const (
	ClassRead     = "read"
	ClassWrite    = "write"
	ClassExternal = "external"
	ClassMoney    = "money"
)

// DefaultEffect — эффект, если ни одно правило не сработало.
func DefaultEffect(class string) string {
	if class == ClassRead {
		return config.EffectAllow
	}
	return config.EffectAsk
}

// Implicit — внутренние коннекторы daemon, доступные любому профилю, но не
// агентам напрямую (например, egress.connect — решение Egress Proxy о домене).
var Implicit = map[string]bool{"egress": true}

// Everyone — внутренние коннекторы daemon, видимые и доступные любому профилю
// (gate4ai — предложения изменений настроек от агента; применяет владелец).
var Everyone = map[string]bool{"gate4ai": true}

// OwnerOnly — действия, которые правило allow не разрешает: их подтверждает
// владелец (deny по-прежнему запрещает). gate4ai.change_policy меняет права
// агентов — правила, сайты, учётки: агент не расширяет их себе сам.
var OwnerOnly = map[string]bool{"gate4ai.change_policy": true}

// ExplicitOnly — действия, которые правило allow разрешает, только если
// называет их явно: шаблон ("browser.*", "*") их не покрывает. Это запросы,
// меняющие данные на сайтах с cookies пользователя, и вход его учётками.
var ExplicitOnly = map[string]bool{
	"browser.post": true, "browser.put": true, "browser.patch": true, "browser.delete": true,
	"browser.login": true, "browser.fill_secret": true,
}

// allowSkip — почему правило allow не действует на действие ("" — действует).
func allowSkip(r config.Rule, action string) string {
	if r.Effect != config.EffectAllow {
		return ""
	}
	if OwnerOnly[action] {
		return fmt.Sprintf("rule %s (allow) does not apply: %s always needs the owner's approval", r.ID, action)
	}
	if ExplicitOnly[action] && r.Action != action {
		return fmt.Sprintf("rule %s (allow %s) does not cover %s: it is allowed only by a rule that names it", r.ID, r.Action, action)
	}
	return ""
}

// Warnings — правила allow, которые не действуют так, как написаны
// (предупреждение при сохранении политики и в policy_summary).
func Warnings(cfg *config.Config) []string {
	actions := slices.Sorted(maps.Keys(OwnerOnly))
	actions = append(actions, slices.Sorted(maps.Keys(ExplicitOnly))...)
	var out []string
	for _, pn := range slices.Sorted(maps.Keys(cfg.Profiles)) {
		for _, r := range cfg.Profiles[pn].Rules {
			if r.Effect != config.EffectAllow {
				continue
			}
			var owner, explicit []string
			for _, a := range actions {
				if !ActionMatches(r.Action, a) {
					continue
				}
				if OwnerOnly[a] {
					owner = append(owner, a)
				} else if ExplicitOnly[a] && r.Action != a {
					explicit = append(explicit, a)
				}
			}
			if len(owner) > 0 {
				out = append(out, fmt.Sprintf("profiles.%s rule %s: allow has no effect on %s — it always needs the owner's approval (deny still forbids it)",
					pn, r.ID, strings.Join(owner, ", ")))
			}
			if len(explicit) > 0 {
				out = append(out, fmt.Sprintf("profiles.%s rule %s: %s does not cover %s (requests that change data on sites, sign-in) — they ask you unless a rule names them",
					pn, r.ID, r.Action, strings.Join(explicit, ", ")))
			}
		}
	}
	return out
}

// Reserved — имена входа условий, которые нельзя использовать как аргументы.
var Reserved = []string{"provenance", "counters", "lists", "now", "hour", "weekday", "agent", "action", "args"}

// Request — вход PDP: структурированные параметры действия.
type Request struct {
	Agent      string
	Action     string // "email.send"
	Class      string // read | write | external | money
	Args       map[string]any
	Provenance map[string][]string // аргумент → метки происхождения
	Now        time.Time
	// Nested — вызов коннектором через service_call: доступ дан согласием на
	// capabilities.services вызывающего (профиль агента его не перечисляет), а
	// без сработавшего правила действие разрешено — его покрывает решение по
	// родительскому действию. Явные правила deny на действие действуют, ask —
	// нет: подвызов уже показан в подтверждении родительского действия.
	Nested bool
	// StateError — ошибка State Store при обогащении (метки происхождения):
	// решение — deny (state-unavailable), а не «меток нет».
	StateError error
}

// Connector — имя коннектора из действия.
func (r Request) Connector() string {
	c, _, _ := strings.Cut(r.Action, ".")
	return c
}

// Counters — счётчики лимитов (State Store).
type Counters interface {
	// SpentToday — сумма денежных действий всех агентов за сегодня.
	SpentToday() float64
	// CountToday — число выполненных действий агента по шаблону за сегодня.
	CountToday(agent, actionPattern string) int
	// AmountToday — сумма amount действий агента по шаблону за сегодня.
	AmountToday(agent, actionPattern string) float64
}

// CountersErr — счётчики, которые сообщают об ошибке чтения: при ошибке
// лимиты неизвестны, и решение — deny (state-unavailable).
type CountersErr interface {
	Err() error
}

// RuleStateUnavailable — rule_id отказа из-за недоступного State Store.
const RuleStateUnavailable = "state-unavailable"

// NoCounters — нулевые счётчики.
type NoCounters struct{}

func (NoCounters) SpentToday() float64                { return 0 }
func (NoCounters) CountToday(string, string) int      { return 0 }
func (NoCounters) AmountToday(string, string) float64 { return 0 }

// Decision — решение PDP с объяснением.
type Decision struct {
	Effect      string   `json:"effect"`
	RuleIDs     []string `json:"rule_ids"`
	Reason      string   `json:"reason"`
	Obligations []string `json:"obligations,omitempty"`
	Default     bool     `json:"default,omitempty"` // сработало значение по умолчанию класса
	// Scope — домен или папка из списков доступа, по которым принято решение
	// ("site:app.example.org", "folder:/home/u/notes"): подтверждение предлагает
	// добавить их в разрешённые или запрещённые.
	Scope  string   `json:"scope,omitempty"`
	Errors []string `json:"errors,omitempty"` // ошибки вычисления условий (fail-closed)
}

var stringSliceType = reflect.TypeOf([]string{})

// Catalog — аргументы действий из манифестов для проверки имён в условиях.
type Catalog map[string][]string

type compiled struct {
	rule config.Rule
	prg  cel.Program // nil — условия нет
}

type profile struct {
	cfg   config.Profile
	rules []compiled
}

// Engine — скомпилированная политика. Безопасен для конкурентного чтения.
type Engine struct {
	cfg      *config.Config
	profiles map[string]*profile
}

// New компилирует все правила конфигурации.
func New(cfg *config.Config, cat Catalog) (*Engine, error) {
	e := &Engine{cfg: cfg, profiles: map[string]*profile{}}
	var errs config.Errors
	for name, p := range cfg.Profiles {
		cp := &profile{cfg: p}
		for i, r := range p.Rules {
			c := compiled{rule: r}
			if strings.TrimSpace(r.When) != "" {
				prg, err := compile(r, cfg, cat)
				if err != nil {
					errs = append(errs, config.FieldError{Path: fmt.Sprintf("profiles.%s.rules[%d].when", name, i), Msg: err.Error()})
					continue
				}
				c.prg = prg
			}
			cp.rules = append(cp.rules, c)
		}
		e.profiles[name] = cp
	}
	if len(errs) > 0 {
		sort.Slice(errs, func(i, j int) bool { return errs[i].Path < errs[j].Path })
		return nil, errs
	}
	return e, nil
}

// Validator — проверка CEL для config.Parse / Config Store.
func Validator(cat Catalog) config.ExtraValidator {
	return func(c *config.Config) config.Errors {
		if _, err := New(c, cat); err != nil {
			if es, ok := err.(config.Errors); ok {
				return es
			}
			return config.Errors{{Path: "profiles", Msg: err.Error()}}
		}
		return nil
	}
}

var baseEnvOpts = []cel.EnvOption{
	cel.CrossTypeNumericComparisons(true),
	cel.Variable("provenance", cel.MapType(cel.StringType, cel.ListType(cel.StringType))),
	cel.Variable("counters", cel.MapType(cel.StringType, cel.DoubleType)),
	cel.Variable("lists", cel.MapType(cel.StringType, cel.ListType(cel.StringType))),
	cel.Variable("now", cel.TimestampType),
	cel.Variable("hour", cel.IntType),
	cel.Variable("weekday", cel.IntType),
	cel.Variable("agent", cel.StringType),
	cel.Variable("action", cel.StringType),
	cel.Variable("args", cel.MapType(cel.StringType, cel.DynType)),
	cel.Function("glob",
		cel.Overload("glob_string_string", []*cel.Type{cel.StringType, cel.StringType}, cel.BoolType,
			cel.BinaryBinding(func(a, b ref.Val) ref.Val {
				ok, err := GlobErr(string(a.(types.String)), string(b.(types.String)))
				if err != nil {
					// Неверный шаблон — ошибка вычисления (fail-closed), а не молчаливое false.
					return types.WrapErr(err)
				}
				return types.Bool(ok)
			}))),
	cel.Function("domain_matches",
		cel.Overload("domain_matches_string_list", []*cel.Type{cel.StringType, cel.ListType(cel.StringType)}, cel.BoolType,
			cel.BinaryBinding(func(a, b ref.Val) ref.Val {
				pats, err := b.ConvertToNative(stringSliceType)
				if err != nil {
					return types.WrapErr(err)
				}
				return types.Bool(DomainMatches(string(a.(types.String)), pats.([]string)))
			}))),
}

func compile(r config.Rule, cfg *config.Config, cat Catalog) (cel.Program, error) {
	env, err := cel.NewEnv(baseEnvOpts...)
	if err != nil {
		return nil, err
	}
	parsed, iss := env.Parse(r.When)
	if iss.Err() != nil {
		return nil, fmt.Errorf("CEL: %v", iss.Err())
	}
	free := freeIdents(parsed)
	var argNames map[string]bool
	if cat != nil {
		argNames = map[string]bool{}
		for act, args := range cat {
			if ActionMatches(r.Action, act) {
				for _, a := range args {
					argNames[a] = true
				}
			}
		}
	}
	var opts []cel.EnvOption
	for _, id := range free {
		if slices.Contains(Reserved, id) {
			continue
		}
		if argNames != nil && !argNames[id] {
			return nil, fmt.Errorf("unknown variable %q for action %s (arguments: %s; also: %s)",
				id, r.Action, strings.Join(slices.Sorted(maps.Keys(argNames)), ", "), strings.Join(Reserved, ", "))
		}
		opts = append(opts, cel.Variable(id, cel.DynType))
	}
	env, err = env.Extend(opts...)
	if err != nil {
		return nil, err
	}
	checked, iss := env.Compile(r.When)
	if iss.Err() != nil {
		return nil, fmt.Errorf("CEL: %v", iss.Err())
	}
	if checked.OutputType() != cel.BoolType && checked.OutputType() != cel.DynType {
		return nil, fmt.Errorf("condition must be boolean, got %s", checked.OutputType())
	}
	if err := checkGlobLiterals(parsed); err != nil {
		return nil, err
	}
	for _, name := range listRefs(r.When) {
		if _, ok := cfg.Lists[name]; !ok {
			return nil, fmt.Errorf("unknown list %q", name)
		}
	}
	return env.Program(checked, cel.EvalOptions(cel.OptOptimize), cel.InterruptCheckFrequency(100))
}

// freeIdents — свободные идентификаторы выражения (без переменных макросов).
func freeIdents(a *cel.Ast) []string {
	seen := map[string]bool{}
	iter := map[string]bool{}
	celast.PreOrderVisit(a.NativeRep().Expr(), celast.NewExprVisitor(func(e celast.Expr) {
		switch e.Kind() {
		case celast.IdentKind:
			seen[e.AsIdent()] = true
		case celast.ComprehensionKind:
			c := e.AsComprehension()
			iter[c.IterVar()] = true
			iter[c.AccuVar()] = true
			if c.HasIterVar2() {
				iter[c.IterVar2()] = true
			}
		}
	}))
	var out []string
	for id := range seen {
		if !iter[id] {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// checkGlobLiterals проверяет шаблоны glob(…, "литерал") при компиляции:
// опечатка в шаблоне не должна молча выключать правило.
func checkGlobLiterals(a *cel.Ast) error {
	var err error
	celast.PreOrderVisit(a.NativeRep().Expr(), celast.NewExprVisitor(func(e celast.Expr) {
		if err != nil || e.Kind() != celast.CallKind {
			return
		}
		c := e.AsCall()
		if c.FunctionName() != "glob" || len(c.Args()) != 2 || c.Args()[1].Kind() != celast.LiteralKind {
			return
		}
		if s, ok := c.Args()[1].AsLiteral().Value().(string); ok {
			err = CheckGlob(s)
		}
	}))
	return err
}

// listRefs — имена списков в "lists.name".
func listRefs(expr string) []string {
	var out []string
	for i := 0; ; {
		j := strings.Index(expr[i:], "lists.")
		if j < 0 {
			return out
		}
		k := i + j + len("lists.")
		if i+j > 0 {
			prev := expr[i+j-1]
			if prev == '_' || prev == '.' || (prev >= 'a' && prev <= 'z') || (prev >= 'A' && prev <= 'Z') || (prev >= '0' && prev <= '9') {
				i = k
				continue
			}
		}
		end := k
		for end < len(expr) && (expr[end] == '_' || (expr[end] >= 'a' && expr[end] <= 'z') || (expr[end] >= '0' && expr[end] <= '9')) {
			end++
		}
		if end > k {
			out = append(out, expr[k:end])
		}
		i = end
	}
}
