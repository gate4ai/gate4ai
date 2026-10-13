// SPDX-License-Identifier: FSL-1.1-ALv2

// Package mcpcli — бэкенд MCP для `gate4ai mcp` (stdio): вызовы в daemon по сокету.
package mcpcli

import (
	"context"
	"net/url"

	"github.com/gate4ai/gate4ai/internal/front/cli"
	"github.com/gate4ai/gate4ai/internal/front/mcp"
)

// ClientBackend — бэкенд `gate4ai mcp`: вызовы идут в daemon по сокету с токеном агента.
type ClientBackend struct{ C *cli.Client }

// Tools — каталог агента.
func (b ClientBackend) Tools(ctx context.Context) ([]mcp.Tool, error) {
	var t []mcp.Tool
	err := b.C.Do(ctx, "GET", "/v1/tools", nil, &t)
	return t, err
}

// Call — вызов действия или статуса.
func (b ClientBackend) Call(ctx context.Context, action string, args map[string]any) (map[string]any, error) {
	var res map[string]any
	if action == "@status" {
		id, _ := args["id"].(string)
		wait, _ := args["wait"].(string)
		err := b.C.Do(ctx, "GET", "/v1/status/"+url.PathEscape(id)+"?wait="+url.QueryEscape(wait), nil, &res)
		if ae, ok := err.(*cli.APIError); ok {
			return map[string]any{"status": "error", "code": ae.Code, "message": ae.Message}, nil
		}
		return res, err
	}
	err := b.C.Do(ctx, "POST", "/v1/call", map[string]any{"action": action, "args": args}, &res)
	if ae, ok := err.(*cli.APIError); ok {
		return map[string]any{"status": "error", "code": ae.Code, "message": ae.Message}, nil
	}
	return res, err
}
