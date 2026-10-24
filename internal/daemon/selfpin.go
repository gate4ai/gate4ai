// SPDX-License-Identifier: AGPL-3.0-only

package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/manifest"

	"github.com/gate4ai/gate4ai/internal/config"
	"github.com/gate4ai/gate4ai/internal/connectors/store"
)

// pinsArg — служебный аргумент change_policy: то, что показали человеку в
// запросе подтверждения об устанавливаемых коннекторах (версия, дайджест,
// runtime, возможности). Его пишет daemon при проверке запроса; агент его
// задать не может — схема действия отклоняет неизвестные аргументы. При
// исполнении ставится только совпадающее с ним (code review K2).
const pinsArg = "_pinned"

// selfPin — закреплённая установка коннектора из одной правки change_policy.
type selfPin struct {
	Op           string   `json:"op"`
	Connector    string   `json:"connector"`
	Version      string   `json:"version"`
	Runtime      string   `json:"runtime"`
	Publisher    string   `json:"publisher,omitempty"`
	Trust        string   `json:"trust,omitempty"`
	Capabilities []string `json:"capabilities"`
	// Actions — классы действий: "read: a, b" (своя сборка).
	Actions []string `json:"actions,omitempty"`
	// Limits — лимиты одного вызова (своя сборка).
	Limits string `json:"limits,omitempty"`
	// ManifestSHA256, SourceSHA256 — своя сборка: манифест и исходники папки.
	ManifestSHA256 string `json:"manifest_sha256,omitempty"`
	SourceSHA256   string `json:"source_sha256,omitempty"`
	// Digest — install_connector: дайджест пакета из показанной записи индекса.
	Digest string `json:"digest,omitempty"`
	// Replaces — какой установленный коннектор заменит установка.
	Replaces string `json:"replaces,omitempty"`
}

// lines — строки запроса подтверждения об установке.
func (p selfPin) lines() []string {
	var out []string
	rt := "runtime wasm (sandbox)"
	if p.Runtime == manifest.RuntimeNative {
		rt = "runtime native: runs WITHOUT a sandbox, with your user's rights"
	}
	if p.Op == config.OpInstallDev {
		out = append(out, fmt.Sprintf("%s %s, own build (not reviewed), %s", p.Connector, p.Version, rt))
	} else {
		who := p.Publisher
		if p.Trust != "" {
			who += " (" + p.Trust + ")"
		}
		out = append(out, fmt.Sprintf("%s %s by %s, %s", p.Connector, p.Version, who, rt))
	}
	caps := "none"
	if len(p.Capabilities) > 0 {
		caps = strings.Join(p.Capabilities, "; ")
	}
	out = append(out, "capabilities: "+caps)
	if len(p.Actions) > 0 {
		out = append(out, "actions: "+strings.Join(p.Actions, "; "))
	}
	if p.Limits != "" {
		out = append(out, "limits per call: "+p.Limits)
	}
	if p.Replaces != "" {
		out = append(out, "⚠ replaces the installed "+p.Replaces)
	}
	return out
}

// matches — план установки совпадает с закреплённым: имя, версия,
// возможности и runtime.
func (p selfPin) matches(plan *store.Plan) error {
	sorted := func(s []string) []string { return slices.Sorted(slices.Values(s)) }
	switch {
	case plan.Connector != p.Connector || plan.Version != p.Version:
		return fmt.Errorf("the package is %s %s, the owner approved %s %s", plan.Connector, plan.Version, p.Connector, p.Version)
	case !slices.Equal(sorted(plan.Capabilities), sorted(p.Capabilities)):
		return errors.New("the package capabilities differ from those the owner approved; ask again")
	case plan.Native != (p.Runtime == manifest.RuntimeNative):
		return errors.New("the package runtime differs from the one the owner approved; ask again")
	}
	return nil
}

// readPins — закреплённые установки из аргументов (индекс правки → установка).
func readPins(args map[string]any) (map[int]selfPin, error) {
	pins := map[int]selfPin{}
	raw, ok := args[pinsArg]
	if !ok {
		return pins, nil
	}
	s, ok := raw.(string)
	if !ok || json.Unmarshal([]byte(s), &pins) != nil {
		return nil, errors.New("bad pinned installs")
	}
	return pins, nil
}

// pinChanges закрепляет в аргументах установки коннекторов из правок; при
// повторной проверке того же запроса возвращает уже закреплённое (текст
// запроса строится по нему, а не по новому чтению папки или индекса).
func (d *Daemon) pinChanges(args map[string]any, changes []config.Change) (map[int]selfPin, error) {
	if _, ok := args[pinsArg]; ok {
		pins, err := readPins(args)
		if err != nil {
			return nil, err
		}
		for i, p := range pins {
			if i < 0 || i >= len(changes) || changes[i].Op != p.Op {
				return nil, errors.New("pinned installs do not match the changes")
			}
		}
		return pins, nil
	}
	pins := map[int]selfPin{}
	for i, ch := range changes {
		switch ch.Op {
		case config.OpInstallDev:
			p, err := d.pinDev(ch.Folder)
			if err != nil {
				return nil, err
			}
			pins[i] = p
		case config.OpInstallConnector:
			if _, ok := d.Registry.Get(ch.Connector); ok {
				continue // уже установлен: правка только даёт доступ
			}
			p, err := d.pinStore(ch.Connector)
			if err != nil {
				return nil, err
			}
			pins[i] = p
		}
	}
	if len(pins) > 0 {
		b, _ := json.Marshal(pins)
		args[pinsArg] = string(b)
	}
	return pins, nil
}

// pinDev — снимок своей сборки в папке: манифест (runtime только wasm) и
// дайджест исходников.
func (d *Daemon) pinDev(folder string) (selfPin, error) {
	m, raw, err := devManifest(folder)
	if err != nil {
		return selfPin{}, err
	}
	dir, _ := store.SourceFolder(expandHome(folder))
	src, err := sourceDigest(dir)
	if err != nil {
		return selfPin{}, err
	}
	p := selfPin{Op: config.OpInstallDev, Connector: m.Connector, Version: m.Version, Runtime: m.Runtime,
		Capabilities: m.Summary(), Actions: actionClasses(m), Limits: limitsText(m.Limits),
		ManifestSHA256: store.SHA256(raw), SourceSHA256: src}
	if info, err := d.StoreOptions().Info(m.Connector); err == nil {
		p.Replaces = fmt.Sprintf("%s %s (trust %s)", info.Connector, info.Version, info.Trust)
	}
	return p, nil
}

// pinStore — запись индекса магазина, которую увидит человек: версия,
// дайджест, возможности. Без записи в индексе установку не предложить.
func (d *Daemon) pinStore(name string) (selfPin, error) {
	opts := d.StoreOptions()
	ix := opts.CachedIndex()
	if ix == nil || func() bool { _, _, err := ix.Resolve(name, ""); return err != nil }() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if fresh, err := opts.RefreshIndex(ctx); err == nil {
			ix = fresh
		}
	}
	if ix == nil {
		return selfPin{}, fmt.Errorf("install_connector: the store index is not available, so the owner cannot see what %s may do", name)
	}
	e, rel, err := ix.Resolve(name, "")
	if err != nil {
		return selfPin{}, fmt.Errorf("install_connector: %v", err)
	}
	rt := manifest.RuntimeWasm
	if rel.Native() {
		rt = manifest.RuntimeNative
	}
	return selfPin{Op: config.OpInstallConnector, Connector: name, Version: rel.Version, Runtime: rt, Publisher: e.Publisher,
		Trust: ix.Publishers[e.Publisher].Trust, Capabilities: slices.Clone(rel.Capabilities), Digest: rel.Digest}, nil
}

// actionClasses — действия по классам: "read: a, b".
func actionClasses(m manifest.Manifest) []string {
	by := map[string][]string{}
	for _, a := range m.Actions {
		by[a.Class] = append(by[a.Class], a.Name)
	}
	var out []string
	for _, c := range []string{"money", "external", "write", "read"} {
		if len(by[c]) > 0 {
			out = append(out, c+": "+strings.Join(by[c], ", "))
			delete(by, c)
		}
	}
	for _, c := range slices.Sorted(maps.Keys(by)) {
		out = append(out, c+": "+strings.Join(by[c], ", "))
	}
	return out
}

// limitsText — лимиты одного вызова (по умолчанию — значения ядра).
func limitsText(l manifest.Limits) string {
	mem, t := l.MemoryMB, l.TimeoutS
	if mem <= 0 {
		mem = manifest.DefaultMemoryMB
	}
	if t <= 0 {
		t = manifest.DefaultTimeoutS
	}
	return fmt.Sprintf("memory %d MB, timeout %d s", mem, t)
}

// Пределы снимка исходников своей сборки.
const (
	maxSourceFiles = 5000
	maxSourceBytes = 256 << 20
)

// sourceDigest — дайджест дерева исходников папки: пути, типы и содержимое
// файлов (ссылки — по цели, без перехода).
func sourceDigest(dir string) (string, error) {
	h := sha256.New()
	files, total := 0, int64(0)
	err := filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		rel = filepath.ToSlash(rel)
		switch {
		case e.IsDir():
			fmt.Fprintf(h, "d %s\n", rel)
			return nil
		case e.Type()&fs.ModeSymlink != 0:
			t, err := os.Readlink(path)
			if err != nil {
				return err
			}
			fmt.Fprintf(h, "l %s %s\n", rel, strconv.Quote(t))
			return nil
		case !e.Type().IsRegular():
			fmt.Fprintf(h, "o %s\n", rel)
			return nil
		}
		if files++; files > maxSourceFiles {
			return fmt.Errorf("install_dev_connector: more than %d files in %s", maxSourceFiles, dir)
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		fh := sha256.New()
		n, err := io.Copy(fh, io.LimitReader(f, maxSourceBytes-total+1))
		if err != nil {
			return err
		}
		if total += n; total > maxSourceBytes {
			return fmt.Errorf("install_dev_connector: %s is larger than %d MB", dir, maxSourceBytes>>20)
		}
		fmt.Fprintf(h, "f %s %x\n", rel, fh.Sum(nil))
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// checkCredentials — add_credential заводит только новые секреты: алиас,
// который уже есть в Vault, привязал бы существующее значение к сайту агента
// (code review H4). Остальное (перезапись учётки, алиасы политики, пространство
// имён учётки) проверяет config.
func (d *Daemon) checkCredentials(changes []config.Change) error {
	if d.Vault == nil {
		return nil
	}
	have := map[string]bool{}
	for _, a := range d.Vault.Aliases() {
		have[strings.ToLower(a)] = true
	}
	for i, ch := range changes {
		if ch.Op != config.OpAddCredential {
			continue
		}
		var cr config.Credential
		if err := json.Unmarshal([]byte(ch.CredentialJSON), &cr); err != nil {
			return fmt.Errorf("changes[%d]: %v", i, err)
		}
		for _, a := range cr.Secrets() {
			if have[strings.ToLower(a)] {
				return fmt.Errorf("changes[%d]: add_credential: secret %s is already in the vault; an agent may only create new secrets of a new account", i, a)
			}
		}
	}
	return nil
}

var settingTmplRe = regexp.MustCompile(`\$\{setting:([a-zA-Z0-9_.]+)\}`)

// bindSettings — настройки верхнего уровня, от которых зависят сеть, привязки
// секретов, раскрываемые секреты и сайты входа коннектора: ключ → возможности.
func (d *Daemon) bindSettings(conn string) map[string][]string {
	var m manifest.Manifest
	if c, ok := d.Registry.Get(conn); ok {
		m = c.Manifest()
	} else if raw, err := os.ReadFile(filepath.Join(d.StoreOptions().Dir, conn, store.ManifestFile)); err == nil {
		if m, err = manifest.Parse(raw); err != nil {
			return nil
		}
	} else {
		return nil // коннектора нет: настройки ни на что не влияют до его установки
	}
	out := map[string][]string{}
	add := func(what string, vals ...string) {
		for _, v := range vals {
			for _, sm := range settingTmplRe.FindAllStringSubmatch(v, -1) {
				top, _, _ := strings.Cut(sm[1], ".")
				if !slices.Contains(out[top], what) {
					out[top] = append(out[top], what)
				}
			}
		}
	}
	for _, a := range m.Capabilities.Reveal {
		add("reveals secret "+a, a)
	}
	for _, c := range m.Credentials {
		add("signs in on "+strings.Join(c.Bind, ", "), c.Bind...)
	}
	return out
}

// settingWarnings — предупреждения к правке настроек коннектора, от которых
// зависит, куда он ходит в сеть и куда хост отправляет секреты.
func (d *Daemon) settingWarnings(ch config.Change) []string {
	touched := map[string]any{}
	switch ch.Op {
	case config.OpConnectorSetting:
		var m map[string]any
		if json.Unmarshal([]byte(ch.SettingsJSON), &m) != nil {
			return nil
		}
		touched = m
	case config.OpAddSettingItems, config.OpRemoveSettingItems:
		top, _, _ := strings.Cut(ch.Key, ".")
		touched[top] = ch.Items
	default:
		return nil
	}
	sens := d.bindSettings(ch.Connector)
	var out []string
	for _, k := range slices.Sorted(maps.Keys(touched)) {
		caps, ok := sens[k]
		if !ok {
			continue
		}
		v, _ := json.Marshal(touched[k])
		out = append(out, fmt.Sprintf("ATTENTION: setting %s of connector %s decides where it connects and where your secrets are sent (%s); new value: %s",
			k, ch.Connector, strings.Join(caps, "; "), v))
	}
	return out
}
