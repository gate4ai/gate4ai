// SPDX-License-Identifier: AGPL-3.0-only

package browser

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/abi"

	"github.com/gate4ai/gate4ai/internal/connectors/builtin/browser/worker"
)

// ShotKeep — снимки сеансов агентов старше этого удаляются при следующем снимке.
var ShotKeep = 24 * time.Hour

// pngMagic — сигнатура файла PNG.
const pngMagic = "\x89PNG\r\n\x1a\n"

// saveShot пишет снимок сеанса агента в <каталог данных>/browser-shots/<сеанс>-<n>.png
// (0600; папка — та же, что у steps_file, — каталог данных gate4ai). Не PNG
// (пустой или обрезанный ответ движка) не пишется: лучше ошибка, чем файл,
// который не открыть.
func saveShot(id string, n int, png []byte) (string, error) {
	if !strings.HasPrefix(string(png), pngMagic) {
		return "", fmt.Errorf("not a PNG image (%d bytes)", len(png))
	}
	dir := filepath.Join(dataDir(), "browser-shots")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if old, err := os.ReadDir(dir); err == nil {
		for _, e := range old {
			if i, err := e.Info(); err == nil && strings.HasSuffix(e.Name(), ".png") && time.Since(i.ModTime()) > ShotKeep {
				_ = os.Remove(filepath.Join(dir, e.Name()))
			}
		}
	}
	path := filepath.Join(dir, fmt.Sprintf("%s-%d.png", id, n))
	return path, os.WriteFile(path, png, 0o600)
}

// capture — снимок страницы сеанса: у сеанса агента — файл (file в info),
// у страницы вызывающего коннектора — только изображение в content.
func (cs *callSession) capture(ctx context.Context, r worker.ShotRequest) (map[string]any, abi.ExecResult, error) {
	s, err := cs.s.Capture(ctx, r)
	if errors.Is(err, worker.ErrBadShot) {
		return nil, abi.ExecResult{}, abi.Errorf(abi.CodeIO, "%v", err)
	}
	if err != nil {
		return nil, abi.ExecResult{}, err
	}
	info := map[string]any{"mode": s.Mode, "width": s.Width, "height": s.Height, "bytes": len(s.PNG)}
	if r.Selector != "" {
		info["selector"] = r.Selector
	}
	res := abi.ExecResult{Content: []abi.Content{abi.Image("image/png", s.PNG)},
		Evidence: []abi.EvidenceItem{{Name: s.Mode + ".png", MIME: "image/png", Data: s.PNG}}}
	if cs.sid != "" {
		cs.shots++
		path, err := saveShot(cs.sid, cs.shots, s.PNG)
		if err != nil {
			return nil, abi.ExecResult{}, abi.Errorf(abi.CodeIO, "the screenshot was taken but not saved: %v", err)
		}
		info["file"] = path
		res.ResultRef = path
	}
	return info, res, nil
}

// failShot — к ошибке act (нет элемента, нет ожидаемого результата) в сеансе
// агента — снимок окна: видно, почему; путь — в тексте ошибки.
func (cs *callSession) failShot(ctx context.Context, err error) error {
	if cs.sid == "" {
		return err
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	info, _, serr := cs.capture(ctx, worker.ShotRequest{})
	if serr != nil {
		return err
	}
	return fmt.Errorf("%w; screenshot of the page: %s", err, info["file"])
}
