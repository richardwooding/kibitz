// kibitz service worker — makes the app installable and fast, without ever
// serving a stale shell. Strategy:
//   • the whole app shell (navigations, the WASM core, JS/CSS/icons) is served
//     NETWORK-FIRST: fresh when online, so a deploy is never served stale and a
//     user can't run a fresh index.html against an old cached app.js. The cache
//     is only an offline fallback.
//   • /ws (the relay WebSocket) is never touched — it isn't a fetch anyway.
// The relay stays a blind static server; this file adds no network behaviour of
// its own, only caching of what the page already requests.
//
// Bump CACHE when the shell asset set changes to evict old caches on activate.
const CACHE = "kibitz-shell-v11";

// The small shell precached on install so an installed app opens offline. The
// ~9MB WASM core is intentionally NOT precached — it caches on first fetch.
const SHELL = [
  "/",
  "style.css",
  "app.js",
  "board.js",
  "bgboard.js",
  "c4board.js",
  "gomokuboard.js",
  "gomokupboard.js",
  "hexboard.js",
  "dotsboard.js",
  "weiqiboard.js",
  "xiangqiboard.js",
  "ginboard.js",
  "checkersboard.js",
  "reversiboard.js",
  "bsboard.js",
  "fx.js",
  "wasm_exec.js",
  "manifest.json",
  "icon-192.png",
  "icon-512.png",
  "icon-512-maskable.png",
  "apple-touch-icon.png",
  "author.png",
];

self.addEventListener("install", (e) => {
  self.skipWaiting();
  e.waitUntil(
    caches.open(CACHE).then((c) =>
      // Best-effort: one missing asset must not fail the whole install.
      Promise.allSettled(SHELL.map((u) => c.add(u)))
    )
  );
});

self.addEventListener("activate", (e) => {
  e.waitUntil(
    caches.keys().then((keys) =>
      Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k)))
    ).then(() => self.clients.claim())
  );
});

self.addEventListener("fetch", (e) => {
  const req = e.request;
  if (req.method !== "GET") return;
  const url = new URL(req.url);
  if (url.origin !== self.location.origin) return; // let cross-origin pass through
  if (url.pathname === "/ws") return; // never intercept the relay socket
  e.respondWith(networkFirst(req)); // whole shell is network-first
});

// networkFirst: fresh when online (a deploy is picked up immediately), cache as
// the offline fallback — for navigations, fall back to the cached shell root.
async function networkFirst(req) {
  const cache = await caches.open(CACHE);
  try {
    const res = await fetch(req);
    if (res && res.ok) cache.put(req, res.clone());
    return res;
  } catch (err) {
    const hit = (await cache.match(req)) || (req.mode === "navigate" && (await cache.match("/")));
    if (hit) return hit;
    throw err;
  }
}

// ---- turn notifications ----------------------------------------------------
// Pushes are payload-free ("your turn" wakes only — no game content ever leaves
// the device), so every push shows the same generic notification. If a kibitz
// window is already focused, skip it: the player is looking at the board.
self.addEventListener("push", (e) => {
  e.waitUntil(
    (async () => {
      const clis = await self.clients.matchAll({ type: "window", includeUncontrolled: true });
      if (clis.some((c) => c.focused || c.visibilityState === "visible")) return;
      await self.registration.showNotification("kibitz", {
        body: "Your turn.",
        icon: "icon-192.png",
        badge: "icon-192.png",
        tag: "kibitz-turn", // coalesce: one "your turn" at a time
        renotify: true,
      });
    })()
  );
});

// Tapping the notification focuses an existing kibitz window or opens one.
self.addEventListener("notificationclick", (e) => {
  e.notification.close();
  e.waitUntil(
    (async () => {
      const clis = await self.clients.matchAll({ type: "window", includeUncontrolled: true });
      for (const c of clis) {
        if ("focus" in c) return c.focus();
      }
      if (self.clients.openWindow) return self.clients.openWindow("/");
    })()
  );
});
