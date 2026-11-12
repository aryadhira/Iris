#!/usr/bin/env node
/**
 * P3 browser check — real Chromium over the DevTools Protocol, no Playwright.
 *
 * Proves the room is not a black box:
 *   - header chip switches bekerja → menunggu proses → idle
 *   - the pending follow-up is a live line with a countdown
 *   - "hentikan" cancels the chain and the reason is visible
 *   - a finished assistant message has a collapsed "apa yang Hermes lakukan"
 *     timeline with real tool rows (never a fake row when there are none)
 *
 * Real irisd + real Hermes. Screenshots land in changelogs/assets/.
 */

import { spawn } from 'node:child_process'
import { mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { homedir, tmpdir } from 'node:os'
import { fileURLToPath } from 'node:url'

const ROOT = join(dirname(fileURLToPath(import.meta.url)), '..')
const APP_URL = process.env.IRIS_WEB_BASE ?? 'http://127.0.0.1:4175'
const SHOTS_DIR = process.env.IRIS_SHOTS_DIR ?? join(ROOT, 'changelogs', 'assets')
const PASSPHRASE_FILE = process.env.IRIS_PASSPHRASE_FILE
const CDP_PORT = Number(process.env.IRIS_CDP_PORT ?? 9225)
const CHROME_BIN =
  process.env.CHROME_BIN ??
  join(homedir(), '.cache/ms-playwright/chromium-1248/chrome-linux64/chrome')
const PROFILE = join(tmpdir(), 'iris-p3-cdp-profile')

if (!PASSPHRASE_FILE) {
  console.error('IRIS_PASSPHRASE_FILE is required')
  process.exit(2)
}
const passphrase = readFileSync(PASSPHRASE_FILE, 'utf8').trim()
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))

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
      '--window-size=1440,900',
      '--no-first-run',
      '--no-default-browser-check',
      '--disable-dev-shm-usage',
      '--no-sandbox',
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
    { expression, awaitPromise: true, returnByValue: true },
    sessionId,
  )
  if (result.exceptionDetails) {
    throw new Error(
      `page eval failed: ${result.exceptionDetails.exception?.description ?? result.exceptionDetails.text}`,
    )
  }
  return result.result.value
}

async function waitForTrue(expression, timeoutMs, intervalMs = 150) {
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
      click(selector) {
        const el = document.querySelector(selector)
        if (!el) return false
        el.click()
        return true
      },
    }
    return true
  })()`)
}

async function click(selector) {
  const ok = await evaluate(`window.__iris.click(${JSON.stringify(selector)})`)
  if (!ok) throw new Error(`cannot click ${selector}`)
}

async function setInput(selector, value) {
  const ok = await evaluate(`window.__iris.set(${JSON.stringify(selector)}, ${JSON.stringify(value)})`)
  if (!ok) throw new Error(`cannot fill ${selector}`)
}

async function screenshot(name) {
  const result = await cdp.send('Page.captureScreenshot', { format: 'png' }, sessionId)
  const file = join(SHOTS_DIR, `p3-${name}.png`)
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
  await setInput('#room-topic', 'P3 continuity')
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

async function chipState() {
  return evaluate(
    `document.querySelector('[data-testid="room-state-chip"]')?.innerText.trim() ?? ''`,
  )
}

const JOB_PROMPT =
  'Jalankan pekerjaan latar berikut dengan tool terminal(background=true, notify=true): sleep 30; echo IRIS-DONE. JANGAN menunggu hasilnya di turn ini — akhiri turn sekarang. Laporkan id prosesnya, lalu akhiri dengan baris persis: STATUS: RUNNING — menunggu proses 30 detik.'

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
    await createRoom('P3 jendela 390')
    console.log('[PASS] room created at 390x844')

    // --- wait in-turn / live continuation line ---------------------------
    await send(JOB_PROMPT)
    await waitForTrue(`!!document.querySelector('[data-testid="working-state"]')`, 20000, 80)
    const workingChip = await chipState()
    console.log(`[INFO] chip while streaming: ${workingChip}`)
    await waitForTrue(`!document.querySelector('[data-testid="working-state"]')`, 90000, 200)
    await waitForTrue(`!!document.querySelector('[data-testid="continuation-line"]')`, 30000, 100)
    const liveText = await evaluate(
      `document.querySelector('[data-testid="continuation-line"]')?.innerText ?? ''`,
    )
    const waitingChip = await chipState()
    if (!/lanjut otomatis dalam \d+ detik/.test(liveText)) {
      throw new Error(`countdown is missing: ${liveText}`)
    }
    if (waitingChip !== 'menunggu proses') {
      throw new Error(`chip = ${waitingChip}, want menunggu proses`)
    }
    await screenshot('390-live-continuation')
    console.log(`[PASS] live line with countdown at 390: ${liveText.replace(/\s+/g, ' ')}`)

    // Nobody types. The result must arrive.
    await waitForTrue(
      `[...document.querySelectorAll('[data-testid="assistant-message"]')].some((el) => el.innerText.includes('STATUS: DONE') && el.innerText.includes('IRIS-DONE'))`,
      150000,
      300,
    )
    await waitForTrue(
      `!document.querySelector('[data-testid="continuation-line"]')`,
      15000,
      200,
    )
    const idleChip = await chipState()
    if (idleChip !== 'idle') throw new Error(`chip after result = ${idleChip}, want idle`)

    const timeline = await evaluate(`(() => {
      const messages = [...document.querySelectorAll('[data-testid="assistant-message"]')]
      const last = messages[messages.length - 1]
      const details = last?.querySelector('[data-testid="run-timeline"]')
      if (!details) return null
      details.open = true
      return {
        summary: details.querySelector('summary')?.innerText.trim() ?? '',
        tools: [...details.querySelectorAll('[data-testid="run-timeline-list"] li')].map((li) => li.innerText.trim()),
      }
    })()`)
    if (!timeline || timeline.tools.length === 0) {
      throw new Error('the finished reply has no tool timeline')
    }
    if (timeline.summary !== 'apa yang Hermes lakukan') {
      throw new Error(`timeline summary = ${timeline.summary}`)
    }
    await screenshot('390-timeline')
    console.log(`[PASS] timeline: ${JSON.stringify(timeline.tools)}`)

    // --- desktop screenshot of the same room -----------------------------
    await viewport('1440x900', 1440, 900)
    await sleep(300)
    await screenshot('1440-timeline')
    console.log('[PASS] 1440x900 layout has no horizontal overflow')

    // --- cancel the chain -------------------------------------------------
    await viewport('390x844', 390, 844)
    await click('[data-testid="room-menu"]')
    await sleep(300)
    await waitForTrue(`!!document.querySelector('[data-testid="new-room-sidebar"]')`, 10000)
    await click('[data-testid="new-room-sidebar"]')
    await waitForTrue(`!!document.querySelector('#room-name')`, 10000)
    await setInput('#room-name', 'P3 batal 390')
    await setInput('#room-topic', 'P3 cancellation')
    await waitForTrue(`!document.querySelector('[data-testid="create-room"]').disabled`, 5000)
    await click('[data-testid="create-room"]')
    await waitForTrue(`location.pathname.startsWith('/r/')`, 40000, 200)
    await waitForTrue(`!!document.querySelector('[data-testid="empty-room"]')`, 15000)

    await send(JOB_PROMPT)
    await waitForTrue(`!document.querySelector('[data-testid="working-state"]')`, 90000, 200)
    await waitForTrue(`!!document.querySelector('[data-testid="continuation-line"]')`, 30000, 100)
    await screenshot('390-cancel-before')
    await click('[data-testid="continuation-cancel"]')
    await waitForTrue(`!document.querySelector('[data-testid="continuation-line"]')`, 10000, 200)
    await waitForTrue(
      `[...document.querySelectorAll('ol li')].some((el) => el.innerText.includes('kamu menghentikannya'))`,
      10000,
      200,
    )
    await screenshot('390-cancelled')
    console.log('[PASS] hentikan cancelled the chain and left a visible reason')

    // Past the 20 s due time: no follow-up line may come back.
    await sleep(25000)
    const after = await evaluate(`({
      line: !!document.querySelector('[data-testid="continuation-line"]'),
      auto: [...document.querySelectorAll('ol li')].some((el) => el.innerText.includes('lanjut otomatis (1/3)')),
    })`)
    if (after.line || after.auto) {
      throw new Error(`cancelled chain came back: ${JSON.stringify(after)}`)
    }
    console.log('[PASS] no continuation ran after cancellation')

    console.log('PASS: P3 browser check')
  } finally {
    try {
      cdp?.close()
    } catch {
      /* already gone */
    }
    chrome.kill('SIGTERM')
    rmSync(PROFILE, { recursive: true, force: true })
  }
}

main().catch((error) => {
  console.error(`FAIL: ${error.message}`)
  process.exit(1)
})
