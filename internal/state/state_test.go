// SPDX-License-Identifier: FSL-1.1-ALv2

package state

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/internal/policy"
	"github.com/gate4ai/gate4ai/internal/secrets/keys"
)

func openT(t *testing.T) (*Store, string) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "state.db"), keys.NewMemory())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, dir
}

func TestCounters(t *testing.T) {
	s, _ := openT(t)
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.Local)
	s.Now = func() time.Time { return now.Add(-24 * time.Hour) }
	s.RecordAction("a", "payments.pay_bill", "money", 10000) // вчера
	s.Now = func() time.Time { return now }
	s.RecordAction("a", "payments.pay_bill", "money", 5000)
	s.RecordAction("b", "payments.transfer", "money", 700)
	s.RecordAction("a", "email.send", "external", 0)
	s.RecordAction("a", "email.send", "external", 0)
	c := s.Counters()
	if c.SpentToday() != 5700 {
		t.Fatal(c.SpentToday())
	}
	if c.CountToday("a", "email.send") != 2 || c.CountToday("a", "*") != 3 || c.AmountToday("a", "payments.*") != 5000 {
		t.Fatal("counts")
	}
	var _ policy.Counters = c
}

func TestTaint(t *testing.T) {
	s, _ := openT(t)
	s.AddTaint("email:<m1>", []string{" Evil@Attacker.test ", "IBAN123"})
	src, _ := s.TaintSources("evil@attacker.test")
	if len(src) != 1 || src[0] != "email:<m1>" {
		t.Fatal(src)
	}
	if src, _ := s.TaintSources("landlord@example.com"); len(src) != 0 {
		t.Fatal(src)
	}
}

func TestTokens(t *testing.T) {
	s, _ := openT(t)
	tok, err := s.IssueToken("claude-ai", "laptop", "")
	if err != nil {
		t.Fatal(err)
	}
	if a, err := s.AgentByToken(tok); err != nil || a != "claude-ai" {
		t.Fatal(a, err)
	}
	if _, err := s.AgentByToken("g4a_forged"); err != ErrBadToken {
		t.Fatal(err)
	}
	if n, _ := s.RevokeAgent("claude-ai"); n != 1 {
		t.Fatal("revoke")
	}
	if _, err := s.AgentByToken(tok); err != ErrBadToken {
		t.Fatal("revoked token accepted")
	}
	ts, _ := s.Tokens()
	if len(ts) != 1 || !ts[0].Revoked {
		t.Fatal(ts)
	}
}

func TestApprovalQueue(t *testing.T) {
	s, dir := openT(t)
	now := time.Now()
	s.Now = func() time.Time { return now }
	a := &Approval{Created: now, Expires: now.Add(time.Minute), Agent: "claude-ai", Action: "email.send", Class: "external",
		Status: Pending, Preview: "send to x", PreviewHash: "abc", Decision: policy.Decision{Effect: "ask", Reason: "default"},
		Args: map[string]any{"to": []any{"secret-recipient@example.com"}}}
	if err := s.PutApproval(a); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetApproval(a.ID)
	if err != nil || got.Args["to"].([]any)[0] != "secret-recipient@example.com" || got.Decision.Reason != "default" || got.Nonce == "" {
		t.Fatal(got, err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "state.db-wal"))
	if bytes.Contains(raw, []byte("secret-recipient")) {
		t.Fatal("args stored in plaintext")
	}
	if l, _ := s.ListApprovals(Pending, 0); len(l) != 1 {
		t.Fatal("list")
	}
	if _, err := s.Decide(a.ID, Approved, "telegram", "owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Decide(a.ID, Rejected, "desktop", "owner"); err != ErrAlreadyDecided {
		t.Fatal("double decision must fail", err)
	}
	if _, err := s.Decide("a_nope", Approved, "", ""); err != ErrNoApproval {
		t.Fatal(err)
	}
	s.SetResult(a.ID, map[string]any{"status": "ok"})
	s.MarkDone(a.ID)
	got, _ = s.GetApproval(a.ID)
	if got.Status != Done || string(got.Result) != `{"status":"ok"}` || got.Channel != "telegram" {
		t.Fatal(got)
	}
	b := &Approval{Created: now, Expires: now.Add(-time.Second), Agent: "x", Action: "y.z", Status: Pending, Args: map[string]any{}}
	s.PutApproval(b)
	if ex, _ := s.Expired(); len(ex) != 1 || ex[0].ID != b.ID {
		t.Fatal("expired")
	}
	s.Set("k", "v")
	if v, ok := s.Get("k"); !ok || v != "v" {
		t.Fatal("kv")
	}
}

func TestTokenKindsAndExpiry(t *testing.T) {
	s, _ := openT(t)
	now := time.Now()
	s.Now = func() time.Time { return now }
	acc, _ := s.IssueTokenTTL("a", "client1", "oauth", time.Hour)
	ref, _ := s.IssueTokenTTL("a", "client1", "refresh", 24*time.Hour)
	if a, err := s.AgentByToken(acc); err != nil || a != "a" {
		t.Fatal(err)
	}
	if _, err := s.AgentByToken(ref); err != ErrBadToken {
		t.Fatal("refresh token must not grant access")
	}
	if a, l, err := s.RefreshToken(ref); err != nil || a != "a" || l != "client1" {
		t.Fatal(err)
	}
	if _, _, err := s.RefreshToken(acc); err != ErrBadToken {
		t.Fatal("access token is not a refresh token")
	}
	now = now.Add(2 * time.Hour)
	if _, err := s.AgentByToken(acc); err != ErrBadToken {
		t.Fatal("expired token accepted")
	}
	if _, _, err := s.RefreshToken(ref); err != nil {
		t.Fatal(err)
	}
}
