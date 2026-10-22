// SPDX-License-Identifier: AGPL-3.0-only

package pipeline

import (
	"context"
	"slices"
	"strings"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"

	"github.com/gate4ai/gate4ai/internal/config"
	"github.com/gate4ai/gate4ai/internal/policy"
)

// MaxDepth — глубина вложенных вызовов service_call.
const MaxDepth = 4

// callInfo — исполняемое действие: на него ссылаются вложенные вызовы.
type callInfo struct {
	Agent string
	Act   manifest.Action // исполняемое действие: его calls и класс
	ID    string          // квитанция этого действия
	Root  string          // квитанция корневого действия агента
	Depth int             // 0 — действие агента
	Chain []string        // коннекторы от корня до этого действия
	// Session — сеанс браузера агента, в котором идёт вход коннектором входа
	// (browser.login учётки с via): страница вложенных вызовов browser.
	Session string
}

type callKey struct{}

func withCall(ctx context.Context, c callInfo) context.Context {
	return context.WithValue(ctx, callKey{}, c)
}

func callFrom(ctx context.Context) (callInfo, bool) {
	c, ok := ctx.Value(callKey{}).(callInfo)
	return c, ok
}

// nestedKey — вызывающий коннектор вложенного вызова (для ExecRequest.Caller).
type nestedKey struct{}

type nestedCall struct {
	caller string
	params map[string]any
}

// ExecuteNested — вызов service_call коннектора caller во время его действия
// (ctx — контекст этого действия): поставщик и контракт сверяются с
// разрешением rule из манифеста вызывающего, аргументы — со схемой поставщика;
// подвызов объявлен в calls вызывающего действия, а класс не выше его класса —
// поэтому разрешение вызывающего действия его покрывает (policy.Request.Nested:
// без правила разрешено, явный deny действует). Квитанция ссылается
// на родительскую; страница и прочее состояние поставщика живут до конца
// корневого действия (Caller.Call).
func (p *Pipeline) ExecuteNested(ctx context.Context, caller string, rule manifest.ServiceRule, r abi.ServiceCallRequest) (abi.ExecResult, error) {
	parent, ok := callFrom(ctx)
	if !ok {
		return abi.ExecResult{}, abi.Errorf(abi.CodeBadRequest, "service_call is only available while an action runs")
	}
	full := r.Target + "." + r.Action
	if parent.Depth+1 >= MaxDepth {
		return abi.ExecResult{}, abi.Errorf(abi.CodeLimit, "service_call %s: nesting deeper than %d", full, MaxDepth)
	}
	if slices.Contains(parent.Chain, r.Target) {
		return abi.ExecResult{}, abi.Errorf(abi.CodeBadRequest, "service_call %s: cycle %s → %s", full, strings.Join(parent.Chain, " → "), r.Target)
	}
	if !parent.Act.CallsAction(r.Target, r.Action) {
		return abi.ExecResult{}, abi.Errorf(abi.CodeCapabilityDenied, "service_call %s: action %s.%s does not declare this call (calls)", full, caller, parent.Act.Name)
	}
	cli, act, ok := p.Registry.Lookup(full)
	if !ok {
		return abi.ExecResult{}, abi.Errorf(abi.CodeNotFound, "service_call %s: connector or action is not installed", full)
	}
	if manifest.ClassRank(act.Class) > manifest.ClassRank(parent.Act.Class) {
		return abi.ExecResult{}, abi.Errorf(abi.CodeCapabilityDenied, "service_call %s: class %s is above the class %s of %s.%s, whose approval would not cover it",
			full, act.Class, parent.Act.Class, caller, parent.Act.Name)
	}
	m := cli.Manifest()
	if !m.IsService(r.Action) {
		return abi.ExecResult{}, abi.Errorf(abi.CodeCapabilityDenied, "service_call %s: %s does not offer %s to other connectors", full, r.Target, r.Action)
	}
	if m.Contract != rule.Contract {
		return abi.ExecResult{}, abi.Errorf(abi.CodeNotSupported, "service_call %s: %s %s provides contract %d, %s needs contract %d: update the connectors",
			full, r.Target, m.Version, m.Contract, caller, rule.Contract)
	}
	args, err := act.Normalize(r.Args)
	if err != nil {
		return abi.ExecResult{}, abi.Errorf(abi.CodeBadRequest, "service_call %s: %v", full, err)
	}
	prov, perr := p.provenance(args)
	req := policy.Request{Agent: parent.Agent, Action: full, Class: act.Class, Args: args, Provenance: prov, Now: p.Now(), Nested: true, StateError: perr}
	dec := p.decidePolicy(req)
	switch dec.Effect {
	case config.EffectDeny:
		res := p.denyAt(parent.Agent, act, full, args, dec, nil, parent.ID)
		return abi.ExecResult{}, abi.Errorf(abi.CodeCapabilityDenied, "service_call %s denied by policy: %s", full, res.Reason)
	}
	ctx = context.WithValue(ctx, nestedKey{}, nestedCall{caller: caller, params: rule.Params})
	res := p.run(ctx, parent.Agent, full, cli, act, args, dec, nil)
	switch res.Status {
	case StatusOK:
	case StatusVerificationFailed:
		return abi.ExecResult{}, abi.Errorf(abi.CodeConnector, "service_call %s: %s", full, res.Message)
	default:
		code := abi.CodeConnector
		switch {
		case res.connectorCode != "":
			code = res.connectorCode
		case res.Code == CodeInvalidArgs:
			code = abi.CodeBadRequest
		}
		return abi.ExecResult{}, abi.Errorf(code, "service_call %s: %s", full, res.Message)
	}
	return abi.ExecResult{Structured: res.Result, Content: res.Content, ResultRef: res.ID}, nil
}
