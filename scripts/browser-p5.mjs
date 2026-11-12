#!/usr/bin/env node
/**
 * P5 browser check — real Chromium over the DevTools protocol, no Playwright.
 *
 * Uses the real room and real Hermes message seeded by scripts/p5-browser.sh.
 * For each viewport (390x844 and 1440x900):
 *   - sign in, search a word from the message in the rooms panel;
 *   - open the result by deep link and prove the room landed on that message;
 *   - export from the room menu and read the downloaded markdown;
 *   - assert the transcript contains the message and the attachment name.
 *
 * Screenshots and p5-results.json land in changelogs/assets/.
 */

import { spawn } from 'node:child_process'
import { mkdirSync, readdirSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { homedir, tmpdir } from 'node:os'
import { fileURLToPath } from 'node:url'

const ROOT = join(dirname(fileURLToPath(import.meta.url)), '..')
const APP_URL = process.env.IRIS_WEB_BASE ?? 'http://127.0.0.1:3301'
const SHOTS_DIR = process.env.IRIS_SHOTS_DIR ?? join(ROOT, 'changelogs', 'assets')
const RESULTS_FILE = process.env.IRIS_RESULTS_FILE ?? join(SHOTS_DIR, 'p5-results.json')
const PASSPHRASE_FILE = process.env.IRIS_PASSPHRASE_FILE
const ROOM_ID = process.env.IRIS_P5_ROOM_ID
const TOKEN = process.env.IRIS_P5_TOKEN
const ATTACHMENT = process.env.IRIS_P5_ATTACHMENT ?? 'bukti-p5.txt'
const CDP_PORT = Number(process.env.IRIS_CDP_PORT ?? 9227)
const DOWNLOAD_DIR = process.env.IRIS_DOWNLOAD_DIR ?? join(tmpdir(), 'iris-p5-downloads')
const CHROME_BIN =
  process.env.CHROME_BIN ??
  join(homedir(), '.cache/ms-playwright/chromium-1248/chrome-linux64/chrome')
const PROFILE = join(tmpdir(), 'iris-p5-cdp-profile')

if (!PASSPHRASE_FILE) {
  console.error('IRIS_PASSPHRASE_FILE is required')
  process.exit(2)
}
if (!ROOM_ID || !TOKEN) {
  console.error('IRIS_P5_ROOM_ID and IRIS_P5_TOKEN are required')
  process.exit(2)
}

const passphrase = readFileSync(PASSPHRASE_FILE, 'utf8').trim()
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
const results = []
const shots = []

function record(scope, name, pass, detail = '') {
  results.push({ scope, name, pass, detail })
  console.log(`[${pass ? 'PASS' : 'FAIL'}] ${scope}: ${name}${detail ? ` — ${detail}` : ''}`)
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
      if (message.error) reject(new Error(`${message.error.message} (method ${message.method ?? '?'})`))
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
      if (response.ok) return await response.json()
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

async function waitForSelector(selector, timeoutMs = 15000) {
  return waitForTrue(`!!document.querySelector(${JSON.stringify(selector)})`, timeoutMs)
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
  const clicked = await evaluate(`window.__iris.click(${JSON.stringify(selector)})`)
  if (!clicked) throw new Error(`cannot click ${selector}`)
}

async function setInput(selector, value) {
  const ok = await evaluate(
    `window.__iris.set(${JSON.stringify(selector)}, ${JSON.stringify(value)})`,
  )
  if (!ok) throw new Error(`cannot fill ${selector}`)
}

async function screenshot(name) {
  // Let the compositor catch up with the last DOM change, otherwise a phone
  // sheet can be captured one frame before the search results paint.
  await evaluate(`new Promise((resolve) =>
    requestAnimationFrame(() => requestAnimationFrame(() => setTimeout(resolve, 180))))`)
  const result = await cdp.send('Page.captureScreenshot', { format: 'png', fromSurface: true }, sessionId)
  const file = join(SHOTS_DIR, `p5-${name}.png`)
  writeFileSync(file, Buffer.from(result.data, 'base64'))
  shots.push(file)
  console.log(`[SHOT] ${file}`)
  return file
}

function viewportMetrics() {
  return evaluate(`(() => {
    const doc = document.documentElement
    return {
      innerWidth: window.innerWidth,
      innerHeight: window.innerHeight,
      scrollWidth: doc.scrollWidth,
      clientWidth: doc.clientWidth,
    }
  })()`)
}

function clearDownloads() {
  rmSync(DOWNLOAD_DIR, { recursive: true, force: true })
  mkdirSync(DOWNLOAD_DIR, { recursive: true })
}

async function waitForDownload(timeoutMs = 15000) {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    const names = readdirSync(DOWNLOAD_DIR).filter((name) => name.endsWith('.md'))
    if (names.length > 0) {
      const file = join(DOWNLOAD_DIR, names[0])
      // The writer may still be flushing; wait for a stable size.
      const first = statSync(file).size
      await sleep(200)
      if (statSync(file).size === first && first > 0) return file
    }
    await sleep(150)
  }
  throw new Error(`no markdown download appeared in ${DOWNLOAD_DIR}`)
}

async function runViewport(label, width, height) {
  const phone = width < 500
  // The desktop aside stays in the DOM on a phone (hidden with CSS), so scope
  // every search query to the panel the user can actually see.
  const panel = phone ? '[aria-label="Daftar room"]' : 'aside'
  const sel = (name) => `${panel} [data-testid="${name}"]`
  await cdp.send(
    'Emulation.setDeviceMetricsOverride',
    { width, height, deviceScaleFactor: 1, mobile: phone },
    sessionId,
  )
  await cdp.send('Network.clearBrowserCookies', {}, sessionId)
  await navigate(`${APP_URL}/`)
  await evaluate(`localStorage.clear(); sessionStorage.clear(); true`)
  await waitForTrue(`location.pathname === '/login'`, 20000)
  await waitForSelector('#passphrase')
  await setInput('#passphrase', passphrase)
  await click('[data-testid="login-submit"]')
  await waitForTrue(`location.pathname === '/rooms'`, 25000, 200)
  record(label, 'signed in from the served SPA', true, `${width}x${height}`)

  if (phone) {
    await click('[data-testid="rooms-menu"]')
    await waitForSelector('[aria-label="Daftar room"]')
    await sleep(250)
  }
  await waitForSelector(sel('room-search'))

  // An honest empty state first: a word nobody wrote must say so.
  await setInput(sel('room-search'), 'zzzunseentermzzz')
  await waitForSelector(sel('search-empty'), 15000)
  const emptyCopy = await evaluate(
    `document.querySelector(${JSON.stringify(sel('search-empty'))})?.innerText ?? ''`,
  )
  record(label, 'unseen word shows an honest empty state', /Tidak ada pesan/.test(emptyCopy), emptyCopy.replace(/\s+/g, ' ').slice(0, 80))

  // The real search.
  await setInput(sel('room-search'), TOKEN)
  await waitForSelector(sel('search-result'), 15000)
  const found = await evaluate(`(() => {
    const panel = document.querySelector(${JSON.stringify(sel('search-panel'))})
    const all = [...(panel?.querySelectorAll('[data-testid="search-result"]') ?? [])]
    const match = all.find((el) => el.innerText.includes(${JSON.stringify(TOKEN)})) ?? all[0]
    return {
      count: all.length,
      text: match?.innerText ?? '',
      marks: panel?.querySelectorAll('mark').length ?? 0,
    }
  })()`)
  record(
    label,
    'search from the rooms panel finds the message',
    found.count >= 1 && found.text.includes(TOKEN) && found.marks >= 1,
    `${found.count} result(s), ${found.marks} highlight(s)`,
  )
  await screenshot(`${label}-search`)

  const opened = await evaluate(`(() => {
    const all = [...document.querySelectorAll(${JSON.stringify(sel('search-result'))})]
    const match = all.find((el) => el.innerText.includes(${JSON.stringify(TOKEN)})) ?? all[0]
    if (!match) return false
    match.click()
    return true
  })()`)
  if (!opened) throw new Error('no search result could be opened')
  await waitForTrue(`location.pathname === '/r/${ROOM_ID}'`, 20000, 100)
  await waitForTrue(
    `(() => {
      const m = new URLSearchParams(location.search).get('m')
      const el = m ? document.querySelector('[data-message-id="' + m + '"]') : null
      return Boolean(el && el.innerText.includes(${JSON.stringify(TOKEN)}))
    })()`,
    15000,
    100,
  )
  await waitForTrue(
    `(() => {
      const m = new URLSearchParams(location.search).get('m')
      const el = m ? document.querySelector('[data-message-id="' + m + '"]') : null
      return Boolean(el && el.classList.contains('ring-2'))
    })()`,
    5000,
    50,
  )
  const landed = await evaluate(`(() => {
    const m = new URLSearchParams(location.search).get('m')
    const el = m ? document.querySelector('[data-message-id="' + m + '"]') : null
    return {
      m,
      text: el?.innerText ?? '',
      highlighted: Boolean(el?.className.includes('ring-2')),
      textHasToken: (el?.innerText ?? '').includes(${JSON.stringify(TOKEN)}),
    }
  })()`)
  record(
    label,
    'clicking a result deep-links to that message',
    landed.highlighted && landed.textHasToken,
    `message ${landed.m}, highlighted=${landed.highlighted}, textHasToken=${landed.textHasToken}`,
  )
  await screenshot(`${label}-room`)

  clearDownloads()
  await click('[data-testid="room-menu-more"]')
  await waitForSelector('[data-testid="export-room"]')
  await click('[data-testid="export-room"]')
  const file = await waitForDownload()
  const transcript = readFileSync(file, 'utf8')
  const name = file.split('/').pop()
  const checks = [
    transcript.includes(TOKEN),
    transcript.includes('**Kamu**'),
    transcript.includes('**Hermes**'),
    transcript.includes(ATTACHMENT),
    name.endsWith('.md'),
  ]
  record(
    label,
    'export downloads a markdown transcript with the message and attachment',
    checks.every(Boolean),
    `${name}, ${transcript.length} bytes`,
  )

  const finalMetrics = await viewportMetrics()
  record(
    label,
    'no horizontal overflow after search and export',
    finalMetrics.scrollWidth <= finalMetrics.clientWidth,
    `scrollWidth=${finalMetrics.scrollWidth} clientWidth=${finalMetrics.clientWidth}`,
  )

  return { file: name, bytes: transcript.length, viewport: `${width}x${height}` }
}

mkdirSync(SHOTS_DIR, { recursive: true })
const chrome = launchChrome()
const outcome = { startedAt: new Date().toISOString(), roomId: ROOM_ID, checks: results }

try {
  const version = await waitForCDP()
  cdp = await connectCDP(version.webSocketDebuggerUrl)
  const { targetId } = await cdp.send('Target.createTarget', { url: 'about:blank' })
  const attached = await cdp.send('Target.attachToTarget', { targetId, flatten: true })
  sessionId = attached.sessionId
  await cdp.send('Page.enable', {}, sessionId)
  await cdp.send('Runtime.enable', {}, sessionId)
  await cdp.send('Network.enable', {}, sessionId)
  await cdp.send('DOM.enable', {}, sessionId)
  await cdp.send('Browser.setDownloadBehavior', {
    behavior: 'allow',
    downloadPath: DOWNLOAD_DIR,
    eventsEnabled: true,
  })

  outcome.phone = await runViewport('phone', 390, 844)
  outcome.desktop = await runViewport('desktop', 1440, 900)

  const failed = results.filter((r) => !r.pass)
  outcome.finishedAt = new Date().toISOString()
  outcome.failed = failed.map((f) => `${f.scope}: ${f.name}`)
  outcome.screenshots = shots
  writeFileSync(RESULTS_FILE, JSON.stringify(outcome, null, 2))

  console.log(
    `\n${results.length - failed.length}/${results.length} checks passed${failed.length ? ` — FAILURES: ${failed.map((f) => `${f.scope}: ${f.name}`).join(', ')}` : ''}`,
  )
  process.exitCode = failed.length ? 1 : 0
} catch (error) {
  console.error(`DRIVER ERROR: ${error.message}`)
  outcome.finishedAt = new Date().toISOString()
  outcome.error = error.message
  outcome.checks = results
  writeFileSync(RESULTS_FILE, JSON.stringify(outcome, null, 2))
  process.exitCode = 2
} finally {
  try {
    if (cdp) await cdp.send('Browser.close', undefined, undefined)
  } catch {
    /* ignore */
  }
  chrome.kill('SIGKILL')
}
