// MemoDump Service Worker — minimal offline shell caching
const CACHE_NAME = 'memodump-v1'

self.addEventListener('install', (event) => {
    self.skipWaiting()
})

self.addEventListener('activate', (event) => {
    event.waitUntil(
        caches.keys().then((names) =>
            Promise.all(
                names.filter((n) => n !== CACHE_NAME).map((n) => caches.delete(n))
            )
        )
    )
    self.clients.claim()
})

self.addEventListener('fetch', (event) => {
    // Only cache GET requests for app shell assets (not API calls)
    if (event.request.method !== 'GET') return
    const url = new URL(event.request.url)
    if (url.pathname.startsWith('/api')) return

    event.respondWith(
        fetch(event.request)
            .then((response) => {
                // Cache successful responses for offline support
                if (response.status === 200) {
                    const clone = response.clone()
                    caches.open(CACHE_NAME).then((cache) => cache.put(event.request, clone))
                }
                return response
            })
            .catch(() => caches.match(event.request))
    )
})
