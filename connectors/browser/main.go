// SPDX-License-Identifier: AGPL-3.0-only

// Коннектор gate4.ai «Браузер» — нативный пакет (runtime: native): браузерный
// воркер (выделенный профиль Chrome, CDP, Stagehand) в отдельном процессе под
// надзором gate4ai. Агенту — чтение, скриншот и извлечение и сеанс страницы
// (open, observe, act, network, login, fill_secret, page_extract,
// page_screenshot, close) на
// сайтах из настроек (allowed_domains / blocked); другим коннекторам — те же
// действия с открытой страницей через service_call на сайтах из их
// params.sites. Ключ модели для extract хост
// передаёт по capabilities.reveal.
//
// Лицензия: AGPL-3.0-only.
package main

import (
	_ "embed"

	"github.com/gate4ai/gate4ai/sdk-go/native"
)

//go:embed manifest.yaml
var manifestYAML []byte

func main() {
	b := New()
	native.Serve(manifestYAML, b)
	_ = b.W.Close() // daemon остановил пакет: закрыть запущенный им Chrome
}
