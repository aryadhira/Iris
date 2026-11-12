#!/usr/bin/env node
/**
 * P10 browser check — 390x844.
 *
 * Proves the room is no longer owned by the POST that sent the message: start a
 * turn in A, walk to B, come back mid-turn, and A already shows `bekerja` with a
 * running clock and the partial bubble. The final text then arrives without a
 * reload. Screenshots both states.
 */
import { spawn } from 'node:child_process'
import { mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { homedir, tmpdir } from 'node:os'
import { fileURLToPath } from 'node:url'

const ROOT = join(dirname(fileURLToPath(import.meta.url)), '..')
const APP_URL = process.env.IRIS_WEB_BASE ?? 'http://127.0.0.1:3313'
const SHOTS_DIR = process.env.IRIS_SHOTS_DIR ?? join(ROOT, 'changelogs', 'assets')
const RESULTS_FILE = process.env.IRIS_RESULTS_FILE ?? join(SHOTS_DIR, 'p10-results.json')
const PASSPHRASE_FILE = process.env.IRIS_PASSPHRASE_FILE
const CDP_PORT = Number(process.env.IRIS_CDP_PORT ?? 9242)
const CHROME_BIN =
  process.env.CHROME_BIN ??
  join(homedir(), '.cache/ms-playwright/chromium-1248/chrome-linux64/chrome')
const PROFILE = join(tmpdir(), 'iris-p10-cdp-profile')

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
    window.__p10Doc = window.__p10Doc || Math.random().toString(36).slice(2)
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
      clickText(selector, text) {
        const els = [...document.querySelectorAll(selector)]
        const el = els.find((node) => node.getClientRects().length > 0 && node.innerText.includes(text))
        if (!el) return false
        el.click()
        return true
      },
      visible(selector) {
        return [...document.querySelectorAll(selector)].some((el) => el.getClientRects().length > 0)
      },
    }
    return true
  })()`)
}

async function click(selector, index = 0) {
  const expr =
    '(() => { const el = document.querySelectorAll(' +
    JSON.stringify(selector) +
    ')[' +
    index +
    ']; if (!el) return false; el.click(); return true })()'
  const ok = await evaluate(expr)
  if (!ok) throw new Error(`cannot click ${selector}[${index}]`)
}

async function clickText(selector, text) {
  const expr =
    '(() => { const els = [...document.querySelectorAll(' +
    JSON.stringify(selector) +
    ')]; const el = els.find((node) => node.getClientRects().length > 0 && node.innerText.includes(' +
    JSON.stringify(text) +
    ')); if (!el) return false; el.click(); return true })()'
  const ok = await evaluate(expr)
  if (!ok) throw new Error(`cannot click ${selector} containing ${text}`)
}

async function setInput(selector, value) {
  const expr =
    '(() => { const el = document.querySelector(' +
    JSON.stringify(selector) +
    '); if (!el) return false; const proto = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype; Object.getOwnPropertyDescriptor(proto, "value").set.call(el, ' +
    JSON.stringify(value) +
    '); el.dispatchEvent(new Event("input", { bubbles: true })); return true })()'
  const ok = await evaluate(expr)
  if (!ok) throw new Error(`cannot fill ${selector}`)
}

async function screenshot(name) {
  const result = await cdp.send('Page.captureScreenshot', { format: 'png' }, sessionId)
  const file = join(SHOTS_DIR, `p10-${name}.png`)
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
  await installHelpers()
}

async function createRoom(name) {
  const path = await evaluate('location.pathname')
  await click(path === '/rooms' ? '[data-testid="rooms-menu"]' : '[data-testid="room-menu"]')
  await sleep(300)
  await click('[data-testid="new-room-sidebar"]')
  await waitForTrue(`!!document.querySelector('#room-name')`, 10000)
  await setInput('#room-name', name)
  await setInput('#room-topic', 'P10 tinggalkan dan kembali')
  await waitForTrue(`!document.querySelector('[data-testid="create-room"]').disabled`, 5000)
  await click('[data-testid="create-room"]')
  await waitForTrue(
    `location.pathname.startsWith('/r/') && location.pathname !== ${JSON.stringify(path)} && document.querySelector('[data-testid="room-header"]')?.innerText.includes(${JSON.stringify(name)})`,
    40000,
    200,
  )
  await waitForTrue(`!!document.querySelector('[data-testid="composer-input"]')`, 15000)
}

async function openRoomSheet() {
  await click('[data-testid="room-menu"]')
  await waitForTrue(`window.__iris.visible('[data-testid="room-list"]')`, 8000)
}

async function goToRoom(name) {
  await openRoomSheet()
  await clickText('[data-testid="room-list"] button', name)
  await waitForTrue(
    `location.pathname.startsWith('/r/') && document.querySelector('[data-testid="room-header"]')?.innerText.includes(${JSON.stringify(name)})`,
    20000,
    150,
  )
  await waitForTrue(`!!document.querySelector('[data-testid="composer-input"]')`, 15000)
}

function clockSeconds(text) {
  const match = text.match(/(\d+):(\d{2})/)
  if (!match) return -1
  return Number(match[1]) * 60 + Number(match[2])
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
    await createRoom('P10 ponsel A')
    await createRoom('P10 ponsel B')
    await goToRoom('P10 ponsel A')
    const docId = await evaluate(`window.__p10Doc`)
    assert('390', 'two rooms exist and A is open', Boolean(docId), docId)

    const prompt =
      'Jawaban pertamamu HARUS berisi kalimat persis: "Saya sedang mengerjakan tugas yang butuh waktu." ' +
      "Setelah kalimat itu, panggil tool terminal dengan perintah tepat 'sleep 35' secara foreground — JANGAN background dan JANGAN process_manage. " +
      'Tunggu sampai perintah itu selesai di turn ini. Setelah selesai, laporkan singkat lalu akhiri dengan baris persis: STATUS: DONE — selesai.'
    await setInput('[data-testid="composer-input"]', prompt)
    await click('[data-testid="send-button"]')
    await waitForTrue(
      `document.querySelector('[data-testid="room-state-chip"]')?.dataset.state === 'working'`,
      30000,
    )
    await waitForTrue(
      `(document.querySelector('[data-testid="streaming-text"]')?.innerText ?? '').length > 10`,
      40000,
    )
    const workingHeader = await evaluate(
      `document.querySelector('[data-testid="room-state-chip"]')?.innerText ?? ''`,
    )
    const workingClock = clockSeconds(workingHeader)
    await screenshot('390-room-a-working')
    record('390', 'A shows bekerja with a running clock', workingHeader.includes('bekerja') && workingClock >= 0, workingHeader)

    // The sidebar dot must be visible on a 390 px screen, not only desktop.
    await openRoomSheet()
    const dotVisible = await waitForTrue(
      `window.__iris.visible('[data-testid="room-run-dot"]')`,
      15000,
      250,
    )
    assert('390', 'sidebar shows the active-run dot at 390 px', dotVisible)
    await screenshot('390-sidebar-run-dot')
    await clickText('[data-testid="room-list"] button', 'P10 ponsel B')
    await waitForTrue(
      `document.querySelector('[data-testid="room-header"]')?.innerText.includes('P10 ponsel B')`,
      20000,
      150,
    )
    await sleep(3000)

    // Come back through the SPA, not a reload.
    await goToRoom('P10 ponsel A')
    const reentryDoc = await evaluate(`window.__p10Doc`)
    assert('390', 'returning to A did not reload the document', reentryDoc === docId, reentryDoc)
    await waitForTrue(
      `document.querySelector('[data-testid="room-state-chip"]')?.dataset.state === 'working'`,
      10000,
    )
    const reentryHeader = await evaluate(
      `document.querySelector('[data-testid="room-state-chip"]')?.innerText ?? ''`,
    )
    const reentryClock = clockSeconds(reentryHeader)
    const partialVisible = await evaluate(
      `(document.querySelector('[data-testid="streaming-text"]')?.innerText ?? '').length > 10`,
    )
    assert(
      '390',
      'A reopens mid-turn with bekerja and the clock still counting',
      reentryHeader.includes('bekerja') && reentryClock >= workingClock,
      `${workingHeader} -> ${reentryHeader}`,
    )
    assert('390', 'the partial bubble is visible immediately', partialVisible)
    await screenshot('390-room-a-reentry')

    const before = await evaluate(
      `document.querySelectorAll('[data-testid="assistant-message"]').length`,
    )
    await waitForTrue(
      `document.querySelector('[data-testid="room-state-chip"]')?.dataset.state !== 'working'`,
      150000,
      250,
    )
    const finalChip = await evaluate(
      `document.querySelector('[data-testid="room-state-chip"]')?.innerText ?? ''`,
    )
    record('390', 'the header stops claiming to work when the turn ends', !/bekerja/.test(finalChip), finalChip)
    await waitForTrue(
      `document.querySelectorAll('[data-testid="assistant-message"]').length > ${before}`,
      30000,
      250,
    )
    const finalText = await evaluate(
      `[...document.querySelectorAll('[data-testid="assistant-message"]')].at(-1)?.innerText ?? ''`,
    )
    assert('390', 'the final answer appears without a reload', finalText.length > 0, finalText.slice(0, 80))
    const statusLeak = finalText.includes('STATUS:')
    assert('390', 'the assistant answer shows no STATUS line', statusLeak === false)
    await screenshot('390-room-a-final')

    console.log('PASS: P10 browser check')
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
  process.exitCode = 1
})
