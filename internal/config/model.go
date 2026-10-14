// SPDX-License-Identifier: FSL-1.1-ALv2

// Package config — модель конфигурации gate4.ai (агенты, профили, правила, списки,
// лимиты, подтверждения), загрузка из YAML с проверкой схемы и Config Store с версиями.
package config

import (
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

// Эффекты правил.
const (
	EffectAllow = "allow"
	EffectDeny  = "deny"
	EffectAsk   = "ask"
)

// Известные обязательства (obligations) правил.
var KnownObligations = map[string]bool{
	"verify_statement": true, // проверить операцию по выписке
	"verify":           true, // обязательная проверка результата коннектором
	"mask_fields":      true, // маскировать чувствительные поля в ответе
	"limit":            true, // учитывать в счётчиках лимитов
}

// Config — вся конфигурация пользователя.
type Config struct {
	Agents     map[string]Agent          `yaml:"agents" json:"agents"`
	Lists      map[string][]string       `yaml:"lists,omitempty" json:"lists,omitempty"`
	Profiles   map[string]Profile        `yaml:"profiles" json:"profiles"`
	Approvals  Approvals                 `yaml:"approvals,omitempty" json:"approvals"`
	Connectors map[string]map[string]any `yaml:"connectors,omitempty" json:"connectors,omitempty"`
	Retention  Retention                 `yaml:"retention,omitempty" json:"retention"`
	Relay      Relay                     `yaml:"relay,omitempty" json:"relay"`
	Egress     Egress                    `yaml:"egress,omitempty" json:"egress"`
	Browser    Browser                   `yaml:"browser,omitempty" json:"browser"`
	Store      ConnectorStore            `yaml:"store,omitempty" json:"store"`
}

// ConnectorStore — магазин коннекторов: индекс registry и ключи, которым владелец доверяет.
type ConnectorStore struct {
	Registry     string   `yaml:"registry,omitempty" json:"registry,omitempty"`           // URL или путь к index.json
	RegistryKeys []string `yaml:"registry_keys,omitempty" json:"registry_keys,omitempty"` // ключи подписи индекса (ed25519:…)
	TrustedKeys  []string `yaml:"trusted_keys,omitempty" json:"trusted_keys,omitempty"`   // ключи издателей вне registry
}

// Browser — настройки браузерного воркера (службы хоста для функций browser_*).
// Разрешённые сайты задаются в настройках коннектора (connectors.browser.allowed_domains).
type Browser struct {
	Engine       string `yaml:"engine,omitempty" json:"engine,omitempty"`           // stagehand (по умолчанию) | cdp
	CDPURL       string `yaml:"cdp_url,omitempty" json:"cdp_url,omitempty"`         // уже запущенный Chrome
	ChromePath   string `yaml:"chrome_path,omitempty" json:"chrome_path,omitempty"` // путь к Chrome
	ProfileDir   string `yaml:"profile_dir,omitempty" json:"profile_dir,omitempty"` // выделенный профиль
	Model        string `yaml:"model,omitempty" json:"model,omitempty"`             // модель для extract
	ModelKey     string `yaml:"model_key,omitempty" json:"model_key,omitempty"`     // алиас Vault ключа модели
	MaxTextChars int    `yaml:"max_text_chars,omitempty" json:"max_text_chars,omitempty"`
}

// Agent — агент и его профиль прав.
type Agent struct {
	Profile     string `yaml:"profile" json:"profile"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
}

// Profile — слой ролей: доступные коннекторы, правила, лимиты.
type Profile struct {
	Connectors []string `yaml:"connectors" json:"connectors"`
	Rules      []Rule   `yaml:"rules,omitempty" json:"rules,omitempty"`
	Limits     []Limit  `yaml:"limits,omitempty" json:"limits,omitempty"`
	Sandbox    Sandbox  `yaml:"sandbox,omitempty" json:"sandbox"`
}

// Rule — правило ABAC: действие + условие CEL + эффект.
type Rule struct {
	ID          string   `yaml:"id" json:"id"`
	Action      string   `yaml:"action" json:"action"` // точное имя или glob: "payments.*"
	When        string   `yaml:"when,omitempty" json:"when,omitempty"`
	Effect      string   `yaml:"effect" json:"effect"`
	Obligations []string `yaml:"obligations,omitempty" json:"obligations,omitempty"`
	Reason      string   `yaml:"reason,omitempty" json:"reason,omitempty"`
}

// Limit — лимит профиля; разворачивается в правило deny.
type Limit struct {
	ID           string  `yaml:"id" json:"id"`
	Action       string  `yaml:"action" json:"action"`
	PerDay       int     `yaml:"per_day,omitempty" json:"per_day,omitempty"`               // не больше N действий за сутки
	AmountPerDay float64 `yaml:"amount_per_day,omitempty" json:"amount_per_day,omitempty"` // сумма amount за сутки
	Reason       string  `yaml:"reason,omitempty" json:"reason,omitempty"`
}

// Sandbox — настройки изоляции агента с этим профилем (gate4ai run).
type Sandbox struct {
	AllowDomains []string `yaml:"allow_domains,omitempty" json:"allow_domains,omitempty"`
	AskDomains   []string `yaml:"ask_domains,omitempty" json:"ask_domains,omitempty"`
}

// Approvals — каналы и таймаут подтверждений.
type Approvals struct {
	Channels  []string  `yaml:"channels,omitempty" json:"channels,omitempty"`
	Timeout   Duration  `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	OnTimeout string    `yaml:"on_timeout,omitempty" json:"on_timeout,omitempty"`
	Telegram  *Telegram `yaml:"telegram,omitempty" json:"telegram,omitempty"`
}

// Telegram — личный бот пользователя.
type Telegram struct {
	Token   string `yaml:"token" json:"token"`       // алиас Vault, например "@telegram.bot"
	OwnerID int64  `yaml:"owner_id" json:"owner_id"` // Telegram ID владельца
	// Webhook — получать обновления вебхуком на публичный адрес устройства
	// (<адрес>/telegram) вместо long polling; без публичного адреса — polling.
	Webhook bool `yaml:"webhook,omitempty" json:"webhook,omitempty"`
}

// Retention — сроки хранения журнала; 0 — бессрочно.
type Retention struct {
	Receipts       Duration `yaml:"receipts,omitempty" json:"receipts,omitempty"`
	EvidenceMoney  Duration `yaml:"evidence_money,omitempty" json:"evidence_money,omitempty"`
	EvidenceOther  Duration `yaml:"evidence_other,omitempty" json:"evidence_other,omitempty"`
	Denied         Duration `yaml:"denied,omitempty" json:"denied,omitempty"`
	CheckpointEach int      `yaml:"checkpoint_each,omitempty" json:"checkpoint_each,omitempty"`
}

// Relay — подключение устройства к relay.
type Relay struct {
	URL        string `yaml:"url,omitempty" json:"url,omitempty"`
	DeviceID   string `yaml:"device_id,omitempty" json:"device_id,omitempty"`
	Token      string `yaml:"token,omitempty" json:"token,omitempty"`             // алиас Vault
	Email      string `yaml:"email,omitempty" json:"email,omitempty"`             // контакт ACME
	ACMECA     string `yaml:"acme_ca,omitempty" json:"acme_ca,omitempty"`         // по умолчанию Let's Encrypt
	CAFile     string `yaml:"ca_file,omitempty" json:"ca_file,omitempty"`         // свой CA relay (self-hosted, разработка)
	SelfSigned bool   `yaml:"self_signed,omitempty" json:"self_signed,omitempty"` // сертификат устройства без ACME (разработка)
}

// Egress — секреты, подставляемые Egress Proxy по домену.
type Egress struct {
	Secrets []EgressSecret `yaml:"secrets,omitempty" json:"secrets,omitempty"`
}

// EgressSecret — заглушку Placeholder в заголовке Header запроса к Domain
// заменить значением алиаса Secret.
type EgressSecret struct {
	Domain      string `yaml:"domain" json:"domain"`
	Header      string `yaml:"header" json:"header"`
	Placeholder string `yaml:"placeholder" json:"placeholder"`
	Secret      string `yaml:"secret" json:"secret"`
}

// Duration — time.Duration в YAML как "30m", "90d" (d — сутки).
type Duration time.Duration

// D возвращает time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// UnmarshalYAML разбирает "30m", "24h", "90d", "3y".
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := ParseDuration(n.Value)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

// MarshalYAML пишет длительность строкой.
func (d Duration) MarshalYAML() (any, error) { return FormatDuration(time.Duration(d)), nil }

// ParseDuration понимает единицы Go и d (сутки), y (365 суток).
func ParseDuration(s string) (time.Duration, error) {
	if s == "" || s == "0" {
		return 0, nil
	}
	var n float64
	var unit string
	if _, err := fmt.Sscanf(s, "%g%s", &n, &unit); err == nil {
		switch unit {
		case "d":
			return time.Duration(n * float64(24*time.Hour)), nil
		case "y":
			return time.Duration(n * float64(365*24*time.Hour)), nil
		}
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q (examples: 30m, 24h, 90d, 3y)", s)
	}
	return v, nil
}

// FormatDuration — обратное к ParseDuration для целых суток.
func FormatDuration(d time.Duration) string {
	day := 24 * time.Hour
	if d > 0 && d%day == 0 {
		return fmt.Sprintf("%dd", d/day)
	}
	return d.String()
}

// Defaults заполняет значения по умолчанию из спецификации (docs/specification.md).
func (c *Config) Defaults() {
	if c.Approvals.Timeout == 0 {
		c.Approvals.Timeout = Duration(30 * time.Minute)
	}
	if c.Approvals.OnTimeout == "" {
		c.Approvals.OnTimeout = EffectDeny
	}
	if len(c.Approvals.Channels) == 0 {
		c.Approvals.Channels = []string{"desktop"}
	}
	day := 24 * time.Hour
	if c.Retention.EvidenceMoney == 0 {
		c.Retention.EvidenceMoney = Duration(3 * 365 * day)
	}
	if c.Retention.EvidenceOther == 0 {
		c.Retention.EvidenceOther = Duration(90 * day)
	}
	if c.Retention.Denied == 0 {
		c.Retention.Denied = Duration(365 * day)
	}
	if c.Retention.CheckpointEach == 0 {
		c.Retention.CheckpointEach = 100
	}
}

// ProfileOf возвращает профиль агента.
func (c *Config) ProfileOf(agent string) (Profile, bool) {
	a, ok := c.Agents[agent]
	if !ok {
		return Profile{}, false
	}
	p, ok := c.Profiles[a.Profile]
	return p, ok
}

// HasConnector — доступен ли коннектор профилю.
func (p Profile) HasConnector(name string) bool {
	for _, c := range p.Connectors {
		if c == name {
			return true
		}
	}
	return false
}
