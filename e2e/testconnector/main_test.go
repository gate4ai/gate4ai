// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"testing"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
	"github.com/gate4ai/gate4ai/sdk-go/hosttest"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"
)

// host — тестовый хост: браузер отвечает шагами из testdata/session.json
// (так их отдаёт browser.close по сеансу агента на тестовом сайте).
func host(t *testing.T, settings map[string]any) *hosttest.Host {
	t.Helper()
	m, err := manifest.Parse(manifestYAML)
	if err != nil {
		t.Fatal(err)
	}
	fx, err := hosttest.LoadFixture("testdata/session.json")
	if err != nil {
		t.Fatal(err)
	}
	h := hosttest.New(m, settings, nil)
	h.Services = hosttest.Replay("browser", fx)
	return h
}

var settings = map[string]any{"domains": []any{"127.0.0.1"}, "site": "http://127.0.0.1:8099", "login_site": "http://127.0.0.1:8099"}

// read: строки таблицы без заголовка; cabinet: вход по login_required;
// press: номер заявки из строки результата, verify подтверждает его по странице.
func TestActions(t *testing.T) {
	h := host(t, settings)
	res, err := h.Execute(site{}, "read", nil)
	if err != nil {
		t.Fatal(err)
	}
	out := res.Structured.(map[string]any)
	rows, _ := out["rows"].([]any)
	first, _ := rows[0].(map[string]any)
	if out["title"] != "Показания" || len(rows) != 3 || first["meter"] != "Электричество" || first["value"] != "1 234" {
		t.Fatalf("read: %+v", out)
	}

	res, err = h.Execute(site{}, "cabinet", nil)
	if err != nil {
		t.Fatal(err)
	}
	out = res.Structured.(map[string]any)
	if out["status"] != "logged_in" || out["title"] != "Кабинет" {
		t.Fatalf("cabinet: %+v", out)
	}

	res, err = h.Execute(site{}, "press", nil)
	if err != nil {
		t.Fatal(err)
	}
	out = res.Structured.(map[string]any)
	if out["number"] != float64(1) || out["result"] != "Заявка №1 принята" || res.ResultRef != "1" {
		t.Fatalf("press: %+v", res)
	}
	v, err := h.Verify(site{}, "press", nil, res)
	if err != nil || v.Status != abi.Verified || v.Method != "cabinet_page" {
		t.Fatalf("verify: %+v %v", v, err)
	}
}

// Другой номер на странице — not_confirmed, а не ошибка.
func TestVerifyNotConfirmed(t *testing.T) {
	h := host(t, settings)
	v, err := h.Verify(site{}, "press", nil, abi.ExecResult{ResultRef: "7"})
	if err != nil || v.Status != abi.NotConfirmed {
		t.Fatalf("verify: %+v %v", v, err)
	}
}

// --url перебивает настройку; без того и другого — понятная ошибка.
func TestSiteAddress(t *testing.T) {
	h := host(t, map[string]any{"domains": []any{"127.0.0.1"}, "login_site": "http://127.0.0.1:8099"})
	if _, err := h.Execute(site{}, "read", nil); err == nil {
		t.Fatal("no site: expected an error")
	}
	if _, err := h.Execute(site{}, "read", map[string]any{"url": "http://127.0.0.1:8099/"}); err != nil {
		t.Fatal(err)
	}
}
