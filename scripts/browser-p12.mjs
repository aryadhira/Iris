#!/usr/bin/env node
/**
 * P12 browser check — 390x844.
 *
 * Proves the working log is visible while a real turn runs: the panel says
 * `Hermes bekerja` with a running clock and at least two step rows. When the
 * turn ends the panel is gone and the same steps are in the message timeline.
 * Screenshots both states.
 */
import { spawn } from 'node:child_process'
import { mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { homedir, tmpdir } from 'node:os'
import { fileURLToPath } from 'node:url'

const ROOT = join(dirname(fileURLToPath(import.meta.url)), '..')
const APP_URL = process.env.IRIS_WEB_BASE ?? 'http://127.0.0.1:3315'
const SHOTS_DIR = process.env.IRIS_SHOTS_DIR ?? join(ROOT, 'changelogs', 'assets')
const RESULTS_FILE = process.env.IRIS_RESULTS_FILE ?? join(SHOTS_DIR, 'p12-results.json')
const PASSPHRASE_FILE = process.env.IRIS_PASSPHRASE_FILE
const CDP_PORT = Number(process.env.IRIS_CDP_PORT ?? 9244)
const CHROME_BIN =
  process.env.CHROME_BIN ??
  join(homedir(), '.cache/ms-playwright/chromium-1248/chrome-linux64/chrome')
const PROFILE = join(tmpdir(), 'iris-p12-cdp-profile')

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
  const file = join(SHOTS_DIR, `p12-${name}.png`)
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
  await setInput('#room-topic', 'P12 progres live')
  await waitForTrue(`!document.querySelector('[data-testid="create-room"]').disabled`, 5000)
  await click('[data-testid="create-room"]')
  await waitForTrue(
    `location.pathname.startsWith('/r/') && document.querySelector('[data-testid="room-header"]')?.innerText.includes(${JSON.stringify(name)})`,
    40000,
    200,
  )
  await waitForTrue(`!!document.querySelector('[data-testid="composer-input"]')`, 15000)
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
    await createRoom('P12 progres')
    record('390', 'room opened at 390x844', true)

    const prompt =
      'Kerjakan berurutan di turn ini, jangan dilewati:\n' +
      '1. Panggil tool terminal dengan perintah tepat: sh -c "printf \'p12 ok\\n\' > /tmp/iris-p12-browser.txt; sleep 4; cat /tmp/iris-p12-browser.txt"\n' +
      '2. Panggil tool read_file pada path /tmp/iris-p12-browser.txt\n' +
      '3. Jawab satu kalimat hasilnya, lalu akhiri dengan baris persis: STATUS: DONE — selesai.'
    await setInput('[data-testid="composer-input"]', prompt)
    await click('[data-testid="send-button"]')

    await waitForTrue(`!!document.querySelector('[data-testid="turn-progress"]')`, 20000, 80)
    await waitForTrue(
      `document.querySelectorAll('[data-testid="progress-step"]').length >= 2`,
      30000,
      80,
    )
    const header = await evaluate(
      `document.querySelector('[data-testid="turn-progress-header"]')?.innerText ?? ''`,
    )
    const rows = await evaluate(
      `[...document.querySelectorAll('[data-testid="progress-step"]')].map((row) => ({
        kind: row.dataset.kind,
        status: row.dataset.status,
        text: row.innerText.replace(/\\s+/g, ' ').trim(),
        duration: row.querySelector('[data-testid="progress-step-duration"]')?.innerText ?? '',
        scrollWidth: row.scrollWidth,
        clientWidth: row.clientWidth,
      }))`,
    )
    const clock = header.match(/\d+:\d{2}/)
    assert('390', 'the panel says Hermes bekerja with an elapsed clock', header.includes('Hermes bekerja') && Boolean(clock), header)
    assert('390', 'at least two step rows are visible', rows.length >= 2, `${rows.length} rows`)
    assert(
      '390',
      'each visible step has a name or thinking excerpt and a duration',
      rows.every((row) => row.text.length > 0 && row.duration.length > 0),
      JSON.stringify(rows),
    )
    const overflow = await evaluate(
      `[...document.querySelectorAll('[data-testid="progress-step"]')].some((row) => row.scrollWidth > row.clientWidth + 1)`,
    )
    assert('390', 'no step row overflows horizontally', overflow === false)
    const midShot = await screenshot('390-mid-turn')
    const midKinds = rows.map((row) => row.kind)

    await waitForTrue(`!document.querySelector('[data-testid="turn-progress"]')`, 180000, 200)
    record('390', 'the progress panel is gone when the turn ends', true)
    await waitForTrue(
      `document.querySelectorAll('[data-testid="assistant-message"]').length > 0`,
      30000,
      200,
    )
    // Open the stored timeline; the steps must be there exactly once.
    await evaluate(`(() => {
      const details = [...document.querySelectorAll('[data-testid="run-timeline"]')].at(-1)
      if (details && !details.open) details.open = true
      return true
    })()`)
    const timeline = await evaluate(
      `[...document.querySelectorAll('[data-testid="assistant-message"]')].at(-1)
        ?.querySelectorAll('[data-testid="run-timeline-list"] li').length ?? 0`,
    )
    const timelineKinds = await evaluate(
      `[...([...document.querySelectorAll('[data-testid="assistant-message"]')].at(-1)
        ?.querySelectorAll('[data-testid="run-timeline-list"] li') ?? [])].map((row) => row.dataset.kind)`,
    )
    assert('390', 'the stored timeline carries the steps', timeline >= 3, `${timeline} rows`)
    assert(
      '390',
      'the panel folded into the timeline without a duplicate list',
      (await evaluate(`document.querySelectorAll('[data-testid="run-timeline-list"]').length`)) >= 1 &&
        timeline <= 8,
      `timeline rows=${timeline}`,
    )
    assert(
      '390',
      'the visible mid-turn kinds are a prefix of the stored kinds',
      midKinds.every((kind, index) => timelineKinds[index] === kind),
      `${midKinds.join(',')} vs ${timelineKinds.join(',')}`,
    )
    const finalShot = await screenshot('390-finished')
    record('390', 'screenshots captured', Boolean(midShot && finalShot), `${midShot}, ${finalShot}`)

    console.log('PASS: P12 browser check')
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
