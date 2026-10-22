// SPDX-License-Identifier: AGPL-3.0-only

// Package vault — хранилище секретов: системный keychain, агент оперирует
// алиасами (@amazon, @mail.work), подстановка — только внутри коннектора;
// ответы агенту и журнал проходят фильтр маскирования.
package vault

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gate4ai/gate4ai/internal/secrets/keys"
)

const prefix = "vault/"
const indexKey = "vault.index"

var aliasRe = regexp.MustCompile(`^@?[a-z0-9][a-z0-9._-]{0,63}$`)

// Vault — секреты по алиасам.
type Vault struct {
	ks keys.Store
	mu sync.RWMutex
	// значения в памяти нужны фильтру маскирования
	values map[string]string
	// names — алиасы индекса; значение алиаса может быть не загружено, если
	// keychain временно не отдал его (тогда Get пробует снова). Индекс пишется
	// из names, а не из values: временная ошибка не выкидывает алиас навсегда.
	names map[string]bool
	// transient — значения не из хранилища (код, который человек ввёл для
	// входа), которые маскируются до срока.
	transient map[string]time.Time
}

// MaskAlso добавляет значение в маскирование на срок ttl (код от человека для
// browser.fill_secret: в хранилище его нет, но в ответах его быть не должно).
func (v *Vault) MaskAlso(value string, ttl time.Duration) {
	if value == "" {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.transient == nil {
		v.transient = map[string]time.Time{}
	}
	now := time.Now()
	for s, until := range v.transient {
		if now.After(until) {
			delete(v.transient, s)
		}
	}
	v.transient[value] = now.Add(ttl)
}

// Open загружает индекс алиасов из keychain. Значение, которое не прочиталось
// (кроме «нет такого»), остаётся в индексе и читается позже, при Get.
func Open(ks keys.Store) (*Vault, error) {
	v := &Vault{ks: ks, values: map[string]string{}, names: map[string]bool{}}
	idx, err := ks.Get(indexKey)
	if errors.Is(err, keys.ErrNotFound) {
		return v, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	if err := json.Unmarshal([]byte(idx), &names); err != nil {
		return nil, fmt.Errorf("vault index: %w", err)
	}
	for _, n := range names {
		val, err := ks.Get(prefix + n)
		switch {
		case err == nil:
			v.values[n], v.names[n] = val, true
		case !errors.Is(err, keys.ErrNotFound):
			v.names[n] = true
		}
	}
	return v, nil
}

// Norm приводит алиас к виду без "@".
func Norm(alias string) (string, error) {
	if !aliasRe.MatchString(alias) {
		return "", fmt.Errorf("invalid alias %q (expected @name, a-z 0-9 . _ -)", alias)
	}
	return strings.TrimPrefix(alias, "@"), nil
}

// saveIndex пишет индекс: отсортированный JSON-массив имён, пустой — `[]`,
// не `null` (slices.Sorted на пустом наборе дал бы nil).
func (v *Vault) saveIndex() error {
	names := slices.AppendSeq(make([]string, 0, len(v.names)), maps.Keys(v.names))
	slices.Sort(names)
	b, _ := json.Marshal(names)
	return v.ks.Set(indexKey, string(b))
}

// Set сохраняет секрет.
func (v *Vault) Set(alias, value string) error {
	n, err := Norm(alias)
	if err != nil {
		return err
	}
	if value == "" {
		return errors.New("empty secret")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if err := v.ks.Set(prefix+n, value); err != nil {
		return err
	}
	v.values[n], v.names[n] = value, true
	return v.saveIndex()
}

// Delete удаляет секрет.
func (v *Vault) Delete(alias string) error {
	n, err := Norm(alias)
	if err != nil {
		return err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if err := v.ks.Delete(prefix + n); err != nil && !errors.Is(err, keys.ErrNotFound) {
		return err
	}
	delete(v.values, n)
	delete(v.names, n)
	return v.saveIndex()
}

// ErrNoSecret — алиас не задан.
var ErrNoSecret = errors.New("secret not set")

// Get — значение секрета (только для коннекторов и каналов daemon).
func (v *Vault) Get(alias string) (string, error) {
	n, err := Norm(alias)
	if err != nil {
		return "", err
	}
	v.mu.RLock()
	val, ok := v.values[n]
	indexed := v.names[n]
	v.mu.RUnlock()
	if ok {
		return val, nil
	}
	if indexed {
		// Не прочиталось при открытии: keychain мог быть временно недоступен.
		val, err := v.ks.Get(prefix + n)
		if err != nil {
			return "", fmt.Errorf("secret @%s: keychain: %w", n, err)
		}
		v.mu.Lock()
		v.values[n] = val
		v.mu.Unlock()
		return val, nil
	}
	return "", fmt.Errorf("%w: @%s (set it in Web UI or with `gate4ai vault set %s`)", ErrNoSecret, n, n)
}

// Aliases — имена алиасов (без значений).
func (v *Vault) Aliases() []string {
	v.mu.RLock()
	defer v.mu.RUnlock()
	out := make([]string, 0, len(v.names))
	for n := range v.names {
		out = append(out, "@"+n)
	}
	slices.Sort(out)
	return out
}
