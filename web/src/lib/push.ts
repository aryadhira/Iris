import { api } from './api'

/** Web Push needs a secure context, a service worker and the Push API. */
export function pushSupported(): boolean {
  return (
    typeof window !== 'undefined' &&
    'serviceWorker' in navigator &&
    'PushManager' in window &&
    'Notification' in window
  )
}

function serviceWorkerReady(timeoutMs = 4000): Promise<ServiceWorkerRegistration> {
  return Promise.race([
    navigator.serviceWorker.ready,
    new Promise<ServiceWorkerRegistration>((_resolve, reject) => {
      window.setTimeout(() => reject(new Error('service worker is not ready')), timeoutMs)
    }),
  ])
}

function urlBase64ToUint8Array(value: string): Uint8Array {
  const padding = '='.repeat((4 - (value.length % 4)) % 4)
  const base64 = (value + padding).replace(/-/g, '+').replace(/_/g, '/')
  const raw = window.atob(base64)
  const bytes = new Uint8Array(raw.length)
  for (let i = 0; i < raw.length; i += 1) bytes[i] = raw.charCodeAt(i)
  return bytes
}

export async function pushIsOn(): Promise<boolean> {
  if (!pushSupported()) return false
  try {
    const registration = await serviceWorkerReady()
    return Boolean(await registration.pushManager.getSubscription())
  } catch {
    return false
  }
}

export type PushEnableResult = 'subscribed' | 'unsupported' | 'denied' | 'error'

/** Ask once, subscribe this device, and register it with irisd for this room. */
export async function enablePush(roomId: string): Promise<PushEnableResult> {
  if (!pushSupported()) return 'unsupported'
  try {
    const permission = await Notification.requestPermission()
    if (permission !== 'granted') return 'denied'
    const publicKey = await api.getPushKey()
    if (publicKey === '') return 'error'
    const registration = await serviceWorkerReady()
    let subscription = await registration.pushManager.getSubscription()
    if (!subscription) {
      subscription = await registration.pushManager.subscribe({
        userVisibleOnly: true,
        applicationServerKey: urlBase64ToUint8Array(publicKey) as BufferSource,
      })
    }
    const json = subscription.toJSON()
    await api.subscribePush(roomId, { endpoint: subscription.endpoint, keys: json.keys })
    return 'subscribed'
  } catch {
    return 'error'
  }
}

export async function disablePush(): Promise<void> {
  if (!pushSupported()) return
  try {
    const registration = await serviceWorkerReady()
    const subscription = await registration.pushManager.getSubscription()
    if (!subscription) return
    await api.unsubscribePush(subscription.endpoint)
    await subscription.unsubscribe()
  } catch {
    // The server row may already be gone; a local failure must not trap the UI.
  }
}
