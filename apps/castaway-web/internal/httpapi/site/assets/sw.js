// Castaway service worker (served at /sw.js so it controls the whole site). It makes the site installable and
// keeps game assets for flaky signal. Pages and logins are never touched: the browser handles them itself
// (an intercepted OAuth redirect can drop the login cookie). Assets are network-first, so a deploy shows up on the next load; bump CACHE to drop old ones.
const CACHE = "castaway-assets-v2";
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
  }
});
