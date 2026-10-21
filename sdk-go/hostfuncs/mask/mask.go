// SPDX-License-Identifier: Apache-2.0

// Package mask — маскирование значений секретов за один проход: все вхождения
// всех значений (и их производных форм: base64, URL-, JSON- и HTML-экранирование,
// hex, другой регистр) находятся в исходной строке, пересекающиеся участки
// объединяются и заменяются целиком. Порядок значений не важен: короткое
// значение, входящее в длинное, не оставляет хвост длинного открытым.
package mask

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"html"
	"io"
	"net/url"
	"strings"
)

// MinLen — значения короче не маскируются (слишком много ложных совпадений).
const MinLen = 4

// minDerived — производные формы короче не маскируются.
const minDerived = 6

// Set — набор строк для маскирования (в нижнем регистре ASCII).
type Set struct {
	pats   [][]byte
	maxLen int
}

// New — набор из значений и их производных форм; значения короче MinLen
// пропускаются.
func New(values ...string) *Set {
	s := &Set{}
	seen := map[string]bool{}
	add := func(p string, min int) {
		if len(p) < min {
			return
		}
		l := lower([]byte(p))
		if seen[string(l)] {
			return
		}
		seen[string(l)] = true
		s.pats = append(s.pats, l)
		s.maxLen = max(s.maxLen, len(l))
	}
	for _, v := range values {
		if len(v) < MinLen {
			continue
		}
		add(v, MinLen)
		for _, f := range Forms(v) {
			add(f, minDerived)
		}
	}
	return s
}

// Empty — маскировать нечего.
func (s *Set) Empty() bool { return s == nil || len(s.pats) == 0 }

// Forms — производные формы значения, в которых его может вернуть сервер или
// вывести коннектор: base64 и base64url (со всеми тремя выравниваниями внутри
// более длинной строки), URL-кодирование, JSON- и HTML-экранирование, hex.
func Forms(v string) []string {
	var out []string
	for _, enc := range []*base64.Encoding{base64.RawStdEncoding, base64.RawURLEncoding} {
		for k := 0; k < 3; k++ {
			// Значение со сдвигом k байт: берём только символы, целиком
			// определяемые байтами значения (без соседних байтов).
			full := enc.EncodeToString(append(make([]byte, k), v...))
			from := (k*8 + 5) / 6
			to := (k + len(v)) * 8 / 6
			if to > from && to <= len(full) {
				out = append(out, full[from:to])
			}
		}
	}
	out = append(out, url.QueryEscape(v), url.PathEscape(v), hex.EncodeToString([]byte(v)), html.EscapeString(v))
	if b, err := json.Marshal(v); err == nil {
		j := string(b[1 : len(b)-1])
		out = append(out, j, strings.ReplaceAll(j, "/", `\/`))
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if enc.Encode(v) == nil {
		j := strings.TrimSpace(buf.String())
		j = j[1 : len(j)-1]
		out = append(out, j, strings.ReplaceAll(j, "/", `\/`))
	}
	return out
}

func lower(b []byte) []byte {
	out := make([]byte, len(b))
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out[i] = c
	}
	return out
}

// intervals — объединённые участки b, покрытые вхождениями любых строк набора.
func (s *Set) intervals(b []byte) [][2]int {
	if s.Empty() || len(b) == 0 {
		return nil
	}
	lb := lower(b)
	cover := make([]int, len(b)+1) // разностный массив покрытия
	found := false
	for _, p := range s.pats {
		for off := 0; off+len(p) <= len(lb); {
			i := bytes.Index(lb[off:], p)
			if i < 0 {
				break
			}
			cover[off+i]++
			cover[off+i+len(p)]--
			found = true
			off += i + 1
		}
	}
	if !found {
		return nil
	}
	var out [][2]int
	depth, start := 0, 0
	for i := 0; i <= len(b); i++ {
		if cover[i] != 0 {
			was := depth
			depth += cover[i]
			if was == 0 && depth > 0 {
				start = i
			} else if was > 0 && depth == 0 {
				out = append(out, [2]int{start, i})
			}
		}
	}
	return out
}

// Bytes заменяет каждый замаскированный участок на repl.
func (s *Set) Bytes(b []byte, repl string) []byte {
	iv := s.intervals(b)
	if len(iv) == 0 {
		return b
	}
	var out bytes.Buffer
	prev := 0
	for _, x := range iv {
		out.Write(b[prev:x[0]])
		out.WriteString(repl)
		prev = x[1]
	}
	out.Write(b[prev:])
	return out.Bytes()
}

// String — Bytes для строки.
func (s *Set) String(str, repl string) string {
	if s.Empty() {
		return str
	}
	return string(s.Bytes([]byte(str), repl))
}

// Fill закрашивает замаскированные участки символом '*' на месте (длина не
// меняется: так можно маскировать поток с известным Content-Length).
func (s *Set) Fill(b []byte) {
	for _, x := range s.intervals(b) {
		for i := x[0]; i < x[1]; i++ {
			b[i] = '*'
		}
	}
}

// holdback — сколько байт в конце b могут быть началом строки набора.
func (s *Set) holdback(b []byte) int {
	lb := lower(b[max(0, len(b)-s.maxLen+1):])
	for k := len(lb); k > 0; k-- {
		tail := lb[len(lb)-k:]
		for _, p := range s.pats {
			if len(p) > k && bytes.HasPrefix(p, tail) {
				return k
			}
		}
	}
	return 0
}

// Reader маскирует поток r (Fill, длина не меняется). Байты отдаются сразу,
// задерживается только хвост, который может оказаться началом значения.
func (s *Set) Reader(r io.Reader) io.Reader {
	if s.Empty() {
		return r
	}
	return &reader{s: s, r: r}
}

type reader struct {
	s       *Set
	r       io.Reader
	pending []byte // ещё не отданные байты (исходные)
	force   int    // столько первых байт pending уже входят в найденное значение
	out     []byte // готовые к выдаче байты
	err     error
}

func (m *reader) Read(p []byte) (int, error) {
	for len(m.out) == 0 {
		if m.err != nil {
			if len(m.pending) > 0 {
				m.emit(len(m.pending))
				continue
			}
			return 0, m.err
		}
		buf := make([]byte, 32<<10)
		n, err := m.r.Read(buf)
		m.pending = append(m.pending, buf[:n]...)
		m.err = err
		if err == nil {
			m.emit(len(m.pending) - m.s.holdback(m.pending))
		}
	}
	n := copy(p, m.out)
	m.out = m.out[n:]
	return n, nil
}

// emit маскирует pending и отдаёт первые cut байт; остаток остаётся исходным,
// но участки, уже покрытые значением, начавшимся до cut, будут закрашены.
func (m *reader) emit(cut int) {
	if cut <= 0 {
		return
	}
	masked := append([]byte(nil), m.pending...)
	for i := 0; i < m.force && i < len(masked); i++ {
		masked[i] = '*'
	}
	force := 0
	for _, x := range m.s.intervals(m.pending) {
		for i := x[0]; i < x[1]; i++ {
			masked[i] = '*'
		}
		if x[0] < cut && x[1] > cut {
			force = max(force, x[1]-cut)
		}
	}
	force = max(force, m.force-cut)
	m.out = append(m.out, masked[:cut]...)
	m.pending = append([]byte(nil), m.pending[cut:]...)
	m.force = force
}
