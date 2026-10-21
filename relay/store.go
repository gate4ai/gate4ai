// SPDX-License-Identifier: AGPL-3.0-only

package relay

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"time"
)

// Store — состояние relay: устройства, TXT-записи ACME, чекпоинты-свидетельства.
type Store struct {
	path string
	mu   sync.Mutex
	data storeData
}

type storeData struct {
	Devices     map[string]string                `json:"devices"` // device id → sha256(token)
	TXT         map[string][]string              `json:"txt"`     // fqdn → значения
	Checkpoints map[string][]WitnessedCheckpoint `json:"checkpoints"`
	// Stats — счётчики для метрик; переживают перезапуск relay.
	Stats Stats `json:"stats"`
}

// Stats — накопленные с начала учёта регистрации устройств и заказы
// сертификатов, плюс время последних событий (окно StatsWindow) для метрик
// «за последние 7 дней».
type Stats struct {
	Registrations int64                `json:"registrations"`
	CertOrders    int64                `json:"cert_orders"`
	RegisteredAt  []time.Time          `json:"registered_at,omitempty"`
	CertOrderedAt []time.Time          `json:"cert_ordered_at,omitempty"`
	LastCertOrder map[string]time.Time `json:"last_cert_order,omitempty"` // device id → последний заказ
}

// StatsWindow — сколько хранятся времена событий; метрики считают окно 7 дней.
const StatsWindow = 8 * 24 * time.Hour

// certOrderGap — TXT, поставленные устройством чаще этого, относятся к одному
// заказу сертификата (несколько авторизаций, повторы проверки).
const certOrderGap = 10 * time.Minute

// now — часы store (подменяются в тестах).
var now = time.Now

// WitnessedCheckpoint — чекпоинт, принятый свидетелем.
type WitnessedCheckpoint struct {
	Seq        int64     `json:"seq"`
	Head       string    `json:"head"`
	TS         time.Time `json:"ts"`
	Sig        string    `json:"sig"`
	ReceivedAt time.Time `json:"received_at"`
}

// OpenStore открывает (или создаёт) состояние в JSON-файле; path "" — только память.
func OpenStore(path string) (*Store, error) {
	s := &Store{path: path, data: storeData{Devices: map[string]string{}, TXT: map[string][]string{}, Checkpoints: map[string][]WitnessedCheckpoint{}}}
	if path == "" {
		return s, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &s.data); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) save() error {
	if s.path == "" {
		return nil
	}
	b, _ := json.MarshalIndent(s.data, "", " ")
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func hashTok(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewDeviceID — случайный id устройства (имена сертификатов публичны в CT-логах).
func NewDeviceID() string {
	b := make([]byte, 10)
	_, _ = rand.Read(b)
	return strings.ToLower(b32.EncodeToString(b))
}

// Register регистрирует новое устройство; токен показывается один раз.
func (s *Store) Register() (id, token string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	id, token = NewDeviceID(), "dev_"+hex.EncodeToString(b)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Devices[id] = hashTok(token)
	t := now().UTC()
	s.data.Stats.Registrations++
	s.data.Stats.RegisteredAt = append(prune(s.data.Stats.RegisteredAt, t), t)
	return id, token, s.save()
}

// Auth проверяет токен устройства.
func (s *Store) Auth(id, token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.data.Devices[id]
	return ok && h == hashTok(token)
}

// Exists — зарегистрировано ли устройство.
func (s *Store) Exists(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.data.Devices[id]
	return ok
}

// SetTXT / DelTXT — записи _acme-challenge.
func (s *Store) SetTXT(fqdn, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setTXT(fqdn, value)
}

func (s *Store) setTXT(fqdn, value string) error {
	fqdn = strings.ToLower(strings.TrimSuffix(fqdn, "."))
	for _, v := range s.data.TXT[fqdn] {
		if v == value {
			return nil
		}
	}
	s.data.TXT[fqdn] = append(s.data.TXT[fqdn], value)
	return s.save()
}

// SetDeviceTXT ставит TXT ACME DNS-01 устройства id и учитывает заказ
// сертификата Let's Encrypt: relay не видит сам сертификат, но выпуск на
// <id>.<домен> идёт только через этот TXT. TXT одного устройства в пределах
// certOrderGap — один заказ.
func (s *Store) SetDeviceTXT(id, fqdn, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := now().UTC()
	st := &s.data.Stats
	if last, ok := st.LastCertOrder[id]; !ok || t.Sub(last) >= certOrderGap {
		st.CertOrders++
		st.CertOrderedAt = append(prune(st.CertOrderedAt, t), t)
	}
	if st.LastCertOrder == nil {
		st.LastCertOrder = map[string]time.Time{}
	}
	for d, last := range st.LastCertOrder {
		if t.Sub(last) >= certOrderGap {
			delete(st.LastCertOrder, d)
		}
	}
	st.LastCertOrder[id] = t
	return s.setTXT(fqdn, value)
}

func (s *Store) DelTXT(fqdn, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fqdn = strings.ToLower(strings.TrimSuffix(fqdn, "."))
	var keep []string
	for _, v := range s.data.TXT[fqdn] {
		if v != value {
			keep = append(keep, v)
		}
	}
	if len(keep) == 0 {
		delete(s.data.TXT, fqdn)
	} else {
		s.data.TXT[fqdn] = keep
	}
	return s.save()
}

// TXT — значения записи.
func (s *Store) TXT(fqdn string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.data.TXT[strings.ToLower(strings.TrimSuffix(fqdn, "."))]...)
}

// prune оставляет времена не старше StatsWindow.
func prune(ts []time.Time, t time.Time) []time.Time {
	i := 0
	for i < len(ts) && t.Sub(ts[i]) > StatsWindow {
		i++
	}
	return ts[i:]
}

func countSince(ts []time.Time, since time.Time) int {
	n := 0
	for _, t := range ts {
		if !t.Before(since) {
			n++
		}
	}
	return n
}

// StatsSnapshot — счётчики для метрик: всего устройств, накопленные
// регистрации и заказы сертификатов, и они же за последние d (d ≤ StatsWindow).
type StatsSnapshot struct {
	Devices                    int
	Registrations, CertOrders  int64
	RecentRegs, RecentCertOrds int
}

// Snapshot возвращает счётчики для метрик.
func (s *Store) Snapshot(d time.Duration) StatsSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	since := now().Add(-d)
	st := s.data.Stats
	return StatsSnapshot{Devices: len(s.data.Devices), Registrations: st.Registrations, CertOrders: st.CertOrders,
		RecentRegs: countSince(st.RegisteredAt, since), RecentCertOrds: countSince(st.CertOrderedAt, since)}
}

// AddCheckpoint сохраняет свидетельство; номера должны расти.
func (s *Store) AddCheckpoint(id string, cp WitnessedCheckpoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.data.Checkpoints[id]
	if n := len(l); n > 0 && cp.Seq <= l[n-1].Seq {
		if cp.Seq == l[n-1].Seq && cp.Head == l[n-1].Head {
			return nil
		}
		return errors.New("checkpoint sequence must increase (possible log rewrite)")
	}
	s.data.Checkpoints[id] = append(l, cp)
	return s.save()
}

// Checkpoints — свидетельства устройства.
func (s *Store) Checkpoints(id string) []WitnessedCheckpoint {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]WitnessedCheckpoint{}, s.data.Checkpoints[id]...)
}
