// SPDX-License-Identifier: Apache-2.0

package abi

import (
	"encoding/json"
)

// Transport — вызов функции хоста: запрос JSON → Response JSON. В WASM-модуле —
// импорт host_call; в нативных тестах коннектора — хост в том же процессе.
type Transport interface {
	Call(fn string, req []byte) []byte
}

// Host — типизированные функции хоста для кода коннектора.
type Host struct {
	T Transport
}

func (h *Host) call(fn string, req, resp any) error {
	b, err := json.Marshal(req)
	if err != nil {
		return Errorf(CodeBadRequest, "marshal: %v", err)
	}
	return Decode(h.T.Call(fn, b), resp)
}

// callAs — call с ответом типа T.
func callAs[T any](h *Host, fn string, req any) (T, error) {
	var out T
	err := h.call(fn, req, &out)
	return out, err
}

// FSRead — fs_read.
func (h *Host) FSRead(path string, max int64) (FSReadResponse, error) {
	return callAs[FSReadResponse](h, FnFSRead, FSRequest{Path: path, Max: max})
}

// FSReadAt — fs_read с позиции offset (отрицательное — столько байт с конца файла).
func (h *Host) FSReadAt(path string, offset, max int64) (FSReadResponse, error) {
	return callAs[FSReadResponse](h, FnFSRead, FSRequest{Path: path, Max: max, Offset: offset})
}

// FSWrite — fs_write (атомарно: временный файл и переименование).
func (h *Host) FSWrite(path string, data []byte) (FSWriteResponse, error) {
	return callAs[FSWriteResponse](h, FnFSWrite, FSRequest{Path: path, Data: data})
}

// FSAppend — fs_append (создаёт файл, если его нет).
func (h *Host) FSAppend(path string, data []byte) (FSWriteResponse, error) {
	return callAs[FSWriteResponse](h, FnFSAppend, FSRequest{Path: path, Data: data})
}

// FSList — fs_list.
func (h *Host) FSList(path string) (FSListResponse, error) {
	return callAs[FSListResponse](h, FnFSList, FSRequest{Path: path})
}

// FSStat — fs_stat.
func (h *Host) FSStat(path string) (FSStatResponse, error) {
	return callAs[FSStatResponse](h, FnFSStat, FSRequest{Path: path})
}

// KVGet — kv_get.
func (h *Host) KVGet(key string) ([]byte, bool, error) {
	out, err := callAs[KVResponse](h, FnKVGet, KVRequest{Key: key})
	return out.Value, out.Found, err
}

// KVSet — kv_set.
func (h *Host) KVSet(key string, v []byte) error {
	return h.call(FnKVSet, KVRequest{Key: key, Value: v}, nil)
}

// KVDelete — kv_delete.
func (h *Host) KVDelete(key string) error { return h.call(FnKVDelete, KVRequest{Key: key}, nil) }

// Evidence — evidence_add.
func (h *Host) Evidence(name, mime string, data []byte) error {
	return h.call(FnEvidenceAdd, EvidenceItem{Name: name, MIME: mime, Data: data}, nil)
}

// Log — log.
func (h *Host) Log(level, msg string) { _ = h.call(FnLog, LogRequest{Level: level, Msg: msg}, nil) }

// ServiceCall — service_call: действие другого коннектора. Структурированный
// результат поставщика декодируется в out (nil — не нужен); возвращается
// результат целиком (content, result_ref).
func (h *Host) ServiceCall(target, action string, args any, out any) (ExecResult, error) {
	var a map[string]any
	if args != nil {
		b, err := json.Marshal(args)
		if err != nil {
			return ExecResult{}, Errorf(CodeBadRequest, "marshal: %v", err)
		}
		if err := json.Unmarshal(b, &a); err != nil {
			return ExecResult{}, Errorf(CodeBadRequest, "args must be an object: %v", err)
		}
	}
	var res ExecResult
	if err := h.call(FnServiceCall, ServiceCallRequest{Target: target, Action: action, Args: a}, &res); err != nil {
		return res, err
	}
	if out != nil && res.Structured != nil {
		b, _ := json.Marshal(res.Structured)
		if err := json.Unmarshal(b, out); err != nil {
			return res, Errorf(CodeBadRequest, "%s.%s result: %v", target, action, err)
		}
	}
	return res, nil
}
