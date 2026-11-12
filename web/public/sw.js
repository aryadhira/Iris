/* Iris service worker: Web Push only. Hand-written, no build plugin. */

self.addEventListener('install', () => {
  self.skipWaiting()
})

self.addEventListener('activate', (event) => {
  event.waitUntil(self.clients.claim())
})

function plainBody(value) {
  let text = String(value || '')
  text = text.replace(/```[\s\S]*?```/g, ' ')
  text = text.replace(/`([^`]*)`/g, '$1')
  text = text.replace(/!?\[([^\]]*)\]\([^)]*\)/g, '$1')
  text = text.replace(/^\s*#{1,6}\s*/gm, '')
  text = text.replace(/^\s*[-*+]\s+/gm, '')
  text = text.replace(/(\*\*|__|~~|\*)/g, '')
  text = text.replace(/\s+/g, ' ').trim()
  if (text === '') return 'Ada kabar baru dari Iris.'
  return text.length > 120 ? `${text.slice(0, 119).trim()}…` : text
}

self.addEventListener('push', (event) => {
  let data = {}
  try {
    data = event.data ? event.data.json() : {}
  } catch {
    data = { body: event.data ? event.data.text() : '' }
  }
  const roomId = String(data.roomId || '')
  const messageId = String(data.messageId || '')
  const title = String(data.title || 'Iris')
  event.waitUntil(
    self.registration.showNotification(title, {
      body: plainBody(data.body),
      tag: String(data.tag || roomId || 'iris'),
      renotify: false,
      icon: '/icons/icon-192.png',
      badge: '/icons/mark-32.png',
      data: { roomId, messageId },
    }),
  )
})

self.addEventListener('notificationclick', (event) => {
  event.notification.close()
  const data = event.notification.data || {}
  const roomId = String(data.roomId || '')
  const messageId = String(data.messageId || '')
  const path = roomId
    ? `/r/${encodeURIComponent(roomId)}${messageId ? `#m-${encodeURIComponent(messageId)}` : ''}`
    : '/rooms'

  event.waitUntil(
    (async () => {
      const windows = await self.clients.matchAll({ type: 'window', includeUncontrolled: true })
      for (const client of windows) {
        const url = new URL(client.url)
        if (roomId && url.pathname === `/r/${roomId}`) {
          try {
            await client.navigate(path)
          } catch {
            // A browser that refuses navigate still gets focus below.
          }
          return client.focus()
        }
      }
      if (self.clients.openWindow) return self.clients.openWindow(path)
      return undefined
    })(),
  )
})
