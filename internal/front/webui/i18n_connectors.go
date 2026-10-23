// SPDX-License-Identifier: AGPL-3.0-only

package webui

import (
	"maps"

	"github.com/gate4ai/gate4ai/sdk-go/manifest"
)

// Названия и переводы официальных коннекторов (browser, email), которых нет в
// их манифестах и в каталоге магазина. Своё i18n в манифесте важнее этих строк.

type actText struct{ Title, RuTitle, RuDesc string }

type connText struct {
	Title, RuTitle, RuDesc string
	Actions                map[string]actText
}

var officialText = map[string]connText{
	"browser": {Title: "Browser", RuTitle: "Браузер",
		RuDesc: "Страницы в отдельном профиле Chrome: сеансы браузера для агентов и действия на странице для других коннекторов",
		Actions: map[string]actText{
			"read":            {"Read a page", "Прочитать страницу", "Открыть страницу на разрешённом сайте и вернуть её текст"},
			"screenshot":      {"Take a screenshot", "Сделать снимок", "Снимок одного элемента страницы (CSS-селектор); картинка возвращается и сохраняется как доказательство"},
			"extract":         {"Extract data from a page", "Извлечь данные со страницы", "Извлечь данные со страницы по инструкции (моделью из настроек владельца)"},
			"open":            {"Open a page", "Открыть страницу", "Открыть страницу; без сеанса начинает сеанс браузера, страница держится до browser.close"},
			"observe":         {"Look at the open page", "Посмотреть открытую страницу", "Адрес, заголовок и текст открытой страницы, по запросу — элементы для действий и структура"},
			"act":             {"Click or type on a page", "Нажать или ввести на странице", "Нажать, заполнить, ввести с клавиатуры, нажать клавишу или дождаться элемента на открытой странице"},
			"network":         {"See the page's requests", "Посмотреть запросы страницы", "Запросы открытой страницы к разрешённым сайтам — чтобы найти API сайта; токены и пароли скрыты"},
			"login":           {"Sign in to a site", "Войти на сайт", "Войти на открытой странице входа с учёткой, которую владелец задал в gate4.ai; значения не показываются"},
			"fill_secret":     {"Enter one account field", "Ввести поле учётки", "Ввести одно поле учётки в поле страницы без отправки (многошаговый вход); код может спросить у человека"},
			"page_extract":    {"Extract data from the open page", "Извлечь данные с открытой страницы", "Извлечь данные с открытой страницы по инструкции (моделью из настроек владельца)"},
			"page_screenshot": {"Screenshot the open page", "Снимок открытой страницы", "Снимок элемента, окна или всей открытой страницы"},
			"close":           {"Close the browser session", "Закрыть сеанс браузера", "Закончить сеанс браузера и вернуть записанные шаги"},
		}},
	"email": {Title: "Email", RuTitle: "Почта", RuDesc: "Почта по IMAP/SMTP",
		Actions: map[string]actText{
			"list":  {"List emails", "Посмотреть письма", "Письма в папке, новые первыми"},
			"read":  {"Read an email", "Прочитать письмо", "Прочитать одно письмо (не отмечает прочитанным)"},
			"draft": {"Save a draft", "Сохранить черновик", "Сохранить черновик (без отправки)"},
			"send":  {"Send an email", "Отправить письмо", "Отправить письмо"},
		}},
}

// localized — манифест с названиями и переводами официального коннектора, если
// в самом манифесте их нет.
func localized(m manifest.Manifest) manifest.Manifest {
	t, ok := officialText[m.Connector]
	if !ok {
		return m
	}
	if m.Title == "" {
		m.Title = t.Title
	}
	if _, has := m.I18n[langRU]; !has {
		i := map[string]manifest.Text{}
		maps.Copy(i, m.I18n)
		i[langRU] = manifest.Text{Title: t.RuTitle, Description: t.RuDesc}
		m.I18n = i
	}
	acts := make([]manifest.Action, len(m.Actions))
	for n, a := range m.Actions {
		if at, ok := t.Actions[a.Name]; ok {
			if a.Title == "" {
				a.Title = at.Title
			}
			if _, has := a.I18n[langRU]; !has {
				i := map[string]manifest.Text{}
				maps.Copy(i, a.I18n)
				i[langRU] = manifest.Text{Title: at.RuTitle, Description: at.RuDesc}
				a.I18n = i
			}
		}
		acts[n] = a
	}
	m.Actions = acts
	return m
}

// connTitle — человеческое название коннектора на языке l.
func (u *UI) connTitle(l, name string) string {
	if c, ok := u.D.Registry.Get(name); ok {
		return localized(c.Manifest()).TitleIn(l)
	}
	if t, ok := officialText[name]; ok {
		if l == langRU {
			return t.RuTitle
		}
		return t.Title
	}
	return name
}

// connDescription — описание коннектора на языке l (desc — из каталога магазина).
func connDescription(l, name, desc string) string {
	if t, ok := officialText[name]; ok && l == langRU && t.RuDesc != "" {
		return t.RuDesc
	}
	return desc
}

// actionText — название и описание действия «коннектор.действие» на языке l.
func (u *UI) actionText(l, full string) (title, desc string) {
	c, a, ok := u.D.Registry.Lookup(full)
	if !ok {
		return full, ""
	}
	m := localized(c.Manifest())
	for _, x := range m.Actions {
		if x.Name == a.Name {
			return x.TitleIn(l), x.DescriptionIn(l)
		}
	}
	return a.TitleIn(l), a.DescriptionIn(l)
}
