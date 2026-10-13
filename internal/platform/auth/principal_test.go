// SPDX-License-Identifier: FSL-1.1-ALv2

package auth

import "testing"

func TestRequireOwner(t *testing.T) {
	if err := OwnerVia("socket").RequireOwner(); err != nil {
		t.Fatal(err)
	}
	if err := AgentVia("claude-ai", "socket").RequireOwner(); err != ErrNotOwner {
		t.Fatalf("agent must not be owner: %v", err)
	}
	if err := (Principal{}).RequireOwner(); err != ErrNotOwner {
		t.Fatalf("anonymous must not be owner: %v", err)
	}
}
