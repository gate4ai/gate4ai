// SPDX-License-Identifier: FSL-1.1-ALv2

package version

import "testing"

func TestVersionSet(t *testing.T) {
	if Version == "" {
		t.Fatal("version must not be empty")
	}
}
