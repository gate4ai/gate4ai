// SPDX-License-Identifier: AGPL-3.0-only

package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gate4ai/gate4ai/internal/pipeline"
)

// Question — вопрос человеку с вводом значения (код из SMS для входа на сайт,
// browser.fill_secret с source: human). Ответ идёт сразу в поле страницы:
// агенту, коннектору и в журнал он не попадает.
type Question struct {
	ID string `json:"id"`
	pipeline.Question
	Created time.Time `json:"created"`
	Expires time.Time `json:"expires"`
}

type questions struct {
	mu sync.Mutex
	m  map[string]*waiting
}

type waiting struct {
	q   Question
	ans chan string
}

// Asker — канал, который умеет показать вопрос с вводом (необязательно для
// Notifier): id для ответа и текст вопроса.
type Asker interface {
	Asked(id, text string)
}

// Prompt — вопрос для человека: кто спрашивает, где вход и что ввести.
func (q Question) Prompt() string {
	who := q.Agent
	if q.Caller != "" {
		who = q.Caller + " (" + q.Agent + ")"
	}
	return who + " signs in on " + strings.Join(q.Bind, ", ") + " with " + q.Account + " and asks: " + q.Question.Text
}

// ErrNoQuestion — вопроса нет (ответили, отменён или истёк срок).
var ErrNoQuestion = errors.New("question not found or already answered")

// QuestionTTL — сколько ответ человека остаётся в маскировании ответов.
const QuestionTTL = 30 * time.Minute

// ask задаёт вопрос и ждёт ответа до конца ctx (pipeline.Pipeline.Ask).
func (d *Daemon) ask(ctx context.Context, q pipeline.Question) (string, error) {
	var raw [9]byte
	_, _ = rand.Read(raw[:])
	w := &waiting{q: Question{ID: "q_" + hex.EncodeToString(raw[:]), Question: q, Created: time.Now()}, ans: make(chan string, 1)}
	if dl, ok := ctx.Deadline(); ok {
		w.q.Expires = dl
	}
	d.questions.mu.Lock()
	if d.questions.m == nil {
		d.questions.m = map[string]*waiting{}
	}
	d.questions.m[w.q.ID] = w
	d.questions.mu.Unlock()
	defer func() {
		d.questions.mu.Lock()
		delete(d.questions.m, w.q.ID)
		d.questions.mu.Unlock()
	}()
	d.notifiers.each(func(n pipeline.Notifier) {
		if a, ok := n.(Asker); ok {
			a.Asked(w.q.ID, w.q.Prompt())
		}
	})
	d.Log.Info("question for the user", "id", w.q.ID, "agent", q.Agent, "connector", q.Caller, "account", q.Account)
	select {
	case v := <-w.ans:
		if v == "" {
			return "", errors.New("the user declined")
		}
		return v, nil
	case <-ctx.Done():
		return "", errors.New("no answer in time")
	}
}

// Questions — вопросы, ждущие ответа (без ответов).
func (d *Daemon) Questions() []Question {
	d.questions.mu.Lock()
	defer d.questions.mu.Unlock()
	out := make([]Question, 0, len(d.questions.m))
	for _, w := range d.questions.m {
		out = append(out, w.q)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}

// Answer — ответ владельца на вопрос; пустое значение — отказ. Значение
// маскируется в ответах QuestionTTL.
func (d *Daemon) Answer(id, value string) error {
	value = strings.TrimRight(value, "\r\n")
	d.questions.mu.Lock()
	w := d.questions.m[id]
	delete(d.questions.m, id)
	d.questions.mu.Unlock()
	if w == nil {
		return ErrNoQuestion
	}
	if value != "" {
		d.Vault.MaskAlso(value, QuestionTTL)
	}
	w.ans <- value
	return nil
}
