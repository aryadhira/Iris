#!/usr/bin/env node
/**
 * P9 browser check — 390x844.
 *
 * Records a synthetic microphone stream (real speech audio played into a
 * MediaStreamDestination), stops it, and proves the recognised text is sent
 * with no tap on Kirim. The transcript is captured from the real
 * /api/voice/transcribe response, so the toast assertion is about what the
 * sidecar actually heard, not a hard-coded string.
 */
import { spawn } from 'node:child_process'
import { mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { homedir, tmpdir } from 'node:os'
import { fileURLToPath } from 'node:url'

const ROOT = join(dirname(fileURLToPath(import.meta.url)), '..')
const APP_URL = process.env.IRIS_WEB_BASE ?? 'http://127.0.0.1:3309'
const VOICE_URL = process.env.IRIS_VOICE_URL ?? 'http://127.0.0.1:3311'
const SHOTS_DIR = process.env.IRIS_SHOTS_DIR ?? join(ROOT, 'changelogs', 'assets')
const RESULTS_FILE = process.env.IRIS_RESULTS_FILE ?? join(SHOTS_DIR, 'p9-results.json')
const PASSPHRASE_FILE = process.env.IRIS_PASSPHRASE_FILE
const CDP_PORT = Number(process.env.IRIS_CDP_PORT ?? 9241)
const CHROME_BIN =
  process.env.CHROME_BIN ??
  join(homedir(), '.cache/ms-playwright/chromium-1248/chrome-linux64/chrome')
const PROFILE = join(tmpdir(), 'iris-p9-cdp-profile')

if (!PASSPHRASE_FILE) {
  console.error('IRIS_PASSPHRASE_FILE is required')
  process.exit(2)
}
const passphrase = readFileSync(PASSPHRASE_FILE, 'utf8').trim()
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
const results = []

function record(scope, name, pass, detail = '') {
  results.push({ scope, name, pass, detail, at: new Date().toISOString() })
  console.log(`[${pass ? 'PASS' : 'FAIL'}] ${scope}: ${name}${detail ? ` — ${detail}` : ''}`)
  if (!pass) process.exitCode = 1
}

function assert(scope, name, condition, detail = '') {
  record(scope, name, Boolean(condition), detail)
  if (!condition) throw new Error(`${scope}: ${name}${detail ? ` (${detail})` : ''}`)
}

async function connectCDP(wsUrl) {
  const ws = new WebSocket(wsUrl)
  await new Promise((resolve, reject) => {
    ws.onopen = resolve
    ws.onerror = () => reject(new Error('WebSocket connect failed'))
  })
  let nextId = 0
  const pending = new Map()
  ws.onmessage = (event) => {
    const message = JSON.parse(String(event.data))
    if (message.id != null && pending.has(message.id)) {
      const { resolve, reject } = pending.get(message.id)
      pending.delete(message.id)
      if (message.error) reject(new Error(message.error.message))
      else resolve(message.result)
    }
  }
  return {
    send(method, params = {}, sessionId) {
      const id = ++nextId
      return new Promise((resolve, reject) => {
        pending.set(id, { resolve, reject })
        ws.send(JSON.stringify({ id, method, params, ...(sessionId ? { sessionId } : {}) }))
      })
    },
    close: () => ws.close(),
  }
}

function launchChrome() {
  rmSync(PROFILE, { recursive: true, force: true })
  const child = spawn(
    CHROME_BIN,
    [
      '--headless=new',
      `--remote-debugging-port=${CDP_PORT}`,
      `--user-data-dir=${PROFILE}`,
      '--window-size=390,844',
      '--no-first-run',
      '--no-default-browser-check',
      '--disable-dev-shm-usage',
      '--no-sandbox',
      '--disable-setuid-sandbox',
      '--hide-scrollbars',
      '--force-device-scale-factor=1',
      '--autoplay-policy=no-user-gesture-required',
      'about:blank',
    ],
    { stdio: ['ignore', 'ignore', 'pipe'] },
  )
  child.stderr.on('data', () => {})
  return child
}

async function waitForCDP(timeoutMs = 20000) {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    try {
      const response = await fetch(`http://127.0.0.1:${CDP_PORT}/json/version`)
      if (response.ok) return response.json()
    } catch {
      /* not up yet */
    }
    await sleep(200)
  }
  throw new Error('Chrome DevTools endpoint never came up')
}

let cdp
let sessionId

async function evaluate(expression) {
  const result = await cdp.send(
    'Runtime.evaluate',
    { expression, awaitPromise: true, returnByValue: true, userGesture: true },
    sessionId,
  )
  if (result.exceptionDetails) {
    throw new Error(
      `page eval failed: ${result.exceptionDetails.exception?.description ?? result.exceptionDetails.text}`,
    )
  }
  return result.result.value
}

async function waitForTrue(expression, timeoutMs, intervalMs = 120) {
  const deadline = Date.now() + timeoutMs
  let last = null
  while (Date.now() < deadline) {
    last = await evaluate(expression)
    if (last) return last
    await sleep(intervalMs)
  }
  throw new Error(`timeout waiting for: ${expression} (last=${JSON.stringify(last)})`)
}

async function navigate(url) {
  await cdp.send('Page.navigate', { url }, sessionId)
  await waitForTrue(
    `['complete', 'interactive'].includes(document.readyState) && location.href !== 'about:blank'`,
    20000,
    100,
  )
  await installHelpers()
}

async function installHelpers() {
  await evaluate(`(() => {
    window.__iris = {
      set(selector, value) {
        const el = document.querySelector(selector)
        if (!el) return false
        const proto = el instanceof HTMLTextAreaElement
          ? HTMLTextAreaElement.prototype
          : HTMLInputElement.prototype
        Object.getOwnPropertyDescriptor(proto, 'value').set.call(el, value)
        el.dispatchEvent(new Event('input', { bubbles: true }))
        return true
      },
      click(selector, index = 0) {
        const el = document.querySelectorAll(selector)[index]
        if (!el) return false
        el.click()
        return true
      },
    }
    return true
  })()`)
}

async function installVoiceHarness(speechBase64) {
  await evaluate(`(() => {
    window.__p9 = { transcript: '', transcribeFinished: false }
    if (!window.__p9FetchWrapped) {
      window.__p9FetchWrapped = true
      const originalFetch = window.fetch.bind(window)
      window.fetch = async function (...args) {
        const response = await originalFetch(...args)
        const url = typeof args[0] === 'string' ? args[0] : args[0]?.url ?? ''
        if (url.includes('/api/voice/transcribe')) {
          response.clone().json().then((body) => {
            window.__p9.transcript = String(body?.text ?? '')
            window.__p9.transcribeFinished = true
          }).catch(() => { window.__p9.transcribeFinished = true })
        }
        return response
      }
    }
    const bytes = Uint8Array.from(atob(${JSON.stringify(speechBase64)}), (c) => c.charCodeAt(0))
    navigator.mediaDevices.getUserMedia = async () => {
      const context = new AudioContext()
      if (context.state === 'suspended') await context.resume()
      const destination = context.createMediaStreamDestination()
      const buffer = await context.decodeAudioData(bytes.buffer.slice(0))
      const source = context.createBufferSource()
      source.buffer = buffer
      source.connect(destination)
      // Let MediaRecorder start before the first word.
      setTimeout(() => { try { source.start() } catch {} }, 280)
      window.__p9audio = { context, source, destination }
      return destination.stream
    }
    return true
  })()`)
}

async function click(selector, index = 0) {
  const ok = await evaluate(`window.__iris.click(${JSON.stringify(selector)}, ${index})`)
  if (!ok) throw new Error(`cannot click ${selector}[${index}]`)
}

async function setInput(selector, value) {
  const ok = await evaluate(`window.__iris.set(${JSON.stringify(selector)}, ${JSON.stringify(value)})`)
  if (!ok) throw new Error(`cannot fill ${selector}`)
}

async function screenshot(name) {
  const result = await cdp.send('Page.captureScreenshot', { format: 'png' }, sessionId)
  const file = join(SHOTS_DIR, `p9-${name}.png`)
  writeFileSync(file, Buffer.from(result.data, 'base64'))
  console.log(`[SHOT] ${file}`)
  return file
}

async function viewport(label, width, height) {
  const mobile = width < 500
  await cdp.send(
    'Emulation.setDeviceMetricsOverride',
    { width, height, deviceScaleFactor: mobile ? 2 : 1, mobile },
    sessionId,
  )
  await sleep(250)
  const metrics = await evaluate(`({
    innerWidth: window.innerWidth,
    scrollWidth: document.documentElement.scrollWidth,
  })`)
  if (metrics.scrollWidth > metrics.innerWidth + 1) {
    throw new Error(`horizontal overflow at ${label}: ${JSON.stringify(metrics)}`)
  }
}

async function login() {
  await navigate(`${APP_URL}/`)
  await evaluate(`localStorage.clear(); sessionStorage.clear(); true`)
  await waitForTrue(`location.pathname === '/login'`, 20000)
  await setInput('#passphrase', passphrase)
  await click('[data-testid="login-submit"]')
  await waitForTrue(`location.pathname === '/rooms'`, 25000, 200)
  await waitForTrue(`!!document.querySelector('[data-testid="room-list"]')`, 15000)
}

async function createRoom(name) {
  await click('[data-testid="rooms-menu"]')
  await sleep(300)
  await click('[data-testid="new-room-sidebar"]')
  await waitForTrue(`!!document.querySelector('#room-name')`, 10000)
  await setInput('#room-name', name)
  await setInput('#room-topic', 'P9 auto-send suara')
  await waitForTrue(`!document.querySelector('[data-testid="create-room"]').disabled`, 5000)
  await click('[data-testid="create-room"]')
  await waitForTrue(`location.pathname.startsWith('/r/')`, 40000, 200)
  await waitForTrue(`!!document.querySelector('[data-testid="empty-room"]')`, 15000)
}

async function synthesiseProbe() {
  const response = await fetch(`${VOICE_URL}/speak`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ text: 'Ingatkan saya besok pagi.' }),
  })
  if (!response.ok) throw new Error(`sidecar /speak = ${response.status}`)
  const audio = Buffer.from(await response.arrayBuffer())
  if (audio.length < 1000) throw new Error('sidecar produced no probe audio')
  return audio.toString('base64')
}

async function main() {
  mkdirSync(SHOTS_DIR, { recursive: true })
  const speechBase64 = await synthesiseProbe()
  const chrome = launchChrome()
  try {
    await waitForCDP()
    const version = await (await fetch(`http://127.0.0.1:${CDP_PORT}/json/version`)).json()
    cdp = await connectCDP(version.webSocketDebuggerUrl)
    const { targetId } = await cdp.send('Target.createTarget', { url: 'about:blank' })
    const attached = await cdp.send('Target.attachToTarget', { targetId, flatten: true })
    sessionId = attached.sessionId
    await cdp.send('Page.enable', {}, sessionId)
    await cdp.send('Runtime.enable', {}, sessionId)

    await viewport('390x844', 390, 844)
    await login()
    await createRoom('P9 ponsel')
    await installVoiceHarness(speechBase64)
    console.log('[PASS] room created at 390x844')

    await click('[data-testid="mic-button"]')
    await waitForTrue(`document.querySelector('[data-testid="mic-button"]').dataset.state === 'recording'`, 10000)
    await sleep(2200)
    await click('[data-testid="mic-button"]')
    await waitForTrue(`document.querySelector('[data-testid="mic-button"]').dataset.state === 'transcribing'`, 10000)
    await waitForTrue(`window.__p9.transcribeFinished === true`, 30000, 100)
    const transcript = await evaluate(`window.__p9.transcript`)
    assert('390', 'the sidecar recognised real speech', transcript.trim().length > 0, transcript)

    await waitForTrue(`!!document.querySelector('[data-testid="sent-toast"]')`, 30000, 100)
    const shot = await screenshot('390-autosend')
    const ui = await evaluate(`(() => {
      const toast = document.querySelector('[data-testid="sent-toast"]')
      const user = [...document.querySelectorAll('[data-testid="user-message"]')].at(-1)
      const input = document.querySelector('[data-testid="composer-input"]')
      const send = document.querySelector('[data-testid="send-button"]')
      return {
        toast: toast?.innerText ?? '',
        user: user?.innerText ?? '',
        composer: input?.value ?? '',
        sendDisabled: Boolean(send?.disabled),
        status: document.body.innerText.includes('STATUS:'),
      }
    })()`)
    assert('390', 'the toast says terkirim and shows what was sent',
      /terkirim/i.test(ui.toast) && ui.toast.includes(transcript.trim()), JSON.stringify(ui))
    assert('390', 'the user message was sent without a Kirim tap', ui.user.includes(transcript.trim()), ui.user)
    assert('390', 'the composer is empty afterwards', ui.composer === '', JSON.stringify(ui.composer))
    assert('390', 'no STATUS line is visible', ui.status === false)
    assert('390', 'the reply was already streaming (send was accepted)', ui.sendDisabled === true, JSON.stringify(ui))

    await click('[data-testid="message-menu"]')
    await waitForTrue(`!!document.querySelector('[data-testid="retry-message"]')`, 5000)
    const retry = await evaluate(`document.querySelector('[data-testid="retry-message"]')?.innerText ?? ''`)
    assert('390', 'the auto-sent message has Coba lagi immediately', /Coba lagi/.test(retry), retry)
    await click('[data-testid="message-menu"]')

    record('390', 'screenshot', true, shot)
    console.log('PASS: P9 browser check')
  } finally {
    try {
      cdp?.close()
    } catch {
      /* ignore */
    }
    chrome.kill('SIGTERM')
    writeFileSync(RESULTS_FILE, `${JSON.stringify(results, null, 2)}\n`)
  }
}

main().catch((error) => {
  console.error(`FAIL: ${error.stack ?? error}`)
  try {
    writeFileSync(RESULTS_FILE, `${JSON.stringify(results, null, 2)}\n`)
  } catch {
    /* ignore */
  }
  process.exit(1)
})
