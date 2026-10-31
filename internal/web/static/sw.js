// SMMYP Service Worker: statische Dateien aus dem Cache, Seiten network-first mit Offline-Lesecache.
const STATIC = 'smmyp-static-v2';
const PAGES = 'smmyp-pages-v2';
const ASSETS = ['/static/app.css', '/static/app.js', '/static/fonts/plex-sans-400.woff2', '/static/fonts/plex-sans-600.woff2', '/static/fonts/plex-mono-400.woff2', '/favicon.svg'];

self.addEventListener('install', (e) => {
  e.waitUntil(caches.open(STATIC).then((c) => c.addAll(ASSETS)).then(() => self.skipWaiting()));
});

self.addEventListener('activate', (e) => {
  e.waitUntil(caches.keys().then((keys) => Promise.all(keys.filter((k) => k !== STATIC && k !== PAGES).map((k) => caches.delete(k)))).then(() => self.clients.claim()));
});

self.addEventListener('message', (e) => {
  if (e.data === 'logout') caches.delete(PAGES);
});

const cacheable = (url) => !url.pathname.startsWith('/api/') && !url.pathname.startsWith('/admin') && url.pathname !== '/export.json' && !url.searchParams.has('partial');

self.addEventListener('fetch', (e) => {
  const req = e.request;
  const url = new URL(req.url);
  if (req.method !== 'GET' || url.origin !== location.origin) return;
  if (url.pathname.startsWith('/static/') || url.pathname === '/favicon.svg') {
    e.respondWith(caches.match(req, { ignoreSearch: true }).then((hit) => hit || fetch(req)));
    return;
  }
  if (req.mode === 'navigate' && cacheable(url)) {
    e.respondWith(
      fetch(req)
        .then((res) => {
          if (res.ok && res.type === 'basic' && !res.redirected) {
            const copy = res.clone();
            caches.open(PAGES).then((c) => c.put(req, copy));
          }
          return res;
        })
        .catch(() => caches.match(req).then((hit) => hit || new Response('<!doctype html><meta charset="utf-8"><title>Offline</title><p style="font-family:sans-serif;padding:2rem">Offline – diese Seite wurde noch nicht zwischengespeichert.</p>', { headers: { 'Content-Type': 'text/html; charset=utf-8' } }))),
    );
  }
});
