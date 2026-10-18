// SPDX-License-Identifier: AGPL-3.0-only

package config

import "testing"

func TestInstallDevChange(t *testing.T) {
	if err := CheckChange(Change{Op: OpInstallDev}); err == nil {
		t.Fatal("folder is required")
	}
	if err := CheckChange(Change{Op: OpInstallDev, Folder: "rel/dir"}); err == nil {
		t.Fatal("relative folder")
	}
	if err := CheckChange(Change{Op: OpInstallDev, Folder: "/home/a/lk"}); err != nil {
		t.Fatal(err)
	}
	if d := (Change{Op: OpInstallDev, Folder: "/home/a/lk", Connector: "lk"}).Describe(); d != "Собрать и поставить свою сборку из /home/a/lk (коннектор lk) и дать к ней доступ" {
		t.Fatal(d)
	}
}
