// SPDX-License-Identifier: Apache-2.0

// Package hostfuncs — реализация функций хоста ABI (sdk-go/abi) поверх служб хоста:
// каждая функция сначала проверяется по возможностям коннектора (capability);
// сети у коннектора нет — внешний мир только через service_call. Значения
// секретов, раскрытых хостом в этом вызове, вырезаются из доказательств и
// логов (Mask). Один Dispatcher — один вызов коннектора.
//
// Хост gate4.ai подключает сюда свои службы (Vault, хранилище, вызовы других
// коннекторов); тестовый хост коннекторов (hosttest) — фейки.
package hostfuncs

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
	"github.com/gate4ai/gate4ai/sdk-go/capability"
	"github.com/gate4ai/gate4ai/sdk-go/hostfuncs/mask"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"
)

// Secrets — значения секретов по алиасу (Vault хоста).
type Secrets interface {
	Value(alias string) (string, error)
}

// KV — собственное хранилище коннектора.
type KV interface {
	Get(key string) ([]byte, bool, error)
	// Set сохраняет значение; quota — предел общего размера в байтах.
	Set(key string, v []byte, quota int64) error
	Delete(key string) error
}

// ServiceRouter — вызовы действий других коннекторов (service_call): хост
// проводит их через свои политики. rule — разрешение вызывающего из его
// манифеста (уже проверено: действие объявлено).
type ServiceRouter interface {
	ServiceCall(ctx context.Context, rule manifest.ServiceRule, r abi.ServiceCallRequest) (abi.ExecResult, error)
}

// Лимиты одного вызова.
const (
	MaxFSRead     = 16 << 20
	DefaultFSRead = 1 << 20
	MaxEvidence   = 32 << 20
)

// Dispatcher — функции хоста для одного вызова коннектора.
type Dispatcher struct {
	Ctx     context.Context
	Check   *capability.Checker
	Secrets Secrets
	KV      KV
	// Services — вызовы других коннекторов (nil — не поддерживаются).
	Services ServiceRouter
	// Log — диагностический лог коннектора (секреты уже вырезаны).
	Log     func(level, msg string)
	Timeout time.Duration

	mu       sync.Mutex
	used     []string
	evidence []abi.EvidenceItem
	evSize   int
}

func (d *Dispatcher) ctx() context.Context {
	if d.Ctx == nil {
		return context.Background()
	}
	return d.Ctx
}

func (d *Dispatcher) timeout() time.Duration {
	if d.Timeout == 0 {
		return 30 * time.Second
	}
	return d.Timeout
}

// Evidence — доказательства, добавленные через evidence_add.
func (d *Dispatcher) Evidence() []abi.EvidenceItem {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]abi.EvidenceItem(nil), d.evidence...)
}

// masks — набор маскирования по секретам, подставленным в этом вызове
// (значения и производные формы, один проход).
func (d *Dispatcher) masks() *mask.Set {
	d.mu.Lock()
	defer d.mu.Unlock()
	return mask.New(d.used...)
}

// Mask вырезает значения секретов, подставленных в этом вызове.
func (d *Dispatcher) Mask(s string) string { return d.masks().String(s, "[secret]") }

func (d *Dispatcher) maskBytes(b []byte) []byte { return d.masks().Bytes(b, "[secret]") }

// textual — доказательство с текстом (маскируется); изображения — нет.
func textual(mime string) bool {
	m := strings.ToLower(mime)
	return m == "" || strings.HasPrefix(m, "text/") || strings.Contains(m, "json") || strings.Contains(m, "xml") ||
		strings.Contains(m, "javascript") || strings.Contains(m, "x-www-form-urlencoded")
}

// Call — вызов функции хоста (abi.Transport).
func (d *Dispatcher) Call(fn string, req []byte) []byte {
	res, err := d.call(fn, req)
	if err != nil {
		if e, ok := err.(*abi.Error); ok {
			e.Message = d.Mask(e.Message)
			return abi.Fail(e)
		}
		return abi.Fail(abi.Errorf(abi.CodeIO, "%s", d.Mask(err.Error())))
	}
	return abi.OK(res)
}

// handle разбирает запрос функции хоста и вызывает её обработчик.
func handle[Req, Res any](req []byte, f func(Req) (Res, error)) (any, error) {
	var r Req
	if err := json.Unmarshal(req, &r); err != nil {
		return nil, abi.Errorf(abi.CodeBadRequest, "%v", err)
	}
	return f(r)
}

func (d *Dispatcher) call(fn string, req []byte) (any, error) {
	switch fn {
	case abi.FnFSRead, abi.FnFSWrite, abi.FnFSAppend, abi.FnFSList, abi.FnFSStat:
		return handle(req, func(r abi.FSRequest) (any, error) { return d.fs(fn, r) })
	case abi.FnKVGet, abi.FnKVSet, abi.FnKVDelete:
		return handle(req, func(r abi.KVRequest) (any, error) { return d.kv(fn, r) })
	case abi.FnEvidenceAdd:
		return handle(req, func(r abi.EvidenceItem) (any, error) {
			if textual(r.MIME) {
				r.Data = d.maskBytes(r.Data)
			}
			d.mu.Lock()
			defer d.mu.Unlock()
			if d.evSize+len(r.Data) > MaxEvidence {
				return nil, abi.Errorf(abi.CodeLimit, "evidence is larger than %d bytes", MaxEvidence)
			}
			d.evSize += len(r.Data)
			d.evidence = append(d.evidence, r)
			return struct{}{}, nil
		})
	case abi.FnLog:
		return handle(req, func(r abi.LogRequest) (any, error) {
			if d.Log != nil {
				d.Log(r.Level, d.Mask(r.Msg))
			}
			return struct{}{}, nil
		})
	case abi.FnServiceCall:
		return handle(req, d.service)
	}
	return nil, abi.Errorf(abi.CodeNotSupported, "unknown host function %q", fn)
}

var errSwapped = errors.New("path changed during the operation (symlink swap?)")

// openRoot открывает разрешённый каталог как os.Root (дальше файлы
// открываются относительно дескриптора, симлинки — только внутри корня) и
// проверяет, что дескриптор — тот самый каталог: путь по-прежнему без
// симлинков и указывает на тот же каталог (подмена между проверкой и
// открытием — ошибка).
func openRoot(dir string) (*os.Root, error) {
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	st, err := r.Stat(".")
	if err == nil {
		var li os.FileInfo
		var real string
		real, err = filepath.EvalSymlinks(dir)
		if err == nil {
			li, err = os.Lstat(dir)
		}
		if err == nil && (real != dir || !os.SameFile(st, li)) {
			err = errSwapped
		}
	}
	if err != nil {
		r.Close()
		return nil, err
	}
	return r, nil
}

// sameEntry — открытый файл f — это запись rel корня, а не симлинк на другой
// файл (для правил без /**: разрешён только сам файл).
func sameEntry(r *os.Root, rel string, f *os.File) error {
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	li, err := r.Lstat(rel)
	if err != nil {
		return err
	}
	if li.Mode()&os.ModeSymlink != 0 || !os.SameFile(fi, li) {
		return errSwapped
	}
	return nil
}

func rootSize(r *os.Root, rel string) int64 {
	fi, err := r.Stat(rel)
	if err != nil {
		return -1
	}
	return fi.Size()
}

func (d *Dispatcher) fs(fn string, r abi.FSRequest) (any, error) {
	loc, err := d.Check.Locate(r.Path)
	if err != nil {
		return nil, err
	}
	return d.fsAt(fn, r, loc)
}

// fsAt — операция над уже проверенным путём: открытие идёт относительно
// корня, так что подмена компонента пути после проверки не выводит за него.
func (d *Dispatcher) fsAt(fn string, r abi.FSRequest, loc capability.Location) (any, error) {
	real := loc.Real
	ioErr := func(err error) error {
		if os.IsNotExist(err) {
			return abi.Errorf(abi.CodeNotFound, "%s: no such file or directory", r.Path)
		}
		return abi.Errorf(abi.CodeIO, "%v", err)
	}
	root, err := openRoot(loc.Root)
	if err != nil {
		if os.IsNotExist(err) && fn == abi.FnFSStat {
			return abi.FSStatResponse{Size: -1, RealPath: real}, nil
		}
		return nil, ioErr(err)
	}
	defer root.Close()
	rel := loc.Rel
	open := func(flag int, perm os.FileMode) (*os.File, error) {
		f, err := root.OpenFile(rel, flag, perm)
		if err != nil {
			return nil, err
		}
		if loc.Exact {
			if err := sameEntry(root, rel, f); err != nil {
				f.Close()
				return nil, err
			}
		}
		return f, nil
	}
	switch fn {
	case abi.FnFSStat:
		fi, err := root.Stat(rel)
		if err != nil {
			if os.IsNotExist(err) {
				return abi.FSStatResponse{Size: -1, RealPath: real}, nil
			}
			return nil, ioErr(err)
		}
		return abi.FSStatResponse{Exists: true, Dir: fi.IsDir(), Size: fi.Size(), RealPath: real}, nil
	case abi.FnFSRead:
		limit := min(r.Max, MaxFSRead)
		if limit <= 0 {
			limit = DefaultFSRead
		}
		f, err := open(os.O_RDONLY, 0)
		if err != nil {
			return nil, ioErr(err)
		}
		defer f.Close()
		fi, err := f.Stat()
		if err != nil {
			return nil, ioErr(err)
		}
		if fi.IsDir() {
			return nil, abi.Errorf(abi.CodeBadRequest, "%s is a directory", r.Path)
		}
		if r.Offset != 0 { // с позиции; отрицательное — столько байт с конца
			off := r.Offset
			if off < 0 {
				off = max(fi.Size()+off, 0)
			}
			if _, err := f.Seek(min(off, fi.Size()), io.SeekStart); err != nil {
				return nil, ioErr(err)
			}
		}
		b, err := io.ReadAll(io.LimitReader(f, limit+1))
		if err != nil {
			return nil, ioErr(err)
		}
		out := abi.FSReadResponse{Size: fi.Size(), RealPath: real}
		if int64(len(b)) > limit {
			b, out.Truncated = b[:limit], true
		}
		out.Data = b
		return out, nil
	case abi.FnFSList:
		f, err := open(os.O_RDONLY, 0)
		if err != nil {
			return nil, ioErr(err)
		}
		defer f.Close()
		ents, err := f.ReadDir(-1)
		if err != nil {
			return nil, ioErr(err)
		}
		out := abi.FSListResponse{Entries: []abi.FSEntry{}, RealPath: real}
		for _, e := range ents {
			it := abi.FSEntry{Name: e.Name(), Dir: e.IsDir()}
			if fi, err := e.Info(); err == nil && !e.IsDir() {
				it.Size = fi.Size()
			}
			out.Entries = append(out.Entries, it)
		}
		sort.Slice(out.Entries, func(i, j int) bool { return out.Entries[i].Name < out.Entries[j].Name })
		return out, nil
	case abi.FnFSAppend:
		before := rootSize(root, rel)
		f, err := open(os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
		if err != nil {
			return nil, ioErr(err)
		}
		if _, err := f.Write(r.Data); err != nil {
			f.Close()
			return nil, ioErr(err)
		}
		after := int64(-1)
		if fi, err := f.Stat(); err == nil {
			after = fi.Size()
		}
		if err := f.Close(); err != nil {
			return nil, ioErr(err)
		}
		return abi.FSWriteResponse{SizeBefore: before, SizeAfter: after, RealPath: real}, nil
	case abi.FnFSWrite:
		before := rootSize(root, rel)
		// Временный файл в том же каталоге корня, затем rename внутри корня:
		// запись не уходит за его пределы, даже если путь подменят.
		tmpRel := filepath.Join(filepath.Dir(rel), ".gate4ai-"+strconv.FormatInt(time.Now().UnixNano(), 36))
		tmp, err := root.OpenFile(tmpRel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return nil, ioErr(err)
		}
		_, werr := tmp.Write(r.Data)
		cerr := tmp.Close()
		if werr == nil {
			werr = cerr
		}
		if werr == nil {
			werr = root.Rename(tmpRel, rel)
		}
		if werr != nil {
			_ = root.Remove(tmpRel)
			return nil, ioErr(werr)
		}
		return abi.FSWriteResponse{SizeBefore: before, SizeAfter: rootSize(root, rel), RealPath: real}, nil
	}
	return nil, abi.Errorf(abi.CodeNotSupported, "%s", fn)
}

func (d *Dispatcher) kv(fn string, r abi.KVRequest) (any, error) {
	quota, err := d.Check.Storage()
	if err != nil {
		return nil, err
	}
	if d.KV == nil {
		return nil, abi.Errorf(abi.CodeNotSupported, "no storage")
	}
	if r.Key == "" || len(r.Key) > 256 || !utf8.ValidString(r.Key) {
		return nil, abi.Errorf(abi.CodeBadRequest, "bad key")
	}
	switch fn {
	case abi.FnKVGet:
		v, ok, err := d.KV.Get(r.Key)
		if err != nil {
			return nil, abi.Errorf(abi.CodeIO, "%v", err)
		}
		return abi.KVResponse{Value: v, Found: ok}, nil
	case abi.FnKVSet:
		if err := d.KV.Set(r.Key, r.Value, quota); err != nil {
			if e, ok := err.(*abi.Error); ok {
				return nil, e
			}
			return nil, abi.Errorf(abi.CodeIO, "%v", err)
		}
		return struct{}{}, nil
	default:
		if err := d.KV.Delete(r.Key); err != nil {
			return nil, abi.Errorf(abi.CodeIO, "%v", err)
		}
		return struct{}{}, nil
	}
}

func (d *Dispatcher) service(r abi.ServiceCallRequest) (any, error) {
	rule, err := d.Check.Service(r.Target, r.Action)
	if err != nil {
		return nil, err
	}
	if d.Services == nil {
		return nil, abi.Errorf(abi.CodeNotSupported, "no service calls on this host")
	}
	return d.Services.ServiceCall(d.ctx(), rule, r)
}

// Close освобождает ресурсы вызова (сейчас — ничего: соединений у коннекторов нет).
func (d *Dispatcher) Close() {}
