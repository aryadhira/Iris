#!/usr/bin/env node
/**
 * P2 browser check — real Chromium over the DevTools Protocol, no Playwright,
 * no npm dependencies.
 *
 * For each viewport it:
 *   1. signs in and creates a room through the UI
 *   2. uploads a real PNG through the paperclip input and watches the chip
 *      reach "siap"
 *   3. sends it, sees the thumbnail, opens the lightbox, and compares the
 *      downloaded bytes with the upload
 *   4. asks Hermes for a small markdown file, waits for the artifact card, and
 *      compares the downloaded bytes with the file the agent wrote on disk
 *   5. opens the room library, checks both sources, and exercises the two-step
 *      delete without deleting the artifact
 *
 * Real irisd + real Hermes. Screenshots and a result JSON land in
 * changelogs/assets/.
 */

import { spawn } from 'node:child_process'
import {
  appendFileSync,
  mkdirSync,
  readFileSync,
  readdirSync,
  rmSync,
  statSync,
  writeFileSync,
} from 'node:fs'
import { dirname, join } from 'node:path'
import { homedir, tmpdir } from 'node:os'
import { fileURLToPath } from 'node:url'

const ROOT = join(dirname(fileURLToPath(import.meta.url)), '..')
const APP_URL = process.env.IRIS_WEB_BASE ?? 'http://127.0.0.1:4175'
const SHOTS_DIR = process.env.IRIS_SHOTS_DIR ?? join(ROOT, 'changelogs', 'assets')
const RESULTS_FILE = process.env.IRIS_RESULTS_FILE ?? join(SHOTS_DIR, 'p2-results.json')
const PASSPHRASE_FILE = process.env.IRIS_PASSPHRASE_FILE
const DATA_DIR = process.env.IRIS_DATA_DIR
const TEMP_DIR = process.env.IRIS_TEMP_DIR ?? tmpdir()
const CDP_PORT = Number(process.env.IRIS_CDP_PORT ?? 9224)
const CHROME_BIN =
  process.env.CHROME_BIN ??
  join(homedir(), '.cache/ms-playwright/chromium-1248/chrome-linux64/chrome')
const PROFILE = join(tmpdir(), 'iris-p2-cdp-profile')

const PNG_BASE64 =
  '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'
const PNG_BYTES = Buffer.from(PNG_BASE64, 'base64')

if (!PASSPHRASE_FILE) {
  console.error('IRIS_PASSPHRASE_FILE is required')
  process.exit(2)
}
if (!DATA_DIR) {
  console.error('IRIS_DATA_DIR is required')
  process.exit(2)
}
const passphrase = readFileSync(PASSPHRASE_FILE, 'utf8').trim()

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))

// ---------------------------------------------------------------------------
// Minimal CDP client (same shape as the P1b driver)
// ---------------------------------------------------------------------------

async function connectCDP(wsUrl) {
  const ws = new WebSocket(wsUrl)
  await new Promise((resolve, reject) => {
    ws.onopen = resolve
    ws.onerror = () => reject(new Error('WebSocket connect failed'))
  })

  let nextId = 0
  const pending = new Map()
  const listeners = new Set()

  ws.onmessage = (event) => {
    const message = JSON.parse(String(event.data))
    if (message.id != null && pending.has(message.id)) {
      const { resolve, reject } = pending.get(message.id)
      pending.delete(message.id)
      if (message.error) reject(new Error(`${message.error.message} (method ${message.method ?? '?'})`))
      else resolve(message.result)
    } else if (message.method) {
      for (const listener of listeners) listener(message)
    }
  }

  return {
    on: (listener) => listeners.add(listener),
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
  const args = [
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
  ]
  const child = spawn(CHROME_BIN, args, { stdio: ['ignore', 'ignore', 'pipe'] })
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

// ---------------------------------------------------------------------------
// Page helpers
// ---------------------------------------------------------------------------

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
      rect(selector) {
        const el = document.querySelector(selector)
        if (!el) return null
        const r = el.getBoundingClientRect()
        return { top: r.top, left: r.left, right: r.right, bottom: r.bottom, width: r.width, height: r.height }
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
  const result = await cdp.send('Page.captureScreenshot', { format: 'png' }, sessionId)
  const file = join(SHOTS_DIR, `p2-${name}.png`)
  writeFileSync(file, Buffer.from(result.data, 'base64'))
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

async function setFileInput(selector, filePath) {
  const { root } = await cdp.send('DOM.getDocument', { depth: -1, pierce: true }, sessionId)
  const { nodeId } = await cdp.send(
    'DOM.querySelector',
    { nodeId: root.nodeId, selector },
    sessionId,
  )
  if (!nodeId) throw new Error(`file input not found: ${selector}`)
  await cdp.send('DOM.setFileInputFiles', { files: [filePath], nodeId }, sessionId)
}

/** Fetch a file through the page's cookie and return its bytes + headers. */
async function readDownload(href) {
  const result = await evaluate(`(async () => {
    const response = await fetch(${JSON.stringify(href)}, { credentials: 'same-origin' })
    const buffer = new Uint8Array(await response.arrayBuffer())
    let binary = ''
    for (let i = 0; i < buffer.length; i += 0x8000) {
      binary += String.fromCharCode.apply(null, buffer.subarray(i, i + 0x8000))
    }
    return {
      status: response.status,
      headers: {
        contentType: response.headers.get('content-type'),
        disposition: response.headers.get('content-disposition'),
        nosniff: response.headers.get('x-content-type-options'),
        cacheControl: response.headers.get('cache-control'),
        contentLength: response.headers.get('content-length'),
      },
      base64: btoa(binary),
    }
  })()`)
  return { ...result, bytes: Buffer.from(result.base64, 'base64') }
}

// ---------------------------------------------------------------------------
// Results
// ---------------------------------------------------------------------------

const results = []
const shots = []

function record(scope, name, pass, detail = '') {
  results.push({ scope, name, pass, detail })
  console.log(`[${pass ? 'PASS' : 'FAIL'}] ${scope}: ${name}${detail ? ` — ${detail}` : ''}`)
}

function shot(file) {
  shots.push(file)
}

// ---------------------------------------------------------------------------
// One viewport, end to end
// ---------------------------------------------------------------------------

async function runViewport(label, width, height, token) {
  const isPhone = width < 500
  await cdp.send(
    'Emulation.setDeviceMetricsOverride',
    { width, height, deviceScaleFactor: isPhone ? 2 : 1, mobile: isPhone },
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
  await waitForSelector('[data-testid="room-list"]')
  record(label, 'login reaches the rooms list', true)

  if (isPhone) {
    await click('[data-testid="rooms-menu"]')
    await waitForSelector('[aria-label="Daftar room"]')
    await sleep(300)
    await click('[data-testid="new-room-sidebar"]')
  } else {
    await click('[data-testid="new-room"]')
  }
  await waitForSelector('#room-name')
  const roomName = `${isPhone ? 'Ponsel' : 'Desktop'} · file ${token}`
  await setInput('#room-name', roomName)
  await setInput('#room-topic', 'Pemeriksaan P2 dengan upload dan artifact sungguhan')
  await waitForTrue(`!document.querySelector('[data-testid="create-room"]').disabled`, 5000)
  await click('[data-testid="create-room"]')
  await waitForTrue(`location.pathname.startsWith('/r/')`, 40000, 200)
  await waitForSelector('[data-testid="empty-room"]')
  const roomPath = await evaluate(`location.pathname`)
  const roomId = roomPath.split('/').pop()
  record(label, 'room created through the UI', true, roomPath)

  // --- upload the PNG ------------------------------------------------------
  const pngPath = join(TEMP_DIR, `iris-p2-${token}.png`)
  writeFileSync(pngPath, PNG_BYTES)
  await setFileInput('[data-testid="file-input"]', pngPath)
  await waitForSelector('[data-testid="upload-chip"]', 20000)
  await waitForTrue(
    `document.querySelector('[data-testid="upload-chip"]')?.innerText.includes('siap') ?? false`,
    20000,
    100,
  )
  const chip = await evaluate(
    `document.querySelector('[data-testid="upload-chip"]')?.innerText ?? ''`,
  )
  record(
    label,
    'upload chip names the file and reaches ready',
    chip.includes(`iris-p2-${token}.png`) && /siap/.test(chip),
    JSON.stringify(chip.replace(/\s+/g, ' ').slice(0, 110)),
  )
  shot(await screenshot(`${label}-upload`))

  await setInput('[data-testid="composer-input"]', 'Ini foto uji. Balas satu kalimat singkat.')
  await waitForTrue(`!document.querySelector('[data-testid="send-button"]').disabled`, 5000)
  await click('[data-testid="send-button"]')
  await waitForTrue(`!!document.querySelector('[data-testid="working-state"]')`, 20000, 80)
  await waitForSelector('[data-testid="message-thumbnail"]', 20000)
  await waitForTrue(
    `(() => {
      const img = document.querySelector('[data-testid="message-thumbnail"] img')
      return Boolean(img && img.complete && img.naturalWidth > 0)
    })()`,
    15000,
    100,
  )
  record(label, 'sent image renders as a thumbnail', true)

  await click('[data-testid="message-thumbnail"]')
  await waitForSelector('[data-testid="image-lightbox"]')
  await waitForTrue(
    `(() => {
      const img = document.querySelector('[data-testid="image-lightbox"] img')
      return Boolean(img && img.complete && img.naturalWidth > 0)
    })()`,
    10000,
    100,
  )
  const lightbox = await evaluate(
    `(() => {
      const box = document.querySelector('[data-testid="image-lightbox"]')
      const img = box?.querySelector('img')
      return { open: Boolean(box), src: img?.getAttribute('src') ?? '', width: img?.naturalWidth ?? 0 }
    })()`,
  )
  record(
    label,
    'thumbnail opens a lightbox showing the image',
    lightbox.open && lightbox.src.startsWith('/api/files/'),
    `${lightbox.src} naturalWidth=${lightbox.width}`,
  )
  await click('[aria-label="Tutup pratinjau"]')
  await waitForTrue(`!document.querySelector('[data-testid="image-lightbox"]')`, 5000)

  const pngDownload = await readDownload(lightbox.src)
  record(
    label,
    'PNG download is inline, nosniff, and byte-identical',
    pngDownload.status === 200 &&
      pngDownload.headers.contentType === 'image/png' &&
      (pngDownload.headers.disposition ?? '').startsWith('inline') &&
      pngDownload.headers.nosniff === 'nosniff' &&
      pngDownload.headers.cacheControl === 'private, max-age=0, must-revalidate' &&
      pngDownload.bytes.equals(PNG_BYTES),
    `${pngDownload.bytes.length} bytes vs ${PNG_BYTES.length}`,
  )

  await waitForTrue(`!document.querySelector('[data-testid="working-state"]')`, 200000, 250)
  record(label, 'the image turn completed', true)
  shot(await screenshot(`${label}-image`))

  // --- ask for a real artifact --------------------------------------------
  const artifactName = `hasil-${token}.md`
  const artifactLine = `artifact-${token}`
  const prompt =
    `Tulis file markdown kecil bernama ${artifactName} di folder outbox room ini. ` +
    `Isinya tepat satu baris: ${artifactLine}. Setelah itu balas satu kalimat pendek, ` +
    `lalu akhiri dengan baris [[artifact: outbox/${artifactName}]].`

  await setInput('[data-testid="composer-input"]', prompt)
  await waitForTrue(`!document.querySelector('[data-testid="send-button"]').disabled`, 5000)
  await click('[data-testid="send-button"]')
  await waitForTrue(`!!document.querySelector('[data-testid="working-state"]')`, 20000, 80)
  await waitForTrue(`!document.querySelector('[data-testid="working-state"]')`, 240000, 250)
  await waitForSelector('[data-testid="artifact-card"]', 30000)

  const artifact = await evaluate(`(() => {
    const card = document.querySelector('[data-testid="artifact-card"]')
    const reply = [...document.querySelectorAll('[data-testid="assistant-message"]')].pop()
    return {
      href: card?.getAttribute('href') ?? '',
      name: card?.getAttribute('download') ?? '',
      text: card?.innerText ?? '',
      reply: reply?.innerText ?? '',
    }
  })()`)
  record(
    label,
    'artifact card appears under the reply',
    artifact.href.startsWith('/api/files/') && artifact.name.length > 0,
    JSON.stringify(artifact.text.replace(/\s+/g, ' ').slice(0, 110)),
  )
  record(
    label,
    'the marker is not left in the stored reply',
    !artifact.reply.includes('[[artifact:'),
    JSON.stringify(artifact.reply.replace(/\s+/g, ' ').slice(0, 100)),
  )
  shot(await screenshot(`${label}-artifact`))

  const outbox = join(DATA_DIR, 'rooms', roomId, 'outbox')
  const written = readdirSync(outbox)
    .map((name) => join(outbox, name))
    .filter((path) => statSync(path).isFile())
    .sort((a, b) => statSync(b).mtimeMs - statSync(a).mtimeMs)
  const writtenPath = written.find((path) => path.endsWith(artifact.name)) ?? written[0]
  if (!writtenPath) throw new Error(`agent wrote no file into ${outbox}`)
  const agentBytes = readFileSync(writtenPath)

  const artifactDownload = await readDownload(artifact.href)
  record(
    label,
    'artifact download is byte-identical to what the agent wrote',
    artifactDownload.status === 200 && artifactDownload.bytes.equals(agentBytes),
    `${artifactDownload.bytes.length} bytes vs ${agentBytes.length} on disk (${artifact.name})`,
  )
  record(
    label,
    'markdown download is an attachment with nosniff',
    (artifactDownload.headers.disposition ?? '').startsWith('attachment') &&
      artifactDownload.headers.nosniff === 'nosniff' &&
      artifactDownload.headers.contentType === 'text/markdown',
    `${artifactDownload.headers.contentType} · ${artifactDownload.headers.disposition}`,
  )

  // --- library -------------------------------------------------------------
  await click('[data-testid="open-files"]')
  await waitForSelector('[data-testid="file-library-list"]', 20000)
  const library = await evaluate(`(() => {
    const root = document.querySelector('[data-testid="file-library"]')
    return {
      text: root?.innerText ?? '',
      count: root?.querySelectorAll('[data-testid="library-download"]').length ?? 0,
    }
  })()`)
  record(
    label,
    'library lists both "Kamu kirim" and "Hermes kirim"',
    library.count >= 2 && /Kamu kirim/.test(library.text) && /Hermes kirim/.test(library.text),
    `${library.count} files`,
  )
  shot(await screenshot(`${label}-library`))

  await click('[data-testid="library-delete"]')
  await waitForSelector('[data-testid="delete-confirm"]')
  const twoStep = await evaluate(
    `document.querySelector('[data-testid="delete-confirm"]')?.innerText.includes('Ya, hapus') ?? false`,
  )
  record(label, 'delete asks for a second step', twoStep)
  await click('[data-testid="delete-cancel"]')
  await click('[aria-label="Tutup daftar file"]')
  await waitForTrue(`!document.querySelector('[data-testid="file-library"]')`, 5000)

  const finalMetrics = await viewportMetrics()
  record(
    label,
    'room with files has no horizontal overflow',
    finalMetrics.scrollWidth <= finalMetrics.innerWidth,
    `scrollWidth=${finalMetrics.scrollWidth} innerWidth=${finalMetrics.innerWidth}`,
  )

  return {
    roomPath,
    artifact: artifact.name,
    artifactBytes: artifactDownload.bytes.length,
    agentBytes: agentBytes.length,
  }
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

mkdirSync(SHOTS_DIR, { recursive: true })

const chrome = launchChrome()
const consoleErrors = []
const outcome = { startedAt: new Date().toISOString(), viewports: {}, checks: results }

try {
  const version = await waitForCDP()
  cdp = await connectCDP(version.webSocketDebuggerUrl)

  const { targetId } = await cdp.send('Target.createTarget', { url: 'about:blank' })
  const attached = await cdp.send('Target.attachToTarget', { targetId, flatten: true })
  sessionId = attached.sessionId

  cdp.on((message) => {
    if (message.sessionId !== sessionId) return
    if (message.method === 'Log.entryAdded' && message.params.entry.level === 'error') {
      consoleErrors.push(`log: ${message.params.entry.text}`)
    } else if (message.method === 'Runtime.exceptionThrown') {
      consoleErrors.push(
        `exception: ${message.params.exceptionDetails.exception?.description ?? message.params.exceptionDetails.text}`,
      )
    } else if (message.method === 'Runtime.consoleAPICalled' && message.params.type === 'error') {
      consoleErrors.push(message.params.args.map((a) => a.value ?? a.description ?? '').join(' '))
    }
  })

  await cdp.send('Page.enable', {}, sessionId)
  await cdp.send('Runtime.enable', {}, sessionId)
  await cdp.send('Log.enable', {}, sessionId)
  await cdp.send('Network.enable', {}, sessionId)
  await cdp.send('DOM.enable', {}, sessionId)

  const phoneToken = Math.random().toString(16).slice(2, 8)
  outcome.viewports.phone = {
    width: 390,
    height: 844,
    ...(await runViewport('phone', 390, 844, phoneToken)),
  }

  const desktopToken = Math.random().toString(16).slice(2, 8)
  outcome.viewports.desktop = {
    width: 1440,
    height: 900,
    ...(await runViewport('desktop', 1440, 900, desktopToken)),
  }

  const expected401 = consoleErrors.filter((e) => e.includes('401'))
  const unexpected = consoleErrors.filter((e) => !e.includes('401'))
  record(
    'browser',
    'no unexpected console errors',
    unexpected.length === 0,
    unexpected.length === 0
      ? `${expected401.length} expected 401 probe(s) before login`
      : unexpected.slice(0, 4).join(' | '),
  )

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
  appendFileSync(RESULTS_FILE, '\n')
  process.exitCode = 2
} finally {
  try {
    if (cdp) await cdp.send('Browser.close', undefined, undefined)
  } catch {
    /* ignore */
  }
  chrome.kill('SIGKILL')
}
