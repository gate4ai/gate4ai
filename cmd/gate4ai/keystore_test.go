// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gate4ai/gate4ai/internal/secrets/keys"
)

// keystore auto не переключается молча на файл, если установка уже хранит
// ключи в keychain (A.M4): иначе новые ключи, нечитаемые базы, пустой Vault.
func TestOpenKeysAuto(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mem := keys.NewMemory()
	keychainOK := true
	oldNew, oldProbe := newKeyring, probeKeyring
	defer func() { newKeyring, probeKeyring = oldNew, oldProbe }()
	newKeyring = func() keys.Store { return mem }
	probeKeyring = func(keys.Store) error {
		if keychainOK {
			return nil
		}
		return errors.New("dbus not ready")
	}
	isFile := func(s keys.Store) bool { _, ok := s.(*keys.File); return ok }
	marker := func(home string) string {
		b, _ := os.ReadFile(filepath.Join(home, keystoreMarker))
		return strings.TrimSpace(string(b))
	}

	// Свежая установка, keychain есть → keychain, запомнен.
	home := t.TempDir()
	if s, err := openKeys("auto", home, log); err != nil || isFile(s) || marker(home) != "keychain" {
		t.Fatal(s, err, marker(home))
	}
	// Потом keychain недоступен → ошибка с подсказкой, не файл.
	keychainOK = false
	if _, err := openKeys("auto", home, log); err == nil || !strings.Contains(err.Error(), "--keystore file") {
		t.Fatal("must not fall back to the file:", err)
	}
	// Явный выбор файла работает, но маркер не меняет.
	if s, err := openKeys("file", home, log); err != nil || !isFile(s) || marker(home) != "keychain" {
		t.Fatal(s, err, marker(home))
	}

	// Старая установка без маркера: базы есть, keys.json нет → ошибка.
	old := t.TempDir()
	_ = os.WriteFile(filepath.Join(old, "state.db"), []byte("x"), 0o600)
	if _, err := openKeys("auto", old, log); err == nil {
		t.Fatal("existing databases without keys.json must not switch to the file")
	}

	// Свежая установка без keychain → файл, запомнен; и дальше файл, даже когда keychain вернулся.
	fresh := t.TempDir()
	if s, err := openKeys("auto", fresh, log); err != nil || !isFile(s) || marker(fresh) != "file" {
		t.Fatal(s, err, marker(fresh))
	}
	keychainOK = true
	if s, err := openKeys("auto", fresh, log); err != nil || !isFile(s) {
		t.Fatal("file installation must stay on the file", s, err)
	}
}
