// SPDX-License-Identifier: AGPL-3.0-only

package worker

import (
	"context"
	"encoding/binary"
	"fmt"
	"strings"
)

// Clip — прямоугольник страницы в CSS-пикселях от начала документа.
type Clip struct{ X, Y, W, H float64 }

// Режимы снимка.
const (
	ShotElement  = "element"   // элемент по селектору
	ShotViewport = "viewport"  // видимое окно
	ShotFullPage = "full_page" // вся страница
)

// ShotRequest — что снять: элемент по CSS-селектору, иначе видимое окно, или
// всю страницу (FullPage).
type ShotRequest struct {
	Selector string
	FullPage bool
}

// Mode — режим снимка по запросу.
func (r ShotRequest) Mode() string {
	switch {
	case r.Selector != "":
		return ShotElement
	case r.FullPage:
		return ShotFullPage
	}
	return ShotViewport
}

// Shot — снимок PNG и его размер в пикселях (из заголовка PNG).
type Shot struct {
	PNG           []byte
	Width, Height int
	Mode          string
}

// MaxShotHeight — предел высоты снимка всей страницы и элемента (CSS-пиксели):
// длиннее Chrome снимает с искажениями, а файл становится огромным.
const MaxShotHeight = 16384

// shotJS — первый видимый элемент по селектору (прокручен в окно) и
// его прямоугольник от начала документа. Если видимого нет — сколько
// элементов совпало и почему первые из них не видны (display:none,
// visibility:hidden, нулевой размер, вне страницы). Без селектора и full —
// прокрутка в начало (снимок всей страницы: закреплённые элементы — на месте)
// и размер документа.
const shotJS = `((sel, full) => {
  if (!sel) {
    const d = document.documentElement, b = document.body || d;
    if (full) scrollTo(0, 0);
    return {box: {X: 0, Y: 0, W: Math.max(d.scrollWidth, b.scrollWidth, innerWidth), H: Math.max(d.scrollHeight, b.scrollHeight, innerHeight)}, matched: 0};
  }
  let all;
  try { all = [...document.querySelectorAll(sel)]; } catch (e) { return {error: 'invalid CSS selector: ' + e.message}; }
  const docW = Math.max(document.documentElement.scrollWidth, innerWidth), docH = Math.max(document.documentElement.scrollHeight, innerHeight);
  const name = e => e.localName + (e.id ? '#' + e.id : '') + (typeof e.className === 'string' && e.className.trim() ? '.' + e.className.trim().split(/\s+/).slice(0, 2).join('.') : '');
  const why = e => {
    for (let a = e; a; a = a.parentElement) {
      const st = getComputedStyle(a);
      if (st.display === 'none') return 'display:none' + (a === e ? '' : ' on ' + name(a));
      if (st.visibility === 'hidden' && a === e) return 'visibility:hidden';
      if (st.opacity === '0') return 'opacity:0' + (a === e ? '' : ' on ' + name(a));
    }
    const r = e.getBoundingClientRect();
    if (r.width === 0 || r.height === 0) return 'zero size (' + Math.round(r.width) + 'x' + Math.round(r.height) + ')';
    const x = r.left + scrollX, y = r.top + scrollY;
    if (x + r.width <= 0 || y + r.height <= 0 || x >= docW || y >= docH) return 'outside the page (at ' + Math.round(x) + ',' + Math.round(y) + ')';
    return '';
  };
  const reasons = [];
  for (const e of all) {
    const w = why(e);
    if (w) { if (reasons.length < 3) reasons.push(name(e) + ': ' + w); continue; }
    e.scrollIntoView({block: 'nearest', inline: 'nearest'});
    const r = e.getBoundingClientRect();
    return {box: {X: r.left + scrollX, Y: r.top + scrollY, W: r.width, H: r.height}, matched: all.length};
  }
  return {matched: all.length, hidden: reasons};
})`

// Capture — снимок по запросу: элемент (первый видимый по селектору), окно
// или вся страница. Поля пароля и поля с введённым секретом закрашены всегда.
func (x *Executor) Capture(ctx context.Context, r ShotRequest) (Shot, error) {
	var found struct {
		Box     *Clip    `json:"box"`
		Matched int      `json:"matched"`
		Hidden  []string `json:"hidden"`
		Error   string   `json:"error"`
	}
	if err := x.Engine.Eval(ctx, call(shotJS, r.Selector, r.FullPage), &found); err != nil {
		return Shot{}, err
	}
	switch {
	case found.Error != "":
		return Shot{}, fmt.Errorf("screenshot: %s", found.Error)
	case found.Box == nil && found.Matched == 0:
		return Shot{}, fmt.Errorf("%w: %s matches nothing on the page", ErrNotFound, r.Selector)
	case found.Box == nil:
		return Shot{}, fmt.Errorf("%w: %s matches %d element(s), none is visible: %s", ErrNotFound, r.Selector, found.Matched, strings.Join(found.Hidden, "; "))
	}
	var clip *Clip
	full := false
	switch r.Mode() {
	case ShotElement:
		clip = found.Box
	case ShotFullPage:
		full, clip = true, found.Box
	}
	if clip != nil && clip.H > MaxShotHeight {
		clip.H = MaxShotHeight
	}
	png, err := x.covered(ctx, func() ([]byte, error) { return x.Engine.Screenshot(ctx, clip, full) })
	if err != nil {
		return Shot{}, err
	}
	w, h := pngSize(png)
	return Shot{PNG: png, Width: w, Height: h, Mode: r.Mode()}, nil
}

// pngSize — ширина и высота из заголовка PNG (IHDR); 0 — не PNG.
func pngSize(b []byte) (int, int) {
	if len(b) < 24 || string(b[:8]) != "\x89PNG\r\n\x1a\n" || string(b[12:16]) != "IHDR" {
		return 0, 0
	}
	return int(binary.BigEndian.Uint32(b[16:20])), int(binary.BigEndian.Uint32(b[20:24]))
}
