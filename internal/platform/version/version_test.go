// SPDX-License-Identifier: AGPL-3.0-only

package version

import "testing"

func TestVersionSet(t *testing.T) {
	if Version == "" {
		t.Fatal("version must not be empty")
	}
}
