// SPDX-License-Identifier: AGPL-3.0-only

package state

import (
	"errors"
	"fmt"
	"regexp"
	"testing"
	"time"
)

func TestPairing(t *testing.T) {
	s, _ := openT(t)
	now := time.Now()
	s.Now = func() time.Time { return now }
	key := "g4a_agentmadekey"
	p, err := s.PutPairing(&Pairing{Agent: "claude-code", Kind: "claude-code", KeyHash: HashToken(key), Via: "agent-socket"})
	if err != nil || !regexp.MustCompile(`^[A-Z2-9]{3}-[A-Z2-9]{3}$`).MatchString(p.Code) || p.Status != Pending {
		t.Fatalf("put: %+v %v", p, err)
	}
	// Повтор с тем же ключом — тот же запрос и код.
	if again, err := s.PutPairing(&Pairing{Agent: "claude-code", KeyHash: HashToken(key)}); err != nil || again.ID != p.ID || again.Code != p.Code {
		t.Fatalf("repeat: %+v %v", again, err)
	}
	// До решения ключ — не токен.
	if _, err := s.AgentByToken(key); err == nil {
		t.Fatal("pending key must not work")
	}
	if list, _ := s.PendingPairings(); len(list) != 1 {
		t.Fatalf("pending: %v", list)
	}
	if _, err := s.DecidePairing(p.ID, true, "webui"); err != nil {
		t.Fatal(err)
	}
	if a, err := s.AgentByToken(key); err != nil || a != "claude-code" || s.TokenDir(key) != "" {
		t.Fatalf("approved key: %q %v", a, err)
	}
	if _, err := s.DecidePairing(p.ID, false, "webui"); !errors.Is(err, ErrAlreadyDecided) {
		t.Fatalf("second decision: %v", err)
	}
	// Тот же ключ второй раз не спаривается.
	if _, err := s.PutPairing(&Pairing{Agent: "x", KeyHash: HashToken(key)}); err == nil {
		t.Fatal("used key accepted")
	}

	// Ключ с привязкой к каталогу.
	k2 := "g4a_second"
	p2, _ := s.PutPairing(&Pairing{Agent: "claude-code-proj", KeyHash: HashToken(k2), Dir: "/home/u/proj"})
	if _, err := s.DecidePairing(p2.ID, true, "telegram"); err != nil {
		t.Fatal(err)
	}
	if s.TokenDir(k2) != "/home/u/proj" {
		t.Fatal("dir binding")
	}
	toks, _ := s.Tokens()
	if len(toks) != 2 || toks[1].Dir != "/home/u/proj" || toks[1].Label != "paired:/home/u/proj" {
		t.Fatalf("tokens: %+v", toks)
	}

	// Отклонённый — не токен; просроченный не одобряется.
	k3 := "g4a_third"
	p3, _ := s.PutPairing(&Pairing{Agent: "codex", KeyHash: HashToken(k3)})
	if _, err := s.DecidePairing(p3.ID, false, "webui"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AgentByToken(k3); err == nil {
		t.Fatal("rejected key works")
	}
	p4, _ := s.PutPairing(&Pairing{Agent: "codex", KeyHash: HashToken("g4a_4")})
	s.Now = func() time.Time { return now.Add(PairingTTL + time.Second) }
	if _, err := s.DecidePairing(p4.ID, true, "webui"); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired: %v", err)
	}
	if g, _ := s.GetPairing(p4.ID); g.Status != Timeout {
		t.Fatalf("status %s", g.Status)
	}
}

func TestPairingLimit(t *testing.T) {
	s, _ := openT(t)
	for i := 0; i < MaxPendingPair; i++ {
		if _, err := s.PutPairing(&Pairing{Agent: "a", KeyHash: HashToken(fmt.Sprint("k", i))}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.PutPairing(&Pairing{Agent: "a", KeyHash: HashToken("one more")}); !errors.Is(err, ErrTooManyPairings) {
		t.Fatalf("limit: %v", err)
	}
	if _, err := s.PutPairing(&Pairing{Agent: "a", KeyHash: HashToken("local"), Via: "agent-socket"}); err != nil {
		t.Fatalf("the limit is per channel: %v", err)
	}
}
