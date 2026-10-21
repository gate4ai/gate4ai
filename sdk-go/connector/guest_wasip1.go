// SPDX-License-Identifier: Apache-2.0

//go:build wasip1

package connector

import (
	"unsafe"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
)

//go:wasmimport gate4ai host_call
func hostCall(fnPtr, fnLen, reqPtr, reqLen uint32) int32

//go:wasmimport gate4ai host_result
func hostResult(dst uint32)

// keep держит буферы, переданные хосту, до конца вызова (экземпляр модуля —
// на один вызов, поэтому буферы не освобождаются).
var keep [][]byte

func ptr(b []byte) uint32 {
	if len(b) == 0 {
		return 0
	}
	return uint32(uintptr(unsafe.Pointer(unsafe.SliceData(b))))
}

func ret(b []byte) uint64 {
	keep = append(keep, b)
	return uint64(ptr(b))<<32 | uint64(len(b))
}

type guest struct{}

func (guest) Call(fn string, req []byte) []byte {
	f := []byte(fn)
	n := hostCall(ptr(f), uint32(len(f)), ptr(req), uint32(len(req)))
	out := make([]byte, n)
	if n > 0 {
		hostResult(ptr(out))
	}
	return out
}

// allocated — буферы для входных данных, выделенные хосту по gate4ai_alloc.
var allocated = map[uint32][]byte{}

//go:wasmexport gate4ai_alloc
func alloc(n uint32) uint32 {
	if n == 0 {
		n = 1
	}
	b := make([]byte, n)
	p := ptr(b)
	allocated[p] = b
	return p
}

func input(p, n uint32) []byte {
	b, ok := allocated[p]
	if !ok || uint32(len(b)) < n {
		return nil
	}
	delete(allocated, p)
	return b[:n]
}

//go:wasmexport gate4ai_manifest
func exportManifest() uint64 {
	return ret(Handle(registered.m, registered.c, guest{}, OpManifest, nil))
}

//go:wasmexport gate4ai_execute
func exportExecute(p, n uint32) uint64 {
	return ret(Handle(registered.m, registered.c, guest{}, OpExecute, input(p, n)))
}

//go:wasmexport gate4ai_verify
func exportVerify(p, n uint32) uint64 {
	return ret(Handle(registered.m, registered.c, guest{}, OpVerify, input(p, n)))
}

var _ abi.Transport = guest{}
