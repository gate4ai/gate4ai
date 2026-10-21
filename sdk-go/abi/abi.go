// SPDX-License-Identifier: Apache-2.0

// Package abi — ABI v2 между хостом gate4.ai и коннектором: экспорты модуля,
// функции хоста и их запросы/ответы. Всё передаётся в JSON.
//
// Экспорты WASM-модуля (Go: GOOS=wasip1, -buildmode=c-shared; хост вызывает
// _initialize, затем экспорты; один экземпляр модуля — один вызов):
//
//	gate4ai_alloc(size i32) i32          — буфер в памяти модуля для входных данных
//	gate4ai_manifest() i64               — манифест (JSON)
//	gate4ai_execute(ptr, len i32) i64    — ExecRequest → Response{ExecResult}
//	gate4ai_verify(ptr, len i32) i64     — VerifyRequest → Response{VerifyResult}
//
// Результат i64 — (ptr << 32) | len ответа в памяти модуля.
//
// Импорты модуля (модуль "gate4ai"):
//
//	host_call(fn_ptr, fn_len, req_ptr, req_len i32) i32 — вызов функции хоста fn
//	    с запросом JSON; возвращает длину ответа (Response), ответ ждёт у хоста;
//	host_result(dst i32)                                 — копирует ответ в память модуля.
package abi

import (
	"encoding/json"
	"fmt"

	"github.com/gate4ai/gate4ai/sdk-go/manifest"
)

// Version — версия ABI.
const Version = 2

// Имена экспортов и импортов.
const (
	ExportAlloc    = "gate4ai_alloc"
	ExportManifest = "gate4ai_manifest"
	ExportExecute  = "gate4ai_execute"
	ExportVerify   = "gate4ai_verify"
	ImportModule   = "gate4ai"
	ImportCall     = "host_call"
	ImportResult   = "host_result"
)

// Функции хоста.
const (
	FnHTTPRequest = "http_request"
	FnTLSDial     = "tls_dial"
	FnConnRead    = "conn_read"
	FnConnWrite   = "conn_write"
	FnConnClose   = "conn_close"
	FnFSRead      = "fs_read"
	FnFSWrite     = "fs_write"
	FnFSAppend    = "fs_append"
	FnFSList      = "fs_list"
	FnFSStat      = "fs_stat"
	FnKVGet       = "kv_get"
	FnKVSet       = "kv_set"
	FnKVDelete    = "kv_delete"
	FnEvidenceAdd = "evidence_add"
	FnLog         = "log"
	FnServiceCall = "service_call"
)

// Коды ошибок.
const (
	CodeCapabilityDenied = "capability_denied" // возможность не объявлена в манифесте или сужена политикой
	CodeNotSupported     = "not_supported"     // функция не поддерживается хостом
	CodeBadRequest       = "bad_request"       // неверный запрос
	CodeNotFound         = "not_found"         // файл, соединение, ключ не найдены
	CodeIO               = "io_error"          // ошибка сети или файловой системы
	CodeAuth             = "auth_failed"       // аутентификация хостом не прошла
	CodeLimit            = "limit_exceeded"    // квота, размер, время
	CodeConnector        = "connector_error"   // ошибка самого коннектора
	CodeSecretDenied     = "secret_denied"     // учётка недоступна вызывающему (или не задана)
	CodeCancelled        = "cancelled"         // вызов отменён: запрос, который его начал, закончился
)

// Error — ошибка ABI.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Errorf — ошибка с кодом.
func Errorf(code, f string, a ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(f, a...)}
}

// Response — ответ экспорта модуля и функции хоста.
type Response struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

// OK — успешный ответ.
func OK(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return Fail(Errorf(CodeBadRequest, "marshal: %v", err))
	}
	out, _ := json.Marshal(Response{Result: b})
	return out
}

// Fail — ответ с ошибкой.
func Fail(err error) []byte {
	e, ok := err.(*Error)
	if !ok {
		e = &Error{Code: CodeConnector, Message: err.Error()}
	}
	out, _ := json.Marshal(Response{Error: e})
	return out
}

// Decode разбирает ответ в v.
func Decode(resp []byte, v any) error {
	var r Response
	if err := json.Unmarshal(resp, &r); err != nil {
		return Errorf(CodeBadRequest, "bad response: %v", err)
	}
	if r.Error != nil {
		return r.Error
	}
	if v == nil || len(r.Result) == 0 {
		return nil
	}
	return json.Unmarshal(r.Result, v)
}

// ExecRequest — исполнение действия. Settings — настройки экземпляра коннектора
// из конфигурации как есть: секреты в них — только алиасы (@name), значения
// подставляет хост в функциях хоста.
type ExecRequest struct {
	Action   string         `json:"action"`
	Args     map[string]any `json:"args"`
	Settings map[string]any `json:"settings"`
	// Caller — вызывающий коннектор, если действие вызвано через service_call.
	Caller *Caller `json:"caller,omitempty"`
	// Revealed — значения алиасов из capabilities.reveal (только runtime native);
	// для browser.login и browser.fill_secret — ещё значения секретных полей
	// учётки Login (по алиасу секрета) и ответ человека (HumanValue).
	Revealed map[string]string `json:"revealed,omitempty"`
	// Accounts — учётки входа на сайты, доступные вызывающему (без значений и
	// полей формы): на origin из их bind страница браузера может находиться.
	// Хост передаёт их только нативному коннектору browser.
	Accounts []Account `json:"accounts,omitempty"`
	// Login — учётка запрошенного входа с формой (только browser.login и
	// browser.fill_secret): поля, отправка, признак успеха; значения секретных
	// полей — в Revealed.
	Login *Account `json:"login,omitempty"`
}

// HumanValue — ключ Revealed со значением, которое человек ввёл в ответ на
// вопрос хоста (browser.fill_secret с source: human).
const HumanValue = "human"

// Account — учётка входа на сайт из конфигурации владельца (credentials):
// где разрешён ввод (Bind — точные origin) и что вводить. Значений секретов
// здесь нет: поле с Secret называет алиас Vault.
type Account struct {
	Alias string   `json:"alias"`
	Bind  []string `json:"bind"`
	// Via — сложный вход коннектором входа ("<коннектор>.<действие>"): хост
	// вызывает его вместо формы.
	Via     string         `json:"via,omitempty"`
	Fields  []AccountField `json:"fields,omitempty"`
	Submit  *AccountSubmit `json:"submit,omitempty"`
	Success string         `json:"success_url_prefix,omitempty"` // признак успеха: адрес начинается с него
	// LoginPage — признак страницы входа: login_required ставится только на
	// ней, а не на любой странице сайтов Bind.
	LoginPage *AccountPage `json:"login_page,omitempty"`
}

// AccountPage — страница входа: видимый элемент Selector или адрес с началом URLPrefix.
type AccountPage struct {
	Selector  string `json:"selector,omitempty"`
	URLPrefix string `json:"url_prefix,omitempty"`
}

// AccountField — поле формы входа: константа (Value) или секрет (Secret).
type AccountField struct {
	Name     string `json:"name"` // имя для fill_secret: явное или алиас секрета
	Selector string `json:"selector"`
	Value    string `json:"value,omitempty"`
	Secret   string `json:"secret,omitempty"`
	// Type text — секрет можно ввести не только в input[type=password].
	Type string `json:"type,omitempty"`
}

// AccountSubmit — отправка формы: клик по Selector или клавиша Press в последнем поле.
type AccountSubmit struct {
	Selector string `json:"selector,omitempty"`
	Press    string `json:"press,omitempty"`
}

// EvidenceItem — доказательство (файл).
type EvidenceItem struct {
	Name string `json:"name"`
	MIME string `json:"mime"`
	Data []byte `json:"data"`
}

// Типы блоков содержимого — как у блоков content результата инструмента MCP.
const (
	ContentText         = "text"          // Text
	ContentImage        = "image"         // Data + MIMEType (image/*)
	ContentResource     = "resource"      // встроенный ресурс: URI + MIMEType + Text или Data
	ContentResourceLink = "resource_link" // ссылка на ресурс: URI + Name (+ MIMEType)
)

// Content — блок содержимого результата для агента.
type Content struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     []byte `json:"data,omitempty"`
	MIMEType string `json:"mime_type,omitempty"`
	URI      string `json:"uri,omitempty"`
	Name     string `json:"name,omitempty"`
}

// Text — текстовый блок.
func Text(s string) Content { return Content{Type: ContentText, Text: s} }

// Image — изображение.
func Image(mime string, data []byte) Content {
	return Content{Type: ContentImage, MIMEType: mime, Data: data}
}

// Resource — встроенный ресурс: текст (data == nil) или двоичные данные.
func Resource(uri, mime, text string, data []byte) Content {
	return Content{Type: ContentResource, URI: uri, MIMEType: mime, Text: text, Data: data}
}

// ResourceLink — ссылка на ресурс.
func ResourceLink(uri, name, mime string) Content {
	return Content{Type: ContentResourceLink, URI: uri, Name: name, MIMEType: mime}
}

// Validate проверяет блок.
func (c Content) Validate() error {
	switch c.Type {
	case ContentText:
		if len(c.Data) > 0 || c.URI != "" {
			return Errorf(CodeBadRequest, "text content: only text is allowed")
		}
	case ContentImage:
		if len(c.Data) == 0 || len(c.MIMEType) < 7 || c.MIMEType[:6] != "image/" {
			return Errorf(CodeBadRequest, "image content: data and image/* mime_type are required")
		}
	case ContentResource:
		if c.URI == "" || (c.Text != "" && len(c.Data) > 0) {
			return Errorf(CodeBadRequest, "resource content: uri and either text or data are required")
		}
	case ContentResourceLink:
		if c.URI == "" || c.Name == "" {
			return Errorf(CodeBadRequest, "resource_link content: uri and name are required")
		}
	default:
		return Errorf(CodeBadRequest, "unknown content type %q", c.Type)
	}
	return nil
}

// ExecResult — результат исполнения: содержимое для агента (Content) и/или
// структурированный результат (Structured, по output_schema действия, если она
// объявлена) — как content и structuredContent результата инструмента MCP.
// VerifyAfterS > 0 — действие отправлено, но синхронно проверить его нельзя:
// хост вызовет verify через VerifyAfterS секунд (verify_after).
type ExecResult struct {
	Content      []Content      `json:"content,omitempty"`
	Structured   any            `json:"structured,omitempty"`
	ResultRef    string         `json:"result_ref,omitempty"` // идентификатор результата (Message-ID, путь, URL)
	Evidence     []EvidenceItem `json:"evidence,omitempty"`
	Source       string         `json:"source,omitempty"` // источник недоверенного контента: "email:<id>", "web:<url>"
	VerifyAfterS int            `json:"verify_after_s,omitempty"`
}

// CheckResult проверяет результат коннектора: блоки содержимого и
// структурированный результат по output_schema действия (если она объявлена,
// результат обязателен — как structuredContent при outputSchema в MCP).
func CheckResult(r ExecResult, out *manifest.Schema) error {
	for _, c := range r.Content {
		if err := c.Validate(); err != nil {
			return err
		}
	}
	if r.VerifyAfterS < 0 {
		return Errorf(CodeBadRequest, "verify_after_s must not be negative")
	}
	if out == nil {
		return nil
	}
	if r.Structured == nil {
		return Errorf(CodeBadRequest, "structured result is required by output_schema")
	}
	// Сверяем форму после JSON, как её увидит агент.
	b, err := json.Marshal(r.Structured)
	if err != nil {
		return Errorf(CodeBadRequest, "structured result: %v", err)
	}
	var v any
	_ = json.Unmarshal(b, &v)
	if err := out.Check(v); err != nil {
		return Errorf(CodeBadRequest, "structured result does not match output_schema: %v", err)
	}
	return nil
}

// VerifyRequest — проверка результата по независимому каналу.
type VerifyRequest struct {
	Action     string         `json:"action"`
	Args       map[string]any `json:"args"`
	Settings   map[string]any `json:"settings"`
	ResultRef  string         `json:"result_ref"`
	Structured any            `json:"structured,omitempty"`
}

// Статусы проверки, которые возвращает verify коннектора.
const (
	Verified     = "verified"      // действие подтверждено
	NotConfirmed = "not_confirmed" // действие не подтверждено (не прошло, отклонено)
	VerifyAfter  = "verify_after"  // пока не ясно: повторить через VerifyAfterS секунд
)

// Статусы проверки, которые ставит хост.
const (
	Unverified = "unverified" // проверить не удалось (ошибка verify, исчерпан срок)
	Pending    = "pending"    // проверка отложена (verify_after)
)

// VerifyResult — итог проверки. Ошибка verify (а не NotConfirmed) — проверить
// не удалось: сеть, таймаут, сбой сервиса.
type VerifyResult struct {
	Status       string         `json:"status"`
	VerifyAfterS int            `json:"verify_after_s,omitempty"`
	Method       string         `json:"method"`
	Detail       string         `json:"detail,omitempty"`
	Evidence     []EvidenceItem `json:"evidence,omitempty"`
}

// HTTPRequest — http_request. В заголовках и теле плейсхолдер {{secret:@alias}}
// заменяется хостом значением секрета, только если адрес входит в bind секрета.
// Редиректы хост не выполняет: 3xx возвращается модулю.
type HTTPRequest struct {
	Method    string            `json:"method"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers,omitempty"`
	Body      []byte            `json:"body,omitempty"`
	TimeoutMS int               `json:"timeout_ms,omitempty"`
}

// HTTPResponse — ответ http_request (значения подставленных секретов замаскированы).
type HTTPResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    []byte            `json:"body"`
}

// Режимы TLS для tls_dial.
const (
	TLSDirect   = "tls"      // TLS сразу
	TLSStartTLS = "starttls" // STARTTLS протокола (imap, smtp)
	TLSNone     = "none"     // без TLS — только loopback
)

// Протоколы tls_dial, для которых хост выполняет STARTTLS и аутентификацию.
const (
	ProtoIMAP = "imap"
	ProtoSMTP = "smtp"
)

// Схемы аутентификации хостом.
const (
	AuthIMAPLogin     = "imap-login"      // IMAP LOGIN; модуль получает приветствие PREAUTH
	AuthSMTPAuthPlain = "smtp-auth-plain" // SMTP EHLO + AUTH PLAIN; модуль начинает с MAIL FROM
)

// DialAuth — аутентификация в протоколе, выполняемая хостом.
type DialAuth struct {
	Scheme   string `json:"scheme"`
	Username string `json:"username"`
	Secret   string `json:"secret"` // алиас Vault (@name)
}

// DialRequest — tls_dial. Для Protocol imap хост читает приветствие сервера
// (и выполняет STARTTLS / LOGIN) и отдаёт модулю синтетическое приветствие:
// "* PREAUTH …" после входа или "* OK …" без него. Для smtp хост читает 220,
// выполняет EHLO (STARTTLS, AUTH) — модуль начинает сразу с MAIL FROM.
type DialRequest struct {
	Host     string    `json:"host"`
	Port     int       `json:"port"`
	TLS      string    `json:"tls"`
	Protocol string    `json:"protocol,omitempty"`
	Auth     *DialAuth `json:"auth,omitempty"`
}

// DialResponse — идентификатор соединения.
type DialResponse struct {
	Conn int `json:"conn"`
}

// ConnRead — conn_read: до Max байт, ждать данных не дольше WaitMS (≤ 200).
type ConnRead struct {
	Conn   int `json:"conn"`
	Max    int `json:"max"`
	WaitMS int `json:"wait_ms"`
}

// ConnReadResponse — прочитанные данные; пусто без EOF — данных пока нет.
type ConnReadResponse struct {
	Data []byte `json:"data,omitempty"`
	EOF  bool   `json:"eof,omitempty"`
}

// ConnWrite — conn_write.
type ConnWrite struct {
	Conn int    `json:"conn"`
	Data []byte `json:"data"`
}

// ConnWriteResponse — записано байт.
type ConnWriteResponse struct {
	N int `json:"n"`
}

// ConnClose — conn_close.
type ConnClose struct {
	Conn int `json:"conn"`
}

// FSRequest — fs_read, fs_write, fs_append, fs_list, fs_stat. Путь — абсолютный
// или ~/…; хост раскрывает ~, .. и симлинки и сверяет с capabilities.files.
type FSRequest struct {
	Path string `json:"path"`
	Data []byte `json:"data,omitempty"` // fs_write, fs_append
	Max  int64  `json:"max,omitempty"`  // fs_read: не больше Max байт (по умолчанию 1 МиБ)
}

// FSReadResponse — fs_read.
type FSReadResponse struct {
	Data      []byte `json:"data"`
	Size      int64  `json:"size"`
	Truncated bool   `json:"truncated,omitempty"`
	RealPath  string `json:"real_path"`
}

// FSEntry — элемент каталога.
type FSEntry struct {
	Name string `json:"name"`
	Dir  bool   `json:"dir"`
	Size int64  `json:"size,omitempty"`
}

// FSListResponse — fs_list.
type FSListResponse struct {
	Entries  []FSEntry `json:"entries"`
	RealPath string    `json:"real_path"`
}

// FSStatResponse — fs_stat; Size −1, если файла нет.
type FSStatResponse struct {
	Exists   bool   `json:"exists"`
	Dir      bool   `json:"dir,omitempty"`
	Size     int64  `json:"size"`
	RealPath string `json:"real_path"`
}

// FSWriteResponse — fs_write, fs_append.
type FSWriteResponse struct {
	SizeBefore int64  `json:"size_before"`
	SizeAfter  int64  `json:"size_after"`
	RealPath   string `json:"real_path"`
}

// KVRequest — kv_get, kv_set, kv_delete.
type KVRequest struct {
	Key   string `json:"key"`
	Value []byte `json:"value,omitempty"`
}

// KVResponse — kv_get.
type KVResponse struct {
	Value []byte `json:"value,omitempty"`
	Found bool   `json:"found"`
}

// LogRequest — log (значения секретов хост вырезает).
type LogRequest struct {
	Level string `json:"level"` // debug | info | warn | error
	Msg   string `json:"msg"`
}

// ServiceCallRequest — service_call: действие Action коннектора Target с
// аргументами Args (сверяются с input_schema поставщика).
type ServiceCallRequest struct {
	Target string         `json:"target"`
	Action string         `json:"action"`
	Args   map[string]any `json:"args"`
}

// Caller — кто вызвал действие через service_call (передаётся поставщику в
// ExecRequest). Call — корневой вызов: состояние поставщика (например,
// страница браузера) живёт до его конца.
type Caller struct {
	Connector string         `json:"connector"`
	Call      string         `json:"call"`
	Params    map[string]any `json:"params,omitempty"`
	// Session — сеанс браузера агента, в котором идёт вход коннектором входа
	// (browser.login {session, account} учётки с via): его страница — страница вызова.
	Session string `json:"session,omitempty"`
}
