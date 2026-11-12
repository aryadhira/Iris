#!/usr/bin/env node
/**
 * P8 browser check — real Chromium over the DevTools protocol at 390x844.
 *
 * Proves three things the phone actually shows:
 *   1. the room inspector renders the usage chip, a 14-day chart and top rooms;
 *   2. "Coba lagi" is a 44px target, is disabled while a turn runs, and a
 *      retried reply leaves the old one dimmed and labelled, not deleted;
 *   3. a streaming reply starts speaking (the "membaca…" state) before the
 *      speech response has finished arriving.
 */

import { spawn } from 'node:child_process'
import { mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { homedir, tmpdir } from 'node:os'
import { fileURLToPath } from 'node:url'

const ROOT = join(dirname(fileURLToPath(import.meta.url)), '..')
const APP_URL = process.env.IRIS_WEB_BASE ?? 'http://127.0.0.1:3309'
const SHOTS_DIR = process.env.IRIS_SHOTS_DIR ?? join(ROOT, 'changelogs', 'assets')
const RESULTS_FILE = process.env.IRIS_RESULTS_FILE ?? join(SHOTS_DIR, 'p8-results.json')
const PASSPHRASE_FILE = process.env.IRIS_PASSPHRASE_FILE
const CDP_PORT = Number(process.env.IRIS_CDP_PORT ?? 9231)
const CHROME_BIN =
  process.env.CHROME_BIN ??
  join(homedir(), '.cache/ms-playwright/chromium-1248/chrome-linux64/chrome')
const PROFILE = join(tmpdir(), 'iris-p8-cdp-profile')

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
      last(selector) {
        const list = document.querySelectorAll(selector)
        return list.length ? list[list.length - 1] : null
      },
    }
    // Measure the streaming speech response without changing what the app sees.
    window.__irisStream = { started: 0, firstBytesAt: 0, finished: false }
    // Headless Chromium has no audio device, so play() can reject in the same
    // tick and the "membaca…" state would vanish before a poll sees it. A
    // MutationObserver records the state at the instant it appeared, and a
    // short stub below stands in for a speaker that takes a moment to play a
    // chunk. Neither changes the app's state machine.
    window.__irisSpeak = { appearedAt: 0, text: '', streamFinished: null }
    if (!window.__irisSpeakObserver) {
      window.__irisSpeakObserver = new MutationObserver(() => {
        if (window.__irisSpeak.appearedAt) return
        const el = document.querySelector('[data-testid="speaking-state"]')
        if (!el) return
        window.__irisSpeak.appearedAt = performance.now()
        window.__irisSpeak.text = el.innerText
        window.__irisSpeak.streamFinished = window.__irisStream.finished
      })
      window.__irisSpeakObserver.observe(document.documentElement, {
        childList: true,
        subtree: true,
        characterData: true,
      })
    }
    if (!window.__irisFetchWrapped) {
      window.__irisFetchWrapped = true
      const originalFetch = window.fetch.bind(window)
      window.fetch = async function (...args) {
        const url = typeof args[0] === 'string' ? args[0] : args[0]?.url ?? ''
        const response = await originalFetch(...args)
        if (!url.includes('/api/voice/speak/stream') || !response.body) return response
        window.__irisStream = { started: performance.now(), firstBytesAt: 0, finished: false }
        const reader = response.body.getReader()
        const measured = new ReadableStream({
          async pull(controller) {
            const { value, done } = await reader.read()
            if (done) {
              window.__irisStream.finished = true
              controller.close()
              return
            }
            if (!window.__irisStream.firstBytesAt) window.__irisStream.firstBytesAt = performance.now()
            controller.enqueue(value)
          },
          cancel() {
            return reader.cancel()
          },
        })
        return new Response(measured, { status: response.status, headers: response.headers })
      }
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
  const file = join(SHOTS_DIR, `p8-${name}.png`)
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
  await setInput('#room-topic', 'P8 biaya, suara, ulangi')
  await waitForTrue(`!document.querySelector('[data-testid="create-room"]').disabled`, 5000)
  await click('[data-testid="create-room"]')
  await waitForTrue(`location.pathname.startsWith('/r/')`, 40000, 200)
  await waitForTrue(`!!document.querySelector('[data-testid="empty-room"]')`, 15000)
}

async function send(text) {
  await setInput('[data-testid="composer-input"]', text)
  await waitForTrue(`!document.querySelector('[data-testid="send-button"]').disabled`, 5000)
  await click('[data-testid="send-button"]')
}

async function assistantCount() {
  return evaluate(`document.querySelectorAll('[data-testid="assistant-message"]').length`)
}

async function main() {
  mkdirSync(SHOTS_DIR, { recursive: true })
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
    await createRoom('P8 ponsel')
    console.log('[PASS] room created at 390x844')

    // --- one real turn -----------------------------------------------------
    await send('Jawab satu kalimat: halo dari p8.')
    await waitForTrue(`!!document.querySelector('[data-testid="streaming-turn"]')`, 20000)
    await waitForTrue(`document.querySelectorAll('[data-testid="assistant-message"]').length >= 1`, 180000, 250)
    console.log('[PASS] one real reply is in the transcript')

    // --- usage chip + chart ------------------------------------------------
    await click('[data-testid="room-menu-more"]')
    await waitForTrue(`!!document.querySelector('[data-testid="open-inspector"]')`, 5000)
    await click('[data-testid="open-inspector"]')
    await waitForTrue(`!!document.querySelector('[data-testid="usage-panel"]')`, 8000)
    await waitForTrue(
      `(() => {
        const chip = document.querySelector('[data-testid="usage-chip"]')
        return chip && /hari ini/.test(chip.innerText) && !/Memuat biaya/.test(chip.innerText)
      })()`,
      15000,
      200,
    )
    await waitForTrue(`!!document.querySelector('[data-testid="usage-chart"]')`, 15000)
    const panel = await evaluate(`(() => {
      const panel = document.querySelector('[data-testid="usage-panel"]')
      const chip = document.querySelector('[data-testid="usage-chip"]')
      const bars = document.querySelectorAll('[data-testid="usage-chart"] [data-day]')
      const total = document.querySelector('[data-testid="usage-total"]')
      const rooms = document.querySelectorAll('[data-testid="usage-rooms"] li')
      return {
        chip: chip?.innerText ?? '',
        bars: bars.length,
        total: total?.innerText ?? '',
        rooms: rooms.length,
        width: panel?.getBoundingClientRect().width ?? 0,
        viewport: window.innerWidth,
        inner: document.querySelector('[data-testid="room-inspector"]')?.innerText ?? '',
      }
    })()`)
    assert('390', 'usage chip shows today and a turn count', /hari ini .* · \d+ turn/.test(panel.chip), panel.chip)
    assert('390', 'usage chart has 14 day columns', panel.bars === 14, String(panel.bars))
    assert('390', 'usage total is rendered', /\d+ turn/.test(panel.total), panel.total)
    assert('390', 'top rooms are listed', panel.rooms >= 1, String(panel.rooms))
    assert('390', 'usage panel fits the phone width', panel.width <= panel.viewport + 1, `${panel.width}/${panel.viewport}`)
    await sleep(350)
    await screenshot('390-usage')
    await click('[data-testid="close-inspector"]')

    // --- retry: 44px, disabled while working, old reply dimmed -------------
    await click('[data-testid="message-menu"]')
    await waitForTrue(`!!document.querySelector('[data-testid="retry-message"]')`, 5000)
    const menu = await evaluate(`(() => {
      const item = document.querySelector('[data-testid="retry-message"]')
      const trigger = document.querySelector('[data-testid="message-menu"]')
      const rect = item?.getBoundingClientRect()
      const triggerRect = trigger?.getBoundingClientRect()
      return {
        label: item?.innerText ?? '',
        disabled: Boolean(item?.disabled),
        height: rect?.height ?? 0,
        width: rect?.width ?? 0,
        trigger: triggerRect ? { width: triggerRect.width, height: triggerRect.height } : null,
      }
    })()`)
    assert('390', 'retry label is "Coba lagi"', /Coba lagi/.test(menu.label), menu.label)
    assert('390', 'retry target is at least 44px tall', menu.height >= 44, JSON.stringify(menu))
    assert('390', 'retry trigger is 44x44', menu.trigger && menu.trigger.width >= 44 && menu.trigger.height >= 44, JSON.stringify(menu.trigger))
    assert('390', 'retry is enabled while idle', menu.disabled === false, JSON.stringify(menu))

    await click('[data-testid="retry-message"]')
    await waitForTrue(`!!document.querySelector('[data-testid="streaming-turn"]')`, 15000)
    // The turn is running: the same menu item must be disabled now.
    await click('[data-testid="message-menu"]')
    await waitForTrue(`!!document.querySelector('[data-testid="retry-message"]')`, 5000)
    const busyDisabled = await evaluate(
      `document.querySelector('[data-testid="retry-message"]')?.disabled === true`,
    )
    assert('390', 'retry is disabled while the room is working', Boolean(busyDisabled))
    await click('[data-testid="message-menu"]')

    await waitForTrue(`document.querySelectorAll('[data-testid="assistant-message"]').length >= 2`, 180000, 250)
    const retried = await evaluate(`(() => {
      const rows = [...document.querySelectorAll('[data-testid="assistant-message"]')]
      const old = rows[0]
      return {
        count: rows.length,
        superseded: old?.getAttribute('data-superseded'),
        opacity: old ? getComputedStyle(old).opacity : '',
        label: old?.querySelector('[data-testid="superseded-label"]')?.innerText ?? '',
        users: document.querySelectorAll('[data-testid="user-message"]').length,
      }
    })()`)
    assert('390', 'retry added a reply without adding a user message', retried.count === 2 && retried.users === 1, JSON.stringify(retried))
    assert('390', 'old reply is dimmed and labelled', retried.superseded === 'true' && Number(retried.opacity) < 1 && /digantikan/.test(retried.label), JSON.stringify(retried))
    await sleep(300)
    await screenshot('390-retry')

    // --- streaming speech --------------------------------------------------
    // Stand in for a speaker: headless has no audio output, and a rejected
    // play() would otherwise skip the chunk instantly. The app's streaming
    // state machine is untouched; only the element's play() duration is faked.
    await evaluate(`(() => {
      HTMLMediaElement.prototype.play = function () {
        return new Promise((resolve) => setTimeout(resolve, 350))
      }
      return true
    })()`)
    await click('[data-testid="assistant-message"] [data-testid="speaker-button"]', 1)
    await waitForTrue(`window.__irisSpeak.appearedAt > 0`, 20000, 60)
    const speaking = await evaluate(`(() => ({
      text: window.__irisSpeak.text,
      appearedAt: window.__irisSpeak.appearedAt,
      finishedAtAppearance: window.__irisSpeak.streamFinished,
      finishedNow: window.__irisStream.finished,
      started: window.__irisStream.started,
    }))()`)
    assert('390', 'the "membaca…" state appears on the first chunk', /membaca/.test(speaking.text), JSON.stringify(speaking))
    assert('390', 'speech starts before the response finishes', speaking.finishedAtAppearance === false, JSON.stringify(speaking))
    await sleep(250)
    await screenshot('390-speaking')
    await waitForTrue(`window.__irisStream.finished === true`, 30000, 100)

    console.log('PASS: P8 browser check')
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
