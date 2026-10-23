// SPDX-License-Identifier: AGPL-3.0-only

//go:build windows

package procinfo

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

// lookup — родитель и бинарник из снимка процессов; окружение и текущий
// каталог чужого процесса на Windows не читаются (агент узнаётся по имени).
func lookup(pid int, keys []string) (Proc, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return Proc{}, err
	}
	defer windows.CloseHandle(snap)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if int(e.ProcessID) != pid {
			continue
		}
		p := Proc{PID: pid, PPID: int(e.ParentProcessID), Name: windows.UTF16ToString(e.ExeFile[:])}
		if h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid)); err == nil {
			buf := make([]uint16, windows.MAX_LONG_PATH)
			n := uint32(len(buf))
			if windows.QueryFullProcessImageName(h, 0, &buf[0], &n) == nil {
				p.Exe = windows.UTF16ToString(buf[:n])
			}
			windows.CloseHandle(h)
		}
		return p, nil
	}
	return Proc{}, errors.New("procinfo: no such process")
}

func cwdOf(pid int) string { return "" }
