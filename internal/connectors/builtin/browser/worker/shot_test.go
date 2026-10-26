// SPDX-License-Identifier: AGPL-3.0-only

package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

// testPNG — настоящий PNG w×h (одноцветный): фейковые движки отдают его
// вместо строки-заглушки, чтобы проверки сигнатуры и размера были честными.
func testPNG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = 0xff
	}
	img.Set(0, 0, color.RGBA{R: 0xff, A: 0xff})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		panic(err)
	}
	return b.Bytes()
}

// shotEngine — движок, у которого снимок задан: страница с одним элементом.
type shotEngine struct {
	stubEngine
	png []byte
	err error
}

func (e *shotEngine) Screenshot(context.Context, *Clip, bool) ([]byte, error) { return e.png, e.err }
func (e *shotEngine) Eval(_ context.Context, expr string, out any) error {
	if strings.HasPrefix(expr, "((sel, full) =>") {
		b, _ := json.Marshal(map[string]any{"box": map[string]any{"X": 0, "Y": 0, "W": 64, "H": 48}, "matched": 1})
		return json.Unmarshal(b, out)
	}
	return nil
}

// Capture отдаёт PNG с размером из заголовка; не PNG от движка (пустой
// или обрезанный ответ Page.captureScreenshot) — ErrBadShot, а не снимок.
func TestCaptureChecksPNG(t *testing.T) {
	ctx := context.Background()
	e := &shotEngine{png: testPNG(64, 48)}
	x := &Executor{Engine: e}
	s, err := x.Capture(ctx, ShotRequest{Selector: "#a"})
	if err != nil || s.Width != 64 || s.Height != 48 || s.Mode != ShotElement || !bytes.Equal(s.PNG, e.png) {
		t.Fatalf("%+v %v", s, err)
	}
	for _, bad := range [][]byte{nil, []byte("\x89PNG"), e.png[:20]} {
		e.png = bad
		if _, err := x.Capture(ctx, ShotRequest{}); !errors.Is(err, ErrBadShot) {
			t.Fatalf("%q: %v", bad, err)
		}
	}
	e.png, e.err = nil, errors.New("cdp down")
	if _, err := x.Capture(ctx, ShotRequest{}); err == nil || errors.Is(err, ErrBadShot) {
		t.Fatal(err)
	}
}
