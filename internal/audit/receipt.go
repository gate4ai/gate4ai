// SPDX-License-Identifier: AGPL-3.0-only

// Package audit — журнал действий: квитанции (JSON, RFC 8785), цепочка хэшей,
// SQLite-индекс с AEAD-шифрованием полей, доказательства (sha256 + age),
// подписанные чекпоинты Ed25519, экспорт и проверка, сроки хранения.
package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/gowebpki/jcs"
)

// Receipt — квитанция на одно действие.
type Receipt struct {
	Seq   int64     `json:"seq"`
	ID    string    `json:"id"`
	TS    time.Time `json:"ts"`
	Agent string    `json:"agent"`
	// Parent — квитанция действия, которое вызвало это через service_call.
	Parent       string         `json:"parent,omitempty"`
	Action       string         `json:"action"`
	Class        string         `json:"class,omitempty"`
	Args         map[string]any `json:"args"` // после маскирования
	Decision     Decision       `json:"decision"`
	Approval     *Approval      `json:"approval,omitempty"`
	Execution    Execution      `json:"execution"`
	Verification Verification   `json:"verification"`
	PrevHash     string         `json:"prev_hash"`
}

// Decision — решение PDP.
type Decision struct {
	Effect  string   `json:"effect"`
	RuleIDs []string `json:"rule_ids"`
	Reason  string   `json:"reason"`
}

// Approval — подтверждение человеком.
type Approval struct {
	Channel     string    `json:"channel"`
	Who         string    `json:"who"`
	At          time.Time `json:"at"`
	Result      string    `json:"result"` // approved | rejected | timeout
	PreviewHash string    `json:"hash"`   // хэш превью, показанного человеку
}

// Execution — результат исполнения.
type Execution struct {
	Status    string `json:"status"` // ok | error | invalid_result | skipped
	ResultRef string `json:"result_ref,omitempty"`
	Error     string `json:"error,omitempty"`
}

// Verification — проверка результата по независимому каналу.
type Verification struct {
	Method       string   `json:"method,omitempty"`
	Status       string   `json:"status"` // verified | not_confirmed | pending | unverified | not_applicable
	EvidenceRefs []string `json:"evidence_refs"`
}

// Canonical — каноническое представление RFC 8785.
func (r *Receipt) Canonical() ([]byte, error) {
	if r.RuleIDsNil() {
		r.Decision.RuleIDs = []string{}
	}
	if r.Verification.EvidenceRefs == nil {
		r.Verification.EvidenceRefs = []string{}
	}
	if r.Args == nil {
		r.Args = map[string]any{}
	}
	b, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	return jcs.Transform(b)
}

// RuleIDsNil — rule_ids не задан.
func (r *Receipt) RuleIDsNil() bool { return r.Decision.RuleIDs == nil }

// Hash — sha256 канонического JSON в hex.
func (r *Receipt) Hash() (string, error) {
	b, err := r.Canonical()
	if err != nil {
		return "", err
	}
	return HashBytes(b), nil
}

// HashBytes — sha256 в hex.
func HashBytes(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// Short — краткий хэш для Telegram и уведомлений: "#a3f9c21e".
func Short(hash string) string {
	if len(hash) > 8 {
		hash = hash[:8]
	}
	return "#" + hash
}

// Canonicalize — RFC 8785 для произвольного значения.
func Canonicalize(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return jcs.Transform(b)
}
