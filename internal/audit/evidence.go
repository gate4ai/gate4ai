// SPDX-License-Identifier: AGPL-3.0-only

package audit

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"filippo.io/age"

	"github.com/gate4ai/gate4ai/internal/secrets/keys"
)

// Evidence — хранилище доказательств: файл с именем по sha256 содержимого,
// зашифрованный age. Квитанция хранит только "sha256:<hex>", поэтому удаление
// файла (crypto-shredding) не ломает цепочку.
type Evidence struct {
	dir string
	id  *age.X25519Identity
}

// OpenEvidence открывает хранилище; идентичность age хранится в keychain.
func OpenEvidence(dir string, ks keys.Store) (*Evidence, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	var id *age.X25519Identity
	s, err := ks.Get("audit.age")
	switch {
	case err == nil:
		id, err = age.ParseX25519Identity(s)
		if err != nil {
			return nil, fmt.Errorf("audit.age: %w", err)
		}
	case errors.Is(err, keys.ErrNotFound):
		id, err = age.GenerateX25519Identity()
		if err != nil {
			return nil, err
		}
		if err := ks.Set("audit.age", id.String()); err != nil {
			return nil, err
		}
	default:
		return nil, err
	}
	return &Evidence{dir: dir, id: id}, nil
}

// Ref — ссылка на доказательство.
func Ref(sum string) string { return "sha256:" + sum }

func (e *Evidence) path(ref string) (string, error) {
	sum, ok := strings.CutPrefix(ref, "sha256:")
	if !ok || len(sum) != 64 || strings.Trim(sum, "0123456789abcdef") != "" {
		return "", fmt.Errorf("bad evidence ref %q", ref)
	}
	return filepath.Join(e.dir, sum+".age"), nil
}

// Put шифрует и сохраняет доказательство; возвращает ссылку "sha256:…".
func (e *Evidence) Put(data []byte) (string, error) {
	ref := Ref(HashBytes(data))
	p, _ := e.path(ref)
	if _, err := os.Stat(p); err == nil {
		return ref, nil
	}
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, e.id.Recipient())
	if err != nil {
		return "", err
	}
	if _, err := w.Write(data); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	if err := writeFile0600(p, buf.Bytes()); err != nil {
		return "", err
	}
	return ref, nil
}

// ErrShredded — доказательство удалено (срок хранения или crypto-shredding).
var ErrShredded = errors.New("evidence deleted")

// Get расшифровывает доказательство и проверяет хэш.
func (e *Evidence) Get(ref string) ([]byte, error) {
	p, err := e.path(ref)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrShredded
	} else if err != nil {
		return nil, err
	}
	defer f.Close()
	r, err := age.Decrypt(f, e.id)
	if err != nil {
		return nil, err
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if Ref(HashBytes(b)) != ref {
		return nil, fmt.Errorf("evidence %s: hash mismatch", ref)
	}
	return b, nil
}

// Delete удаляет доказательство.
func (e *Evidence) Delete(ref string) error {
	p, err := e.path(ref)
	if err != nil {
		return err
	}
	err = os.Remove(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func writeFile0600(p string, b []byte) error {
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
