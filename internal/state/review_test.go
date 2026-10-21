// SPDX-License-Identifier: AGPL-3.0-only

package state

import (
	"errors"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/internal/policy"
)

// Просроченный запрос нельзя одобрить или отклонить, только перевести в
// timeout (C.L1).
func TestDecideExpired(t *testing.T) {
	s, _ := openT(t)
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	a := &Approval{Created: now, Expires: now.Add(time.Minute), Agent: "a", Action: "x.y", Status: Pending, Args: map[string]any{}}
	if err := s.PutApproval(a); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	if _, err := s.Decide(a.ID, Approved, "telegram", "owner"); !errors.Is(err, ErrExpired) {
		t.Fatal(err)
	}
	if _, err := s.Decide(a.ID, Rejected, "telegram", "owner"); !errors.Is(err, ErrExpired) {
		t.Fatal(err)
	}
	if got, err := s.Decide(a.ID, Timeout, "timeout", "system"); err != nil || got.Status != Timeout {
		t.Fatal(got, err)
	}
	if _, err := s.Decide(a.ID, Approved, "telegram", "owner"); !errors.Is(err, ErrAlreadyDecided) {
		t.Fatal(err)
	}
}

// Ошибка чтения счётчиков видна PDP (C.M5); резерв лимита снимается (C.M4).
func TestCountersErrorAndReservation(t *testing.T) {
	s, _ := openT(t)
	id, err := s.ReserveAction("a", "email.send", "external", 0)
	if err != nil {
		t.Fatal(err)
	}
	if n := s.Counters().CountToday("a", "email.send"); n != 1 {
		t.Fatal(n)
	}
	if err := s.ReleaseAction(id); err != nil {
		t.Fatal(err)
	}
	c := s.Counters()
	if c.CountToday("a", "email.send") != 0 || c.(policy.CountersErr).Err() != nil {
		t.Fatal("release")
	}
	s.Close()
	c = s.Counters()
	c.SpentToday()
	if c.(policy.CountersErr).Err() == nil {
		t.Fatal("closed database must report an error, not zero counters")
	}
}

// Момент отзыва агента запоминается (H5).
func TestAgentRevokedAt(t *testing.T) {
	s, _ := openT(t)
	if at, err := s.AgentRevokedAt("a"); err != nil || !at.IsZero() {
		t.Fatal(at, err)
	}
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	s.RevokeAgent("a")
	if at, err := s.AgentRevokedAt("a"); err != nil || !at.Equal(now) {
		t.Fatal(at, err)
	}
}
