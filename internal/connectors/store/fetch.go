// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"fmt"

	"github.com/gate4ai/gate4ai/sdk-go/registry"
)

// FetchRelease скачивает и проверяет версию из индекса (base — адрес индекса).
func FetchRelease(ctx context.Context, base, name, publisher string, rel registry.Release, pub registry.Publisher) (*Package, error) {
	ref := ResolveRef(base, rel.URL)
	b, err := Fetch(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", ref, err)
	}
	pkg, err := CheckRelease(b, name, publisher, rel, pub)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ref, err)
	}
	return pkg, nil
}
