// gate4.ai Web UI: небольшие улучшения поверх серверных форм. Без этого файла
// всё работает: формы отправляются, страницы отрисовываются на сервере.
(function () {
  "use strict";
  var csrf = (document.querySelector('meta[name="csrf"]') || {}).content || "";
  var base = (document.querySelector('meta[name="base"]') || {}).content || "";

  // Копирование: <button data-copy="#id"> или data-copy-text="…".
  document.addEventListener("click", function (e) {
    var b = e.target.closest("[data-copy],[data-copy-text]");
    if (!b) return;
    e.preventDefault();
    var text = b.getAttribute("data-copy-text");
    if (text === null) {
      var el = document.querySelector(b.getAttribute("data-copy"));
      text = el ? (el.value !== undefined && el.tagName !== "PRE" && el.tagName !== "CODE" ? el.value : el.textContent) : "";
    }
    var done = function () { var t = b.textContent; b.textContent = "Скопировано"; setTimeout(function () { b.textContent = t; }, 1500); };
    if (navigator.clipboard) navigator.clipboard.writeText(text).then(done, function () {});
  });

  // Подтверждение опасных действий: <form data-confirm="Текст?">.
  document.addEventListener("submit", function (e) {
    var f = e.target, q = f.getAttribute("data-confirm");
    if (q && !window.confirm(q)) e.preventDefault();
  });

  // Автоотправка GET-форм выбора: <select data-autosubmit>.
  document.addEventListener("change", function (e) {
    if (e.target.matches("[data-autosubmit]")) e.target.form.submit();
  });

  // Блок формы только для одного значения поля: <fieldset data-show-when="profile=_new">.
  // Скрытый блок ещё и выключен, чтобы его поля не уходили с формой.
  function showWhen() {
    document.querySelectorAll("[data-show-when]").forEach(function (b) {
      var kv = b.getAttribute("data-show-when").split("="), f = b.closest("form");
      var el = f && f.elements[kv[0]];
      if (!el) return;
      var on = el.value === kv.slice(1).join("=");
      b.hidden = !on;
      b.disabled = !on;
    });
  }
  showWhen();
  window.addEventListener("pageshow", showWhen);
  document.addEventListener("change", function (e) {
    if (e.target.form && e.target.form.querySelector("[data-show-when]")) showWhen();
  });

  // Ещё одно условие правила.
  document.addEventListener("click", function (e) {
    var b = e.target.closest("[data-add-cond]");
    if (!b) return;
    e.preventDefault();
    var rows = document.querySelectorAll(".cond");
    var last = rows[rows.length - 1];
    if (!last) return;
    var n = last.cloneNode(true), i = rows.length;
    n.querySelectorAll("input,select").forEach(function (el) {
      if (el.type === "checkbox") { el.checked = false; el.name = "cond_not_" + i; }
      else if (el.name && el.name.indexOf("list_items_") === 0) { el.name = "list_items_" + i; el.value = ""; }
      else if (el.tagName === "INPUT") el.value = "";
      else el.selectedIndex = 0;
    });
    last.parentNode.insertBefore(n, last.nextSibling);
  });

  // Решение по запросу отправляется один раз: повторное нажатие (или второй
  // клик, пока действие исполняется) давало «запрос уже решён».
  document.addEventListener("submit", function (e) {
    var f = e.target;
    if (!f.matches("form[data-decide]") || e.defaultPrevented) return;
    if (f.getAttribute("data-sent")) { e.preventDefault(); return; }
    f.setAttribute("data-sent", "1");
    var s = e.submitter;
    if (s && s.name) {
      // Выключенная кнопка не уходит с формой — её значение передаём скрытым полем.
      var h = document.createElement("input");
      h.type = "hidden"; h.name = s.name; h.value = s.value;
      f.appendChild(h);
      s.textContent = "Отправляю…";
    }
    f.querySelectorAll("button").forEach(function (b) { b.disabled = true; });
  });
  window.addEventListener("pageshow", function (e) {
    if (!e.persisted) return; // страница из кэша «назад»: кнопки снова доступны
    if (document.querySelector("form[data-decide][data-sent]")) location.reload();
  });

  // Очередь подтверждений обновляется сама, пока страница открыта: новые
  // запросы появляются, решённые в другом канале или истёкшие — исчезают
  // с пояснением, кто и как решил.
  var live = document.querySelector("[data-live-pending]");
  if (live) {
    var back = live.getAttribute("data-back") || "/approvals";
    var cardOf = function (id) {
      var found = null;
      live.querySelectorAll("[data-approval]").forEach(function (c) { if (c.getAttribute("data-approval") === id) found = c; });
      return found;
    };
    var setCount = function (n) {
      var c = document.querySelector("[data-pending-count]");
      if (!c) return;
      c.textContent = n;
      c.hidden = n === 0;
    };
    var apply = function (j) {
      j.resolved.forEach(function (g) {
        var c = cardOf(g.id);
        if (!c || c.querySelector("form[data-sent]")) return; // решаем здесь — ждём ответа формы
        c.removeAttribute("data-approval");
        c.classList.add("gone");
        c.textContent = "";
        var p = document.createElement("p");
        p.className = "muted";
        p.style.margin = "0";
        p.textContent = g.text;
        c.appendChild(p);
        setTimeout(function () { c.remove(); }, 10000);
      });
      var prev = null;
      j.pending.forEach(function (a) {
        var c = cardOf(a.id);
        if (!c) {
          var t = document.createElement("template");
          t.innerHTML = a.html.trim();
          c = t.content.firstElementChild;
          if (prev) prev.after(c); else live.prepend(c);
        }
        prev = c;
      });
      var empty = live.querySelector("[data-live-empty]");
      if (empty) empty.hidden = j.pending.length > 0;
      setCount(j.pending.length);
    };
    var poll = function () {
      if (document.hidden) return;
      var known = [];
      live.querySelectorAll("[data-approval]").forEach(function (c) { known.push(c.getAttribute("data-approval")); });
      fetch(base + "/approvals/live?back=" + encodeURIComponent(back) + "&known=" + encodeURIComponent(known.join(",")),
        { credentials: "same-origin", headers: { Accept: "application/json" } })
        .then(function (r) { return r.ok ? r.json() : null; })
        .then(function (j) { if (j) apply(j); })
        .catch(function () {});
    };
    setInterval(poll, 3000);
    document.addEventListener("visibilitychange", function () { if (!document.hidden) poll(); });
  }

  // Relay подключается (сертификат, туннель): страница обновляется при смене этапа.
  var relay = document.querySelector("[data-live-relay]");
  if (relay) {
    var phase = relay.getAttribute("data-live-relay");
    setInterval(function () {
      if (document.hidden) return;
      fetch(location.href, { credentials: "same-origin" }).then(function (r) { return r.text(); }).then(function (t) {
        var m = t.match(/data-live-relay="(\w+)"/);
        if ((m ? m[1] : "") !== phase && !document.querySelector("form:focus-within")) location.reload();
      }).catch(function () {});
    }, 3000);
  }

  // Браузерный push: <button data-push-key="…">.
  function b64u(s) {
    var p = "=".repeat((4 - (s.length % 4)) % 4), raw = atob((s + p).replace(/-/g, "+").replace(/_/g, "/"));
    var out = new Uint8Array(raw.length);
    for (var i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i);
    return out;
  }
  var pb = document.querySelector("[data-push-key]");
  if (pb) {
    var status = document.querySelector("[data-push-status]");
    var say = function (t) { if (status) status.textContent = t; };
    if (!("serviceWorker" in navigator) || !("PushManager" in window)) {
      pb.disabled = true; say("Этот браузер не поддерживает push-уведомления.");
    } else if (!window.isSecureContext) {
      pb.disabled = true; say("Push работает только по HTTPS или на localhost.");
    } else {
      navigator.serviceWorker.getRegistration(base + "/").then(function (reg) {
        if (!reg) return;
        return reg.pushManager.getSubscription().then(function (s) { if (s) say("Этот браузер уже получает уведомления."); });
      });
      pb.addEventListener("click", function (e) {
        e.preventDefault();
        say("Запрашиваю разрешение…");
        navigator.serviceWorker.register(base + "/sw.js", { scope: base + "/" }).then(function (reg) {
          return navigator.serviceWorker.ready.then(function () { return reg; });
        }).then(function (reg) {
          return reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: b64u(pb.getAttribute("data-push-key")) });
        }).then(function (sub) {
          return fetch(base + "/push/subscribe", { method: "POST", credentials: "same-origin",
            headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf }, body: JSON.stringify(sub.toJSON()) });
        }).then(function (r) { return r.json(); }).then(function (j) {
          if (j.error) throw new Error(j.error);
          say(j.channel_on ? "Готово: уведомления будут приходить в этот браузер." : "Браузер подписан. Включите канал «Браузерный push» на компьютере.");
          setTimeout(function () { location.reload(); }, 1200);
        }).catch(function (err) { say("Не получилось: " + err.message); });
      });
    }
  }
})();
