// SPDX-License-Identifier: AGPL-3.0-only

package relay

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
)

// Store — состояние relay: устройства, TXT-записи ACME, чекпоинты-свидетельства.
//
// Изменения сериализует wmu; данные в памяти защищает mu, и запись на диск
// идёт без него: Auth и Exists (каждое входящее соединение и туннель) не ждут
// диска. Файл состояния пишется атомарно (временный файл, fsync, rename, fsync
// каталога); если запись не удалась, изменение в памяти откатывается.
// Чекпоинты лежат по файлу на устройство (<state>.witness/<id>.json): приём
// чекпоинта не переписывает всё состояние.
type Store struct {
	path string
	wmu  sync.Mutex   // изменения по одному: снимок и запись без mu
	mu   sync.RWMutex // данные в памяти
	data storeData

	// MaxDevices — сколько устройств можно зарегистрировать всего (0 — без предела).
	MaxDevices int
	// MaxCheckpoints — сколько последних чекпоинтов хранится на устройство.
	MaxCheckpoints int
	// MaxTXT — значений TXT на одно имя: лишние (самые старые) вытесняются.
	MaxTXT int
	// CertOrdersPerDay — заказов сертификата на устройство за сутки (0 — без предела).
	CertOrdersPerDay int
}

type storeData struct {
	Devices map[string]string   `json:"devices"` // device id → sha256(token)
	TXT     map[string][]string `json:"txt"`     // fqdn → значения
	// Checkpoints — чекпоинты хранилища в памяти (path ""); у хранилища в
	// файле они в каталоге свидетеля.
	Checkpoints map[string][]WitnessedCheckpoint `json:"-"`
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
	// DeviceOrders — заказы устройства за последние сутки (лимит CertOrdersPerDay).
	DeviceOrders map[string][]time.Time `json:"device_orders,omitempty"`
}

// StatsWindow — сколько хранятся времена событий; метрики считают окно 7 дней.
const StatsWindow = 8 * 24 * time.Hour

// certOrderGap — TXT, поставленные устройством чаще этого, относятся к одному
// заказу сертификата (несколько авторизаций, повторы проверки).
const certOrderGap = 10 * time.Minute

// Пределы store по умолчанию.
const (
	DefaultMaxDevices       = 100000
	DefaultMaxCheckpoints   = 1000
	DefaultMaxTXT           = 2
	DefaultCertOrdersPerDay = 3
	maxTXTLen               = 255 // одна строка TXT (RFC 1035); значение ACME — 43 символа
)

// Ошибки store, которые API отдаёт клиенту своими кодами.
var (
	ErrFull         = errors.New("relay: device limit reached")
	ErrBadTXT       = errors.New("relay: bad TXT value")
	ErrOrderLimit   = errors.New("relay: certificate order limit for the device reached")
	ErrRewind       = errors.New("checkpoint sequence must increase (possible log rewrite)")
	ErrUnknownOwner = errors.New("relay: unknown device")
)

// now — часы store (подменяются в тестах).
var now = time.Now

// writeFile — атомарная запись файла (подменяется в тестах).
var writeFile = writeFileAtomic

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
	s := &Store{path: path, MaxDevices: DefaultMaxDevices,
		MaxCheckpoints: DefaultMaxCheckpoints, MaxTXT: DefaultMaxTXT, CertOrdersPerDay: DefaultCertOrdersPerDay}
	if path != "" {
		b, err := os.ReadFile(path)
		if err == nil {
			err = json.Unmarshal(b, &s.data)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	// null или отсутствие поля в файле — пустые карты, а не паника на первой записи.
	if s.data.Devices == nil {
		s.data.Devices = map[string]string{}
	}
	if s.data.TXT == nil {
		s.data.TXT = map[string][]string{}
	}
	if s.data.Checkpoints == nil {
		s.data.Checkpoints = map[string][]WitnessedCheckpoint{}
	}
	return s, nil
}

// update применяет изменение fn к данным и сохраняет их; undo (nil — ничего
// не изменилось) откатывает изменение, если запись на диск не удалась.
func (s *Store) update(fn func(d *storeData) (undo func(), err error)) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	s.mu.Lock()
	undo, err := fn(&s.data)
	s.mu.Unlock()
	if err != nil || undo == nil {
		return err
	}
	if err := s.save(); err != nil {
		s.mu.Lock()
		undo()
		s.mu.Unlock()
		return err
	}
	return nil
}

// save пишет состояние (под wmu: данные меняются только под ним, поэтому
// читать их здесь можно без mu).
func (s *Store) save() error {
	if s.path == "" {
		return nil
	}
	b, err := json.MarshalIndent(s.data, "", " ")
	if err != nil {
		return err
	}
	return writeFile(s.path, b)
}

// writeFileAtomic пишет файл так, что после сбоя питания на диске старая или
// новая версия целиком: временный файл, fsync, rename, fsync каталога.
func writeFileAtomic(path string, b []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, 0o600)
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if runtime.GOOS == "windows" { // каталог там не синхронизируется
		return nil
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
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
	err = s.update(func(d *storeData) (func(), error) {
		if s.MaxDevices > 0 && len(d.Devices) >= s.MaxDevices {
			return nil, ErrFull
		}
		old := d.Stats
		d.Devices[id] = hashTok(token)
		t := now().UTC()
		d.Stats.Registrations++
		d.Stats.RegisteredAt = append(prune(d.Stats.RegisteredAt, t), t)
		return func() { delete(d.Devices, id); d.Stats = old }, nil
	})
	if err != nil {
		return "", "", err
	}
	return id, token, nil
}

// zeroHash — хэш, с которым сравнивается токен неизвестного устройства: время
// проверки не выдаёт, есть ли такое устройство.
var zeroHash = strings.Repeat("0", 64)

// Auth проверяет токен устройства (сравнение за постоянное время).
func (s *Store) Auth(id, token string) bool {
	got := hashTok(token)
	s.mu.RLock()
	h, ok := s.data.Devices[id]
	s.mu.RUnlock()
	if !ok {
		h = zeroHash
	}
	return subtle.ConstantTimeCompare([]byte(h), []byte(got)) == 1 && ok
}

// Exists — зарегистрировано ли устройство.
func (s *Store) Exists(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.data.Devices[id]
	return ok
}

// validTXT — значение TXT ACME: base64url (RFC 8555, 8.4), не длиннее одной
// строки TXT. Иное ломало бы упаковку ответа DNS.
func validTXT(v string) bool {
	if v == "" || len(v) > maxTXTLen {
		return false
	}
	for _, c := range v {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func normName(fqdn string) string { return strings.ToLower(strings.TrimSuffix(fqdn, ".")) }

// SetTXT ставит запись _acme-challenge (без учёта заказов устройства).
func (s *Store) SetTXT(fqdn, value string) error {
	if !validTXT(value) {
		return ErrBadTXT
	}
	return s.update(func(d *storeData) (func(), error) { return s.addTXT(d, normName(fqdn), value), nil })
}

// addTXT добавляет значение (лишние старые вытесняются: на имя не больше
// MaxTXT значений); возвращает откат или nil, если значение уже есть.
func (s *Store) addTXT(d *storeData, fqdn, value string) func() {
	old, had := d.TXT[fqdn]
	if slices.Contains(old, value) {
		return nil
	}
	l := append(append([]string{}, old...), value)
	if n := max(s.MaxTXT, 1); len(l) > n {
		l = l[len(l)-n:]
	}
	d.TXT[fqdn] = l
	return func() {
		if had {
			d.TXT[fqdn] = old
		} else {
			delete(d.TXT, fqdn)
		}
	}
}

// SetDeviceTXT ставит TXT ACME DNS-01 устройства id и учитывает заказ
// сертификата Let's Encrypt: relay не видит сам сертификат, но выпуск на
// <id>.<домен> идёт только через этот TXT. TXT одного устройства в пределах
// certOrderGap — один заказ; заказов на устройство в сутки не больше
// CertOrdersPerDay (общий лимит Let's Encrypt на домен relay не выбрать одним
// устройством).
func (s *Store) SetDeviceTXT(id, fqdn, value string) error {
	if !validTXT(value) {
		return ErrBadTXT
	}
	fqdn = normName(fqdn)
	return s.update(func(d *storeData) (func(), error) {
		if _, ok := d.Devices[id]; !ok {
			return nil, ErrUnknownOwner
		}
		if slices.Contains(d.TXT[fqdn], value) {
			return nil, nil
		}
		t := now().UTC()
		st := &d.Stats
		old := *st
		old.LastCertOrder = maps.Clone(st.LastCertOrder)
		old.DeviceOrders = maps.Clone(st.DeviceOrders)
		if last, ok := st.LastCertOrder[id]; !ok || t.Sub(last) >= certOrderGap {
			if st.DeviceOrders == nil {
				st.DeviceOrders = map[string][]time.Time{}
			}
			for dev, ts := range st.DeviceOrders {
				if ts = pruneTo(ts, t, 24*time.Hour); len(ts) == 0 {
					delete(st.DeviceOrders, dev)
				} else {
					st.DeviceOrders[dev] = ts
				}
			}
			if s.CertOrdersPerDay > 0 && len(st.DeviceOrders[id]) >= s.CertOrdersPerDay {
				*st = old
				return nil, ErrOrderLimit
			}
			st.DeviceOrders[id] = append(st.DeviceOrders[id], t)
			st.CertOrders++
			st.CertOrderedAt = append(prune(st.CertOrderedAt, t), t)
		}
		if st.LastCertOrder == nil {
			st.LastCertOrder = map[string]time.Time{}
		}
		maps.DeleteFunc(st.LastCertOrder, func(_ string, last time.Time) bool { return t.Sub(last) >= certOrderGap })
		st.LastCertOrder[id] = t
		undoTXT := s.addTXT(d, fqdn, value)
		return func() {
			d.Stats = old
			if undoTXT != nil {
				undoTXT()
			}
		}, nil
	})
}

// DelTXT удаляет значение записи _acme-challenge.
func (s *Store) DelTXT(fqdn, value string) error {
	fqdn = normName(fqdn)
	return s.update(func(d *storeData) (func(), error) {
		old, ok := d.TXT[fqdn]
		if !ok {
			return nil, nil
		}
		keep := slices.DeleteFunc(slices.Clone(old), func(v string) bool { return v == value })
		if len(keep) == len(old) {
			return nil, nil
		}
		if len(keep) == 0 {
			delete(d.TXT, fqdn)
		} else {
			d.TXT[fqdn] = keep
		}
		return func() { d.TXT[fqdn] = old }, nil
	})
}

// TXT — значения записи.
func (s *Store) TXT(fqdn string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string{}, s.data.TXT[normName(fqdn)]...)
}

// prune оставляет времена не старше StatsWindow.
func prune(ts []time.Time, t time.Time) []time.Time { return pruneTo(ts, t, StatsWindow) }

func pruneTo(ts []time.Time, t time.Time, window time.Duration) []time.Time {
	i := 0
	for i < len(ts) && t.Sub(ts[i]) > window {
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
	s.mu.RLock()
	defer s.mu.RUnlock()
	since := now().Add(-d)
	st := s.data.Stats
	return StatsSnapshot{Devices: len(s.data.Devices), Registrations: st.Registrations, CertOrders: st.CertOrders,
		RecentRegs: countSince(st.RegisteredAt, since), RecentCertOrds: countSince(st.CertOrderedAt, since)}
}

// witnessPath — файл чекпоинтов устройства (id — base32 из NewDeviceID).
func (s *Store) witnessPath(id string) string {
	return filepath.Join(s.path+".witness", id+".json")
}

func (s *Store) writeWitness(id string, l []WitnessedCheckpoint) error {
	if err := os.MkdirAll(s.path+".witness", 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(l)
	if err != nil {
		return err
	}
	return writeFile(s.witnessPath(id), b)
}

func (s *Store) readWitness(id string) ([]WitnessedCheckpoint, error) {
	b, err := os.ReadFile(s.witnessPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var l []WitnessedCheckpoint
	return l, json.Unmarshal(b, &l)
}

// AddCheckpoint сохраняет свидетельство; номера должны расти. Тот же seq с
// тем же head — повтор (принимается без изменений), с другим head — ErrRewind.
func (s *Store) AddCheckpoint(id string, cp WitnessedCheckpoint) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if !s.Exists(id) {
		return ErrUnknownOwner
	}
	l, err := s.checkpoints(id)
	if err != nil {
		return err
	}
	if n := len(l); n > 0 && cp.Seq <= l[n-1].Seq {
		if cp.Seq == l[n-1].Seq && cp.Head == l[n-1].Head {
			return nil
		}
		return ErrRewind
	}
	l = append(append([]WitnessedCheckpoint{}, l...), cp)
	if k := max(s.MaxCheckpoints, 1); len(l) > k {
		l = l[len(l)-k:]
	}
	if s.path != "" {
		return s.writeWitness(id, l)
	}
	s.mu.Lock()
	s.data.Checkpoints[id] = l
	s.mu.Unlock()
	return nil
}

// checkpoints — сохранённые чекпоинты устройства: из файла свидетеля или из
// памяти (срез там не меняется, только заменяется целиком).
func (s *Store) checkpoints(id string) ([]WitnessedCheckpoint, error) {
	if s.path != "" {
		return s.readWitness(id)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.Checkpoints[id], nil
}

// Checkpoints — свидетельства устройства (последние MaxCheckpoints).
func (s *Store) Checkpoints(id string) []WitnessedCheckpoint {
	l, _ := s.checkpoints(id)
	return append([]WitnessedCheckpoint{}, l...)
}
