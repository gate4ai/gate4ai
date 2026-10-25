// SPDX-License-Identifier: AGPL-3.0-only

// Package audit — журнал действий: квитанции (JSON, RFC 8785), цепочка хэшей,
// SQLite-индекс с AEAD-шифрованием полей, доказательства (sha256 + age),
// подписанные чекпоинты Ed25519, экспорт и проверка, сроки хранения.
package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gowebpki/jcs"
)

// Receipt — квитанция на одно действие.
type Receipt struct {
	Seq   int64     `json:"seq"`
	ID    string    `json:"id"`
	TS    time.Time `json:"ts"`
	Agent string    `json:"agent"`
	// Caller — откуда агент вызвал действие: каталог и процессы.
	Caller *Caller `json:"caller,omitempty"`
	// Parent — квитанция действия, которое вызвало это через service_call.
	Parent string `json:"parent,omitempty"`
	// Intent — квитанция-намерение этого действия (execution.status "started"),
	// записанная до исполнения; итоговая квитанция ссылается на неё.
	Intent       string         `json:"intent,omitempty"`
	Action       string         `json:"action"`
	Class        string         `json:"class,omitempty"`
	Args         map[string]any `json:"args"` // после маскирования
	Decision     Decision       `json:"decision"`
	Approval     *Approval      `json:"approval,omitempty"`
	Execution    Execution      `json:"execution"`
	Verification Verification   `json:"verification"`
	PrevHash     string         `json:"prev_hash"`
}

// Caller — откуда пришёл вызов агента. Локально (сокет) daemon узнаёт его
// сам по процессу-собеседнику (source "peer"); удалённый CLI сообщает каталог
// сам (source "reported").
type Caller struct {
	Kind    string       `json:"kind,omitempty"` // тип агента: claude-code, codex…
	Dir     string       `json:"dir,omitempty"`  // каталог запуска агента
	Source  string       `json:"source"`         // peer | reported
	Session string       `json:"session,omitempty"`
	Chain   []CallerProc `json:"chain,omitempty"` // от собеседника до корня агента
}

// CallerProc — процесс в цепочке вызывающего.
type CallerProc struct {
	PID  int    `json:"pid"`
	Name string `json:"name,omitempty"`
	Exe  string `json:"exe,omitempty"`
	Cwd  string `json:"cwd,omitempty"`
}

// Decision — решение PDP.
type Decision struct {
	Effect  string   `json:"effect"`
	RuleIDs []string `json:"rule_ids"`
	Reason  string   `json:"reason"`
	// Errors — ошибки вычисления условий правил (решено в пользу строгости).
	Errors []string `json:"errors,omitempty"`
}

// Approval — подтверждение человеком.
type Approval struct {
	Channel     string    `json:"channel"`
	Who         string    `json:"who"`
	At          time.Time `json:"at"`
	Result      string    `json:"result"` // approved | rejected | timeout
	PreviewHash string    `json:"hash"`   // хэш превью, показанного человеку
}

// Статусы исполнения квитанции-намерения и прерванного действия.
const (
	ExecStarted     = "started"     // намерение: записано до исполнения
	ExecInterrupted = "interrupted" // daemon остановился во время исполнения
)

// Execution — результат исполнения.
type Execution struct {
	Status    string `json:"status"` // ok | error | invalid_result | skipped | started | interrupted
	ResultRef string `json:"result_ref,omitempty"`
	// Page — адрес страницы, на которой выполнено действие (Браузер).
	Page  string `json:"page,omitempty"`
	Error string `json:"error,omitempty"`
}

// Verification — проверка результата по независимому каналу.
type Verification struct {
	Method       string   `json:"method,omitempty"`
	Status       string   `json:"status"` // verified | not_confirmed | pending | unverified | not_applicable
	EvidenceRefs []string `json:"evidence_refs"`
}

// Canonical — каноническое представление RFC 8785.
func (r *Receipt) Canonical() ([]byte, error) {
	if r.Decision.RuleIDs == nil {
		r.Decision.RuleIDs = []string{}
	}
	if r.Verification.EvidenceRefs == nil {
		r.Verification.EvidenceRefs = []string{}
	}
	if r.Args == nil {
		r.Args = map[string]any{}
	}
	return Canonicalize(r)
}

// ReceiptFormat — формат новых квитанций: правила, по которым считается хэш.
// Формат 1 — sha256 канонического JSON (RFC 8785) записанного документа
// квитанции. Правила выпущенного формата не меняются: другой способ хэширования
// или сериализации — новый номер формата, а записи прежних форматов
// проверяются по своим правилам (formatHash).
const ReceiptFormat = 1

// formatHash — хэш записанного документа квитанции по правилам её формата.
// Проверка идёт по самому документу, а не по его пересборке из Receipt:
// новые поля структуры не меняют хэш старых записей.
func formatHash(format int, doc []byte) (string, error) {
	switch format {
	case 1:
		b, err := jcs.Transform(doc)
		if err != nil {
			return "", err
		}
		return HashBytes(b), nil
	}
	return "", fmt.Errorf("receipt format %d is not supported by this gate4ai; update gate4ai", format)
}

// Hash — sha256 канонического JSON в hex.
func (r *Receipt) Hash() (string, error) {
	b, err := r.Canonical()
	if err != nil {
		return "", err
	}
	return HashBytes(b), nil
}

// HeaderHash — хэш заголовка квитанции: то, что остаётся в индексе после
// редактирования (seq, id, ts, agent, action, class, effect, prev_hash).
// Запись о редактировании (journal.redact) хранит его, поэтому подмена
// метаданных отредактированной квитанции видна при проверке.
func HeaderHash(r Receipt) string {
	b, _ := Canonicalize(map[string]any{"seq": r.Seq, "id": r.ID, "ts": r.TS.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano),
		"agent": r.Agent, "action": r.Action, "class": r.Class, "effect": r.Decision.Effect, "prev_hash": r.PrevHash})
	return HashBytes(b)
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
