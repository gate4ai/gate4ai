// SPDX-License-Identifier: FSL-1.1-ALv2

// Package auth описывает, кто обращается к daemon: владелец или агент.
package auth

import "errors"

// Kind — тип собеседника.
type Kind int

const (
	// Anonymous — собеседник не опознан.
	Anonymous Kind = iota
	// Owner — человек-владелец устройства (проверка собеседника по сокету или сессия Web UI).
	Owner
	// Agent — агент со своим токеном; прав владельца не получает никогда.
	Agent
)

// Principal — опознанный собеседник.
type Principal struct {
	Kind  Kind
	Agent string // имя агента для Kind == Agent
	Via   string // канал: "socket", "agent-socket", "webui", "mcp-http", "mcp-stdio", "https" (API агента на публичном адресе)
}

// ErrNotOwner — операция доступна только владельцу.
var ErrNotOwner = errors.New("owner authentication required")

// IsOwner сообщает, аутентифицирован ли владелец.
func (p Principal) IsOwner() bool { return p.Kind == Owner }

// RequireOwner возвращает ErrNotOwner, если p — не владелец.
func (p Principal) RequireOwner() error {
	if !p.IsOwner() {
		return ErrNotOwner
	}
	return nil
}

// OwnerVia — владелец, пришедший по каналу via.
func OwnerVia(via string) Principal { return Principal{Kind: Owner, Via: via} }

// AgentVia — агент name, пришедший по каналу via.
func AgentVia(name, via string) Principal { return Principal{Kind: Agent, Agent: name, Via: via} }
