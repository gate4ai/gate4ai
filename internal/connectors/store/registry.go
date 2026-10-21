// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gate4ai/sdk-go/registry"
)

// TrustLocal — пакет владельца из файла (--dev). Уровни доверия издателей
// registry — в sdk-go/registry.
const TrustLocal = "local"

// fetch читает путь или http(s)-URL.
func fetch(ctx context.Context, src string) ([]byte, error) { return fetchN(ctx, src, maxPackage) }

// isURL — источник по адресу http(s).
func isURL(src string) bool {
	return strings.HasPrefix(src, "https://") || strings.HasPrefix(src, "http://")
}

// fetchN — fetch с пределом размера ответа (больше предела — ошибку даст Open).
func fetchN(ctx context.Context, src string, limit int64) ([]byte, error) {
	if isURL(src) {
		ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, "GET", src, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("GET %s: %s", src, resp.Status)
		}
		return io.ReadAll(io.LimitReader(resp.Body, limit+1))
	}
	return os.ReadFile(src)
}

// Fetch читает путь или http(s)-URL (пакет, список платформ).
func Fetch(ctx context.Context, src string) ([]byte, error) { return fetch(ctx, src) }

// ResolveRef — ссылка ref относительно base (адреса индекса или списка платформ).
func ResolveRef(base, ref string) string { return resolveRef(base, ref) }

// resolveRef — ссылка относительно индекса.
func resolveRef(base, ref string) string {
	if strings.Contains(ref, "://") || filepath.IsAbs(ref) {
		return ref
	}
	if u, err := url.Parse(base); err == nil && (u.Scheme == "https" || u.Scheme == "http") {
		r, _ := url.Parse(ref)
		return u.ResolveReference(r).String()
	}
	return filepath.Join(filepath.Dir(base), ref)
}

// IndexData — индекс и подпись: данные для подписи — сам файл index.json.
func IndexData(ctx context.Context, src string) (raw []byte, sig string, err error) {
	if raw, err = fetch(ctx, src); err != nil {
		return nil, "", fmt.Errorf("registry index: %w", err)
	}
	s, err := fetch(ctx, src+".sig")
	if err != nil {
		return nil, "", fmt.Errorf("registry index signature: %w", err)
	}
	return raw, strings.TrimSpace(string(s)), nil
}

// LoadIndex загружает индекс и проверяет подпись ключами registry.
func LoadIndex(ctx context.Context, src string, keys []string) (*registry.Index, []byte, error) {
	raw, sig, err := IndexData(ctx, src)
	if err != nil {
		return nil, nil, err
	}
	ix, err := registry.ParseIndex(raw, sig, keys)
	if errors.Is(err, registry.ErrNoKeys) || errors.Is(err, registry.ErrBadSignature) {
		err = fmt.Errorf("%w (store.registry_keys)", err)
	}
	return ix, raw, err
}
