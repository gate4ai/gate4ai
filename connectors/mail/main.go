// SPDX-License-Identifier: AGPL-3.0-only

// Коннектор gate4.ai «Почта» (IMAP/SMTP с паролем приложения): чтение,
// черновики, отправка; входящий контент помечается как недоверенный; отправка
// проверяется по папке «Отправленные». Соединения открывает хост (tls_dial),
// он же выполняет вход по алиасу секрета: пароль коннектору не виден.
//
// Лицензия: AGPL-3.0-only.
package main

import (
	_ "embed"

	"github.com/gate4ai/gate4ai/sdk-go/connector"
)

//go:embed manifest.yaml
var manifestYAML []byte

func init() { connector.Register(manifestYAML, &Mail{}) }

func main() {}
