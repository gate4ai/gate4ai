// gate4.ai: Service Worker браузерного push — показывает краткое уведомление;
// решение принимается на странице Web UI.
self.addEventListener("push", function (e) {
  var m = {};
  try { m = e.data ? e.data.json() : {}; } catch (_) { m = { title: "gate4.ai", body: e.data ? e.data.text() : "" }; }
  e.waitUntil(self.registration.showNotification(m.title || "gate4.ai", {
    body: m.body || "", tag: m.tag || undefined, requireInteraction: true, data: { url: m.url || "./" }
  }));
});
self.addEventListener("notificationclick", function (e) {
  e.notification.close();
  var url = new URL(e.notification.data && e.notification.data.url || "./", self.registration.scope).href;
  e.waitUntil(self.clients.matchAll({ type: "window" }).then(function (ws) {
    for (var i = 0; i < ws.length; i++) { if (ws[i].url === url && "focus" in ws[i]) return ws[i].focus(); }
    return self.clients.openWindow(url);
  }));
});
