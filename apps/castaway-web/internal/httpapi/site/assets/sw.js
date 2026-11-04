// Castaway service worker (served at /sw.js so it controls the whole site). It makes the site installable and
// keeps game assets for flaky signal. Pages are always fetched from the network: they need a login and are
// never cached. Assets are network-first, so a deploy shows up on the next load; bump CACHE to drop old ones.
const CACHE = "castaway-assets-v1";
self.addEventListener("install", () => self.skipWaiting());
self.addEventListener("activate", (e) => e.waitUntil(
  caches.keys().then((keys) => Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k)))).then(() => self.clients.claim())));
self.addEventListener("fetch", (e) => {
  const req = e.request, url = new URL(req.url);
  if (req.method !== "GET" || url.origin !== location.origin) return;
  if (url.pathname.startsWith("/assets/")) {
    e.respondWith(fetch(req).then((res) => {
      if (res.ok) { const copy = res.clone(); e.waitUntil(caches.open(CACHE).then((c) => c.put(req, copy))); }
      return res;
    }).catch(() => caches.match(req).then((hit) => hit || Response.error())));
  } else if (req.mode === "navigate") {
    e.respondWith(fetch(req).catch(() => new Response(
      "<!doctype html><meta name=viewport content='width=device-width'><title>Offline · Castaway</title>" +
      "<body style='font:16px sans-serif;background:#1f3a24;color:#ece6d6;display:grid;place-items:center;height:100vh;margin:0'>" +
      "<p>You're offline. Reconnect and try again.</p>",
      { headers: { "Content-Type": "text/html; charset=utf-8" } })));
  }
});
