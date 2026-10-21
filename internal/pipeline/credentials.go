// SPDX-License-Identifier: AGPL-3.0-only

package pipeline

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
	"github.com/gate4ai/gate4ai/sdk-go/domain"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"

	"github.com/gate4ai/gate4ai/internal/audit"
	"github.com/gate4ai/gate4ai/internal/config"
	"github.com/gate4ai/gate4ai/internal/policy"
)

// BrowserConnector — нативный коннектор браузера: только ему хост передаёт
// учётки входа (credentials), и только в действиях входа — значения.
const BrowserConnector = "browser"

// Действия браузера, которым передаются значения полей учётки.
const (
	ActionLogin      = "login"
	ActionFillSecret = "fill_secret"
)

// CodeSecretDenied — учётка недоступна вызывающему (агенту — ответ этим кодом).
const CodeSecretDenied = abi.CodeSecretDenied

// Vault — значения секретов по алиасу (только для ввода учёток в браузере).
type Vault interface {
	Get(alias string) (string, error)
}

// Question — вопрос человеку с вводом значения (код из SMS) для
// browser.fill_secret с source: human; ответ идёт сразу в поле страницы.
type Question struct {
	Agent   string   // агент корневого действия
	Caller  string   // коннектор, который вводит код ("" — агент)
	Account string   // учётка
	Bind    []string // сайт входа
	Text    string   // вопрос владельца из учётки или коннектора
}

// accessor — кто пользуется учёткой: профиль агента корневого действия или
// вызывающий коннектор (service_call).
type accessor struct {
	agent, profile, connector string
}

func (a accessor) String() string {
	if a.connector != "" {
		return "connector " + a.connector
	}
	return "agent " + a.agent
}

func (p *Pipeline) accessorOf(ctx context.Context, agent string) accessor {
	if n, ok := ctx.Value(nestedKey{}).(nestedCall); ok && n.caller != "" {
		return accessor{agent: agent, connector: n.caller}
	}
	return accessor{agent: agent, profile: p.cfg().Agents[agent].Profile}
}

// allowed — учётка доступна: агенту — его профиль в use; коннектору — он в
// connectors, это коннектор входа учётки (via) или его слот связан с ней в
// connectors.<коннектор>.credentials, а bind учётки — в bind слота из манифеста.
func (p *Pipeline) allowed(cfg *config.Config, alias string, cr config.Credential, a accessor) bool {
	if a.connector == "" {
		return a.profile != "" && slices.Contains(cr.Use, a.profile)
	}
	if slices.Contains(cr.Connectors, a.connector) || (cr.Via != nil && cr.Via.Connector == a.connector) {
		return true
	}
	cli, ok := p.Registry.Get(a.connector)
	if !ok {
		return false
	}
	m := cli.Manifest()
	kind := manifest.SlotForm
	if cr.Via != nil {
		kind = manifest.SlotVia
	}
	for _, s := range m.Credentials {
		if cfg.SlotAccount(a.connector, s.Slot) != alias || !s.Accepts(kind) {
			continue // учётка другого вида, чем ждёт слот (форма / коннектор входа)
		}
		slot := s.Origins(cfg.Connectors[a.connector])
		if len(slot) > 0 && !slices.ContainsFunc(cr.Origins(), func(o string) bool { return !slices.Contains(slot, o) }) {
			return true
		}
	}
	return false
}

// accountsFor — учётки, доступные вызывающему: на их сайтах входа страница
// браузера может находиться.
func (p *Pipeline) accountsFor(cfg *config.Config, a accessor) []abi.Account {
	var out []abi.Account
	names := make([]string, 0, len(cfg.Credentials))
	for n := range cfg.Credentials {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, alias := range names {
		cr := cfg.Credentials[alias]
		if !p.allowed(cfg, alias, cr, a) {
			continue
		}
		acc := abi.Account{Alias: alias, Bind: cr.Origins(), LoginPage: pageOf(cr)}
		if cr.Via != nil {
			acc.Via = cr.Via.Full()
		}
		out = append(out, acc)
	}
	return out
}

// resolveAccount — аргумент account: "@алиас", слот вызывающего коннектора или алиас без "@".
func resolveAccount(cfg *config.Config, a accessor, arg string) string {
	if strings.HasPrefix(arg, "@") {
		return arg
	}
	if a.connector != "" {
		if alias := cfg.SlotAccount(a.connector, arg); alias != "" {
			return alias
		}
	}
	return "@" + arg
}

// pageOf — признак страницы входа учётки.
func pageOf(cr config.Credential) *abi.AccountPage {
	if cr.LoginPage == nil {
		return nil
	}
	return &abi.AccountPage{Selector: cr.LoginPage.Selector, URLPrefix: cr.LoginPage.URLPrefix}
}

// formOf — учётка с формой для browser (без значений): поля формы или
// коннектора входа (via).
func formOf(alias string, cr config.Credential) *abi.Account {
	acc := &abi.Account{Alias: alias, Bind: cr.Origins(), LoginPage: pageOf(cr)}
	if cr.Via != nil {
		acc.Via = cr.Via.Full()
	}
	for _, fl := range cr.AllFields() {
		name := fl.Name
		if name == "" {
			name = fl.Secret
		}
		acc.Fields = append(acc.Fields, abi.AccountField{Name: name, Selector: fl.Selector, Value: fl.Value, Secret: fl.Secret, Type: fl.Type})
	}
	if f := cr.Form; f != nil {
		acc.Submit = &abi.AccountSubmit{Selector: f.Submit.Selector, Press: f.Submit.Press}
		if f.Success != nil {
			acc.Success = f.Success.URLPrefix
		}
	}
	return acc
}

// fieldMatches — поле учётки по имени из fill_secret (как его ищет browser):
// имя, алиас секрета или алиас без имени учётки (password для @emias.password).
func fieldMatches(alias string, f abi.AccountField, name string) bool {
	return name != "" && (f.Name == name || f.Secret == name || f.Secret == "@"+name || f.Secret == alias+"."+name)
}

// secretUse — использованное поле учётки (для события secret.use).
type secretUse struct {
	account, field string
	bind           []string
}

// loginPrep — подготовленный вход: учётка, коннектор входа и введённые поля.
type loginPrep struct {
	alias string
	cr    config.Credential
	via   *config.LoginVia
	uses  []secretUse
	who   accessor
	human func() error // fill_secret с source: human — спросить после проверки браузером
}

// ArgPrecheck — служебный аргумент browser.fill_secret: только проверки
// (страница на сайте входа, поле подходит), без ввода. Его ставит хост; агент
// и коннектор передать его не могут — аргументы проверяются по схеме.
const ArgPrecheck = "precheck"

// ArgRecordOnly — служебный аргумент browser.login: записать в сеанс агента
// шаг входа через коннектор входа (результат — в result) без самого входа.
const ArgRecordOnly = "record_only"

func denied(f string, a ...any) error { return abi.Errorf(abi.CodeSecretDenied, f, a...) }

// prepareBrowser дополняет запрос к browser учётками вызывающего; для login и
// fill_secret — формой запрошенной учётки и значениями её полей (Revealed),
// если учётка доступна вызывающему (иначе secret_denied). Другим действиям и
// коннекторам значения не передаются никогда.
func (p *Pipeline) prepareBrowser(ctx context.Context, agent string, act manifest.Action, req *abi.ExecRequest) (*loginPrep, error) {
	cfg := p.cfg()
	who := p.accessorOf(ctx, agent)
	req.Accounts = p.accountsFor(cfg, who)
	if act.Name != ActionLogin && act.Name != ActionFillSecret {
		return nil, nil
	}
	arg, _ := req.Args["account"].(string)
	alias := resolveAccount(cfg, who, arg)
	cr, ok := cfg.Credentials[alias]
	if !ok || !p.allowed(cfg, alias, cr, who) {
		// Без подсказки, есть ли такая учётка у владельца.
		return nil, denied("account %s is not available to %s (credentials.%s: use / connectors)", arg, who, arg)
	}
	args := make(map[string]any, len(req.Args))
	for k, v := range req.Args {
		args[k] = v
	}
	args["account"] = alias
	req.Args = args
	req.Login = formOf(alias, cr)
	prep := &loginPrep{alias: alias, cr: cr, who: who}
	if cr.Via != nil {
		if act.Name == ActionLogin {
			if who.connector == cr.Via.Connector {
				return nil, abi.Errorf(abi.CodeBadRequest, "%s is the sign-in connector of %s: enter its fields with browser.fill_secret, not browser.login", who.connector, alias)
			}
			prep.via = cr.Via
			return prep, nil
		}
	}
	reveal := func(secret, field string) error {
		if p.Vault == nil {
			return abi.Errorf(abi.CodeNotSupported, "no vault")
		}
		v, err := p.Vault.Get(secret)
		if err != nil || v == "" {
			return denied("secret %s of account %s is not set: the owner sets it — gate4ai vault set %s (asks with hidden input) or Web UI → Secrets", secret, alias, secret)
		}
		if req.Revealed == nil {
			req.Revealed = map[string]string{}
		}
		req.Revealed[secret] = v
		prep.uses = append(prep.uses, secretUse{account: alias, field: field, bind: req.Login.Bind})
		return nil
	}
	if act.Name == ActionLogin {
		for _, f := range req.Login.Fields {
			if f.Secret != "" {
				if err := reveal(f.Secret, f.Name); err != nil {
					return nil, err
				}
			}
		}
		return prep, nil
	}
	field, _ := args["field"].(string)
	if src, _ := args["source"].(string); src == "human" {
		// Спрашивает invoke после проверки браузером (сайт входа, поле): иначе
		// человек вводит код, а браузер отказывает — код из SMS пропадает.
		prep.human = func() error {
			v, err := p.askHuman(ctx, who, alias, cr, field, args)
			if err != nil {
				return err
			}
			req.Revealed = map[string]string{abi.HumanValue: v}
			name := field
			if name == "" {
				name = "code"
			}
			prep.uses = append(prep.uses, secretUse{account: alias, field: "human:" + name, bind: req.Login.Bind})
			return nil
		}
		return prep, nil
	}
	for _, f := range req.Login.Fields {
		if fieldMatches(alias, f, field) && f.Secret != "" {
			return prep, reveal(f.Secret, f.Name)
		}
	}
	return prep, nil // поле-константа или неизвестное поле: ответит browser
}

// AskTimeout — сколько ждать ответа человека на вопрос с вводом.
var AskTimeout = 5 * time.Minute

// askHuman — код от человека через канал подтверждений: вопрос владельца из
// учётки (prompts) или коннектора; ответ — только в поле страницы.
func (p *Pipeline) askHuman(ctx context.Context, who accessor, alias string, cr config.Credential, field string, args map[string]any) (string, error) {
	if p.Ask == nil {
		return "", abi.Errorf(abi.CodeNotSupported, "no channel to ask the user")
	}
	text := cr.Prompts[field]
	if text == "" {
		text, _ = args["prompt"].(string)
	}
	if text == "" {
		text = "a one-time code"
	}
	ctx, cancel := context.WithTimeout(ctx, AskTimeout)
	defer cancel()
	v, err := p.Ask(ctx, Question{Agent: who.agent, Caller: who.connector, Account: alias, Bind: cr.Origins(), Text: text})
	if err != nil {
		return "", denied("no answer from the user: %v", err)
	}
	return v, nil
}

// loginVia — browser.login учётки со сложным входом: daemon вызывает действие
// коннектора входа с тем же account вложенным вызовом этого действия (тот же
// корень, та же страница; в сеансе агента — его страница). Политика: явный
// deny на действие коннектора входа действует, класс — не выше write.
func (p *Pipeline) loginVia(ctx context.Context, agent string, info callInfo, prep *loginPrep, session string) (abi.ExecResult, error) {
	full := prep.via.Full()
	cli, act, ok := p.Registry.Lookup(full)
	if !ok {
		return abi.ExecResult{}, abi.Errorf(abi.CodeNotFound, "sign-in connector %s of account %s is not installed", full, prep.alias)
	}
	if manifest.ClassRank(act.Class) > manifest.ClassRank(policy.ClassWrite) {
		return abi.ExecResult{}, abi.Errorf(abi.CodeCapabilityDenied, "sign-in action %s has class %s, above write of browser.login", full, act.Class)
	}
	if info.Depth+1 >= MaxDepth {
		return abi.ExecResult{}, abi.Errorf(abi.CodeLimit, "%s: nesting deeper than %d", full, MaxDepth)
	}
	args, err := act.Normalize(map[string]any{"account": prep.alias})
	if err != nil {
		return abi.ExecResult{}, abi.Errorf(abi.CodeBadRequest, "sign-in action %s: %v", full, err)
	}
	dec := p.Engine().Decide(policy.Request{Agent: agent, Action: full, Class: act.Class, Args: args, Now: p.Now(), Nested: true}, p.State.Counters())
	if dec.Effect == config.EffectDeny {
		res := p.deny(agent, act, full, args, dec, nil)
		return abi.ExecResult{}, abi.Errorf(abi.CodeCapabilityDenied, "%s denied by policy: %s", full, res.Reason)
	}
	// Коннектор входа работает через browser: в цепочке вызовов его нет.
	info.Chain = slices.DeleteFunc(slices.Clone(info.Chain), func(c string) bool { return c == BrowserConnector })
	info.Session = session
	ctx = context.WithValue(withCall(ctx, info), nestedKey{}, nestedCall{caller: BrowserConnector})
	res := p.run(ctx, agent, full, cli, act, args, dec, nil)
	if res.Status != StatusOK {
		code := res.connectorCode
		if code == "" {
			code = abi.CodeConnector
		}
		return abi.ExecResult{}, abi.Errorf(code, "sign-in connector %s: %s", full, res.Message)
	}
	out := map[string]any{}
	if m, ok := res.Result.(map[string]any); ok {
		for _, k := range []string{"status", "url"} {
			if s, ok := m[k].(string); ok {
				out[k] = s
			}
		}
	}
	st, _ := out["status"].(string)
	return abi.ExecResult{Structured: out, ResultRef: st}, nil
}

// preInput — ошибки browser до ввода значений: секрет не использован.
var preInput = map[string]bool{"wrong_origin": true, "form_mismatch": true, "use_connector": true, "secret_on_page": true,
	abi.CodeSecretDenied: true, abi.CodeBadRequest: true, abi.CodeNotFound: true}

// journalUses пишет события secret.use (алиас, поле, origin страницы, кто,
// id действия) — без значений; parent — квитанция действия.
func (p *Pipeline) journalUses(agent, action, parent string, prep *loginPrep, ex abi.ExecResult, execErr error) {
	if prep == nil || len(prep.uses) == 0 {
		return
	}
	var ae *abi.Error
	if errors.As(execErr, &ae) && preInput[ae.Code] {
		return
	}
	origin := ""
	if m, ok := ex.Structured.(map[string]any); ok {
		origin, _ = m["origin"].(string)
	}
	for _, u := range prep.uses {
		o := origin
		if o == "" {
			o = strings.Join(u.bind, ", ")
		}
		args := map[string]any{"account": u.account, "field": u.field, "origin": domain.OriginOf(o), "action": action}
		if domain.OriginOf(o) == "" {
			args["origin"] = o
		}
		if prep.who.connector != "" {
			args["connector"] = prep.who.connector
		}
		r := &audit.Receipt{ID: audit.NewID(), Agent: agent, Parent: parent, Action: "secret.use", Class: policy.ClassRead, Args: args,
			Decision:     audit.Decision{Effect: config.EffectAllow, RuleIDs: []string{}, Reason: "used within " + action + " (" + parent + ")"},
			Execution:    audit.Execution{Status: "ok"},
			Verification: audit.Verification{Status: "not_applicable"}}
		if _, err := p.Journal.Append(r); err != nil {
			p.Log.Error("journal append", "err", err)
		}
	}
}

// accountPreview — что увидит человек в подтверждении: учётки, которые
// использует действие (browser.login / fill_secret или слоты коннектора).
func (p *Pipeline) accountPreview(agent, action string, act manifest.Action, args map[string]any) string {
	cfg := p.cfg()
	conn, name, _ := strings.Cut(action, ".")
	if conn == BrowserConnector && (name == ActionLogin || name == ActionFillSecret) {
		arg, _ := args["account"].(string)
		alias := resolveAccount(cfg, accessor{}, arg)
		if cr, ok := cfg.Credentials[alias]; ok {
			return fmt.Sprintf("signs in on %s with account %s", strings.Join(cr.Origins(), ", "), alias)
		}
		return ""
	}
	if !act.CallsAction(BrowserConnector, ActionLogin) && !act.CallsAction(BrowserConnector, ActionFillSecret) {
		return ""
	}
	cli, ok := p.Registry.Get(conn)
	if !ok {
		return ""
	}
	var lines []string
	for _, s := range cli.Manifest().Credentials {
		if alias := cfg.SlotAccount(conn, s.Slot); alias != "" {
			lines = append(lines, fmt.Sprintf("signs in on %s with account %s (slot %s)", strings.Join(cfg.Credentials[alias].Origins(), ", "), alias, s.Slot))
		}
	}
	for _, alias := range sortedCredentials(cfg) {
		if slices.Contains(cfg.Credentials[alias].Connectors, conn) {
			lines = append(lines, fmt.Sprintf("may sign in on %s with account %s", strings.Join(cfg.Credentials[alias].Origins(), ", "), alias))
		}
	}
	return strings.Join(lines, "\n")
}

func sortedCredentials(cfg *config.Config) []string {
	out := make([]string, 0, len(cfg.Credentials))
	for n := range cfg.Credentials {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
