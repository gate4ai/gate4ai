// SPDX-License-Identifier: AGPL-3.0-only

package approval

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/internal/pipeline"
	"github.com/gate4ai/gate4ai/internal/secrets/keys"
	"github.com/gate4ai/gate4ai/internal/state"
)

// blockingDecider — исполнение подтверждённого действия «длится», пока
// не закрыт release.
type blockingDecider struct {
	release chan struct{}
	async   chan string
}

func (b *blockingDecider) Decide(ctx context.Context, id string, approve bool, channel, who string) (pipeline.Result, error) {
	<-b.release
	return pipeline.Result{Status: "ok"}, nil
}

func (b *blockingDecider) DecideAsync(ctx context.Context, id string, approve bool, channel, who string) error {
	b.async <- id
	go func() { <-b.release }()
	return nil
}

func openState(t *testing.T) *state.Store {
	t.Helper()
	st, err := state.Open(filepath.Join(t.TempDir(), "state.db"), keys.NewMemory())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// Нажатие «Разрешить» не держит цикл опроса Telegram на время исполнения
// действия (C.M3): бот сразу отвечает и принимает следующие обновления.
func TestTelegramApproveDoesNotBlockPolling(t *testing.T) {
	st := openState(t)
	a := &state.Approval{ID: "a_1", Created: time.Now(), Expires: time.Now().Add(time.Hour), Agent: "x", Action: "browser.fill_secret",
		Status: state.Pending, Args: map[string]any{}}
	if err := st.PutApproval(a); err != nil {
		t.Fatal(err)
	}
	fake := &fakeTelegram{token: "1:T"}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	dec := &blockingDecider{release: make(chan struct{}), async: make(chan string, 1)}
	defer close(dec.release)
	tg := NewTelegram(&BotAPI{Base: srv.URL, Token: "1:T"}, 7, dec, st)
	done := make(chan struct{})
	go func() { tg.Handle(context.Background(), cbUpdate(1, 7, "y:a_1:"+sig(a))); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("telegram handler blocked by the running action")
	}
	if id := <-dec.async; id != "a_1" {
		t.Fatal(id)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.answers) != 1 || fake.answers[0] != "Готово" {
		t.Fatal(fake.answers)
	}
}

// «Показать текст» длиннее лимита Telegram обрезается с пометкой (C.L5).
func TestTelegramPreviewTruncated(t *testing.T) {
	a := &state.Approval{Preview: strings.Repeat("я", 5000), PreviewHash: "a3f9c21e00"}
	got := previewText(a)
	if n := len([]rune(got)); n > 4096 || !strings.Contains(got, "gate4ai show #a3f9c21e") {
		t.Fatal(n, got[len(got)-100:])
	}
	short := &state.Approval{Preview: "short"}
	if previewText(short) != "short" {
		t.Fatal("short preview changed")
	}
}

type slowNotifier struct{ delay time.Duration }

func (s slowNotifier) Notify(*state.Approval)        { time.Sleep(s.delay) }
func (s slowNotifier) Resolved(*state.Approval)      { time.Sleep(s.delay) }
func (s slowNotifier) Denied(string, string, string) { time.Sleep(s.delay) }

// Медленный канал не задерживает вызов агента (C.L3): событие ставится в
// очередь канала.
func TestSlowChannelDoesNotBlock(t *testing.T) {
	sw := &switchable{}
	sw.set(slowNotifier{delay: 300 * time.Millisecond})
	start := time.Now()
	sw.Notify(&state.Approval{})
	sw.Denied("a", "b", "c")
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("notify blocked the caller")
	}
	sw.wait()
	if time.Since(start) < 600*time.Millisecond {
		t.Fatal("events must be delivered in order, one by one")
	}
}
