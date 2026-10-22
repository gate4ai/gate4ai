// SPDX-License-Identifier: AGPL-3.0-only

// Package files — встроенный коннектор ядра «Файлы» (runtime: builtin): чтение,
// список, добавление и запись в разрешённых папках (connectors.files.roots)
// поверх функций хоста fs_* с той же проверкой возможностей, что у WASM.
// Скрытые файлы ниже корня, ключи и каталоги секретов закрыты всегда
// (guard.go), открыть — allow_hidden и allow_paths.
// До v0.6 ядра — отдельный WASM-коннектор gate4ai-files из магазина.
package files

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"
)

// Name — имя коннектора.
const Name = "files"

//go:embed manifest.yaml
var manifestYAML []byte

// Manifest — манифест коннектора.
func Manifest() manifest.Manifest {
	m, err := manifest.Parse(manifestYAML)
	if err != nil {
		panic(err)
	}
	return m
}

// Settings — connectors.files в конфигурации.
type Settings struct {
	Roots    []string `json:"roots"`
	MaxBytes int64    `json:"max_bytes"`
	// AllowHidden открывает скрытые файлы и папки ниже корня (.ssh, .config…).
	AllowHidden bool `json:"allow_hidden"`
	// AllowPaths — пути (файлы или папки целиком), открытые несмотря на
	// защиту: скрытые, ключи, каталоги секретов и браузеров.
	AllowPaths []string `json:"allow_paths"`
}

func settings(m map[string]any) (Settings, error) {
	var s Settings
	b, _ := json.Marshal(m)
	if err := json.Unmarshal(b, &s); err != nil {
		return s, err
	}
	if len(s.Roots) == 0 {
		return s, errors.New("connectors.files.roots is empty: no folders are allowed")
	}
	if s.MaxBytes <= 0 {
		s.MaxBytes = 1 << 20
	}
	return s, nil
}

// Files — коннектор.
type Files struct{}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func evidence(w abi.FSWriteResponse, data string) []abi.EvidenceItem {
	b, _ := json.Marshal(map[string]any{"path": w.RealPath, "size_before": w.SizeBefore, "size_after": w.SizeAfter, "data_sha256": sum([]byte(data))})
	return []abi.EvidenceItem{{Name: "file.json", MIME: "application/json", Data: b}}
}

// Execute исполняет действие.
func (Files) Execute(ctx context.Context, h *abi.Host, r abi.ExecRequest) (abi.ExecResult, error) {
	s, err := settings(r.Settings)
	if err != nil {
		return abi.ExecResult{}, err
	}
	p, _ := r.Args["path"].(string)
	g, err := newGuard(h, s)
	if err != nil {
		return abi.ExecResult{}, err
	}
	if _, err := g.stat(h, p); err != nil {
		return abi.ExecResult{}, err
	}
	switch r.Action {
	case "read":
		f, err := h.FSRead(p, s.MaxBytes)
		if err != nil {
			return abi.ExecResult{}, err
		}
		// Путь могли подменить симлинком между проверкой и чтением.
		if err := g.check(p, f.RealPath); err != nil {
			return abi.ExecResult{}, err
		}
		return readResult(p, f)
	case "list":
		l, err := h.FSList(p)
		if err != nil {
			return abi.ExecResult{}, err
		}
		if err := g.check(p, l.RealPath); err != nil {
			return abi.ExecResult{}, err
		}
		out := []any{}
		for _, e := range l.Entries {
			// Закрытые элементы в списке не показываются.
			if g.why(filepath.Join(l.RealPath, e.Name)) != "" {
				continue
			}
			item := map[string]any{"name": e.Name, "dir": e.Dir}
			if !e.Dir {
				item["size"] = e.Size
			}
			out = append(out, item)
		}
		return abi.ExecResult{Structured: map[string]any{"path": p, "entries": out}, ResultRef: l.RealPath}, nil
	case "append":
		text, _ := r.Args["text"].(string)
		w, err := h.FSAppend(p, []byte(text))
		if err != nil {
			return abi.ExecResult{}, err
		}
		return abi.ExecResult{Structured: map[string]any{"path": p, "appended_bytes": len(text)}, ResultRef: w.RealPath, Evidence: evidence(w, text)}, nil
	case "write":
		content, _ := r.Args["content"].(string)
		w, err := h.FSWrite(p, []byte(content))
		if err != nil {
			return abi.ExecResult{}, err
		}
		return abi.ExecResult{Structured: map[string]any{"path": p, "bytes": len(content)}, ResultRef: w.RealPath, Evidence: evidence(w, content)}, nil
	}
	return abi.ExecResult{}, fmt.Errorf("unknown action %s", r.Action)
}

// readResult — содержимое файла как блок для агента: текст — text, изображение —
// image, прочее — двоичный resource. Двоичный файл, обрезанный по max_bytes,
// бесполезен — ошибка.
func readResult(p string, f abi.FSReadResponse) (abi.ExecResult, error) {
	mt := mime.TypeByExtension(strings.ToLower(path.Ext(f.RealPath)))
	text := utf8.Valid(f.Data) && bytes.IndexByte(f.Data, 0) < 0
	var c abi.Content
	switch {
	case text:
		if mt == "" || !strings.HasPrefix(mt, "text/") && !strings.Contains(mt, "json") && !strings.Contains(mt, "xml") {
			mt = "text/plain; charset=utf-8"
		}
		c = abi.Text(string(f.Data))
	case f.Truncated:
		return abi.ExecResult{}, fmt.Errorf("binary file is larger than max_bytes (%d bytes)", f.Size)
	case strings.HasPrefix(mt, "image/"):
		c = abi.Image(mt, f.Data)
	default:
		if mt == "" {
			mt = "application/octet-stream"
		}
		c = abi.Resource("file://"+f.RealPath, mt, "", f.Data)
	}
	st := map[string]any{"path": p, "mime_type": mt, "size": f.Size}
	if f.Truncated {
		st["truncated"] = true
	}
	return abi.ExecResult{Content: []abi.Content{c}, Structured: st, ResultRef: f.RealPath}, nil
}

// Verify перечитывает файл: append — файл заканчивается добавленным текстом,
// write — содержимое совпадает побайтно.
func (Files) Verify(ctx context.Context, h *abi.Host, r abi.VerifyRequest) (abi.VerifyResult, error) {
	if _, err := settings(r.Settings); err != nil {
		return abi.VerifyResult{}, err
	}
	p, _ := r.Args["path"].(string)
	var f abi.FSReadResponse
	var err error
	var ok bool
	switch r.Action {
	case "append":
		// Только хвост длиной в добавленный текст: файл (лог) может быть
		// больше предела fs_read, а проверять нужно лишь его конец.
		text, _ := r.Args["text"].(string)
		n := int64(len(text))
		f, err = h.FSReadAt(p, -n, n)
		ok = err == nil && f.Size >= n && bytes.Equal(f.Data, []byte(text))
	case "write":
		content, _ := r.Args["content"].(string)
		f, err = h.FSRead(p, int64(len(content))+1)
		ok = err == nil && !f.Truncated && bytes.Equal(f.Data, []byte(content))
	default:
		return abi.VerifyResult{}, abi.Errorf(abi.CodeBadRequest, "action %s has no verification", r.Action)
	}
	if err != nil {
		return abi.VerifyResult{}, err
	}
	if !ok {
		return abi.VerifyResult{Status: abi.NotConfirmed, Method: "read_back", Detail: "file content does not match"}, nil
	}
	return abi.VerifyResult{Status: abi.Verified, Method: "read_back", Detail: "sha256 " + sum(f.Data)}, nil
}
