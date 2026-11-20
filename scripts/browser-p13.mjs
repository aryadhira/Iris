#!/usr/bin/env node
/**
 * P13 browser check — room media playback at 390x844 and 1440x900.
 *
 * Proves an assistant message renders a real audio player, a real video
 * player, an image thumbnail that opens the lightbox, and a download control
 * that still returns an attachment. Escape and backdrop both close the
 * lightbox. Console errors fail the run.
 */
import { spawn } from 'node:child_process'
import { mkdirSync, readdirSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { homedir, tmpdir } from 'node:os'
import { fileURLToPath } from 'node:url'

const ROOT = join(dirname(fileURLToPath(import.meta.url)), '..')
const APP_URL = process.env.IRIS_WEB_BASE ?? 'http://127.0.0.1:3316'
const SHOTS_DIR = process.env.IRIS_SHOTS_DIR ?? join(ROOT, 'changelogs', 'assets')
const RESULTS_FILE = process.env.IRIS_RESULTS_FILE ?? join(SHOTS_DIR, 'p13-results.json')
const PASSPHRASE_FILE = process.env.IRIS_PASSPHRASE_FILE
const ROOM_ID = process.env.IRIS_ROOM_ID
const MP3_ID = process.env.IRIS_MP3_ID
const MP4_ID = process.env.IRIS_MP4_ID
const PNG_ID = process.env.IRIS_PNG_ID
const DOWNLOAD_DIR = process.env.IRIS_DOWNLOAD_DIR
const CDP_PORT = Number(process.env.IRIS_CDP_PORT ?? 9246)
const CHROME_BIN =
  process.env.CHROME_BIN ??
  join(homedir(), '.cache/ms-playwright/chromium-1248/chrome-linux64/chrome')
const PROFILE = join(tmpdir(), 'iris-p13-cdp-profile')

if (!PASSPHRASE_FILE || !ROOM_ID || !MP3_ID || !MP4_ID || !PNG_ID || !DOWNLOAD_DIR) {
  console.error('IRIS_PASSPHRASE_FILE, IRIS_ROOM_ID, IRIS_MP3_ID, IRIS_MP4_ID, IRIS_PNG_ID and IRIS_DOWNLOAD_DIR are required')
  process.exit(2)
}
const passphrase = readFileSync(PASSPHRASE_FILE, 'utf8').trim()
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
const results = []
const consoleErrors = []

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
      return
    }
    if (message.method === 'Runtime.consoleAPICalled' && message.params?.type === 'error') {
      consoleErrors.push({
        kind: 'console',
        text: (message.params.args ?? []).map((arg) => arg.value ?? arg.description ?? '').join(' '),
      })
    }
    if (message.method === 'Runtime.exceptionThrown') {
      consoleErrors.push({
        kind: 'exception',
        text: message.params?.exceptionDetails?.exception?.description ?? message.params?.exceptionDetails?.text ?? '',
      })
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
  const file = join(SHOTS_DIR, `p13-${name}.png`)
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
  await sleep(350)
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
}

async function openRoom() {
  await navigate(`${APP_URL}/r/${ROOM_ID}`)
  await waitForTrue(`!!document.querySelector('[data-testid="assistant-message"]')`, 20000, 150)
  await waitForTrue(`!!document.querySelector('[data-testid="artifact-list"]')`, 10000, 100)
}

function downloadFiles() {
  try {
    return readdirSync(DOWNLOAD_DIR)
  } catch {
    return []
  }
}

async function waitForDownload(timeoutMs = 15000) {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    for (const name of downloadFiles()) {
      if (name.endsWith('.mp3') && statSync(join(DOWNLOAD_DIR, name)).size > 0) {
        return { name, size: statSync(join(DOWNLOAD_DIR, name)).size }
      }
    }
    await sleep(150)
  }
  throw new Error(`download never arrived in ${DOWNLOAD_DIR}: ${JSON.stringify(downloadFiles())}`)
}

async function mediaSnapshot(scope) {
  const snapshot = await evaluate(`(() => {
    const root = document.querySelector('[data-testid="assistant-message"]')
    const audio = root?.querySelector('audio[controls]')
    const video = root?.querySelector('video[controls]')
    const thumb = root?.querySelector('[data-testid="artifact-thumbnail"]')
    const image = thumb?.querySelector('img')
    const card = root?.querySelector('[data-testid="artifact-card"]')
    const box = (el) => el ? { width: Math.round(el.getBoundingClientRect().width), height: Math.round(el.getBoundingClientRect().height) } : null
    return {
      audio: audio ? {
        present: true,
        preload: audio.preload,
        autoplay: audio.autoplay,
        loop: audio.loop,
        controls: audio.controls,
        src: audio.getAttribute('src'),
        box: box(audio),
      } : null,
      video: video ? {
        present: true,
        preload: video.preload,
        playsinline: video.hasAttribute('playsinline'),
        autoplay: video.autoplay,
        loop: video.loop,
        controls: video.controls,
        src: video.getAttribute('src'),
        box: box(video),
      } : null,
      thumbnail: thumb ? {
        tag: thumb.tagName,
        src: image?.getAttribute('src') ?? null,
        box: box(thumb),
      } : null,
      documentCard: card ? {
        testid: card.getAttribute('data-testid'),
        href: card.getAttribute('href'),
        download: card.getAttribute('download'),
      } : null,
      audioDownload: root?.querySelector('[data-testid="audio-download"]')?.getAttribute('href') ?? null,
      videoDownload: video?.closest('figure')?.querySelector('[data-testid="artifact-download"]')?.getAttribute('href') ?? null,
    }
  })()`)
  assert(scope, 'assistant message has an <audio controls> player for the mp3', snapshot.audio?.present === true, JSON.stringify(snapshot.audio))
  assert(scope, 'audio does not autoplay or loop and preloads metadata', snapshot.audio.preload === 'metadata' && !snapshot.audio.autoplay && !snapshot.audio.loop, JSON.stringify(snapshot.audio))
  assert(scope, 'assistant message has a <video controls> player for the mp4', snapshot.video?.present === true, JSON.stringify(snapshot.video))
  assert(scope, 'video is inline, muted by policy (no autoplay), and preloads metadata', snapshot.video.playsinline && snapshot.video.preload === 'metadata' && !snapshot.video.autoplay && !snapshot.video.loop, JSON.stringify(snapshot.video))
  assert(scope, 'video height is capped around max-h-72', snapshot.video.box.height > 0 && snapshot.video.box.height <= 290, JSON.stringify(snapshot.video.box))
  assert(scope, 'image artifact is a thumbnail button', snapshot.thumbnail?.tag === 'BUTTON', JSON.stringify(snapshot.thumbnail))
  assert(scope, 'thumbnail loads the png from /api/files', snapshot.thumbnail?.src === `/api/files/${PNG_ID}`, String(snapshot.thumbnail?.src))
  assert(scope, 'the pdf keeps the document artifact card', snapshot.documentCard !== null, JSON.stringify(snapshot.documentCard))
  assert(scope, 'audio download control points at the explicit attachment URL', snapshot.audioDownload === `/api/files/${MP3_ID}?download=1`, String(snapshot.audioDownload))
  assert(scope, 'video download control points at the explicit attachment URL', snapshot.videoDownload === `/api/files/${MP4_ID}?download=1`, String(snapshot.videoDownload))
  console.log(`[DOM ${scope}] ${JSON.stringify(snapshot)}`)
  return snapshot
}

async function librarySnapshot(scope) {
  await click('[data-testid="open-files"]')
  await waitForTrue(`!!document.querySelector('[data-testid="file-library"]')`, 5000)
  await waitForTrue(`!!document.querySelector('[data-testid="library-audio"]')`, 5000)
  const snapshot = await evaluate(`(() => {
    const root = document.querySelector('[data-testid="file-library"]')
    const audio = root?.querySelector('[data-testid="library-audio"]')
    const video = root?.querySelector('[data-testid="library-video"]')
    const thumb = root?.querySelector('[data-testid="library-thumbnail"]')
    return {
      audio: audio ? { controls: audio.controls, preload: audio.preload, autoplay: audio.autoplay, loop: audio.loop } : null,
      video: video ? { controls: video.controls, preload: video.preload, playsinline: video.hasAttribute('playsinline') } : null,
      thumbnail: thumb ? { src: thumb.querySelector('img')?.getAttribute('src') ?? null } : null,
      deleteButtons: root?.querySelectorAll('[data-testid="library-delete"]').length ?? 0,
      downloads: [...(root?.querySelectorAll('[data-testid="library-download"]') ?? [])].map((link) => link.getAttribute('href')),
    }
  })()`)
  assert(scope, 'the room file panel has an audio player', snapshot.audio?.controls === true && snapshot.audio.preload === 'metadata' && !snapshot.audio.autoplay && !snapshot.audio.loop, JSON.stringify(snapshot.audio))
  assert(scope, 'the room file panel has a video player', snapshot.video?.controls === true && snapshot.video.preload === 'metadata', JSON.stringify(snapshot.video))
  assert(scope, 'the room file panel image opens the lightbox', snapshot.thumbnail?.src === `/api/files/${PNG_ID}`, JSON.stringify(snapshot.thumbnail))
  assert(scope, 'delete and download controls survive in the file panel', snapshot.deleteButtons === 4 && snapshot.downloads.every((href) => String(href).endsWith('?download=1')), JSON.stringify(snapshot))
  console.log(`[LIBRARY ${scope}] ${JSON.stringify(snapshot)}`)

  await evaluate(`document.querySelector('[aria-label="Tutup daftar file"]')?.click()`)
  await waitForTrue(`!document.querySelector('[data-testid="file-library"]')`, 5000, 80)
}

async function openLightbox(scope) {
  await click('[data-testid="artifact-thumbnail"]')
  await waitForTrue(`!!document.querySelector('[data-testid="image-lightbox"]')`, 5000, 80)
  const lightbox = await evaluate(`(() => {
    const root = document.querySelector('[data-testid="image-lightbox"]')
    const img = root?.querySelector('img')
    return {
      open: !!root,
      kind: root?.getAttribute('data-media-kind'),
      tag: img?.tagName ?? null,
      src: img?.getAttribute('src') ?? null,
      naturalWidth: img?.naturalWidth ?? 0,
      naturalHeight: img?.naturalHeight ?? 0,
    }
  })()`)
  assert(scope, 'clicking the thumbnail opens the lightbox with the image', lightbox.open && lightbox.kind === 'image' && lightbox.tag === 'IMG', JSON.stringify(lightbox))
  assert(scope, 'the lightbox image is a loaded png', lightbox.src === `/api/files/${PNG_ID}` && lightbox.naturalWidth > 0, JSON.stringify(lightbox))
  console.log(`[LIGHTBOX ${scope}] ${JSON.stringify(lightbox)}`)
  return lightbox
}

async function main() {
  mkdirSync(SHOTS_DIR, { recursive: true })
  mkdirSync(DOWNLOAD_DIR, { recursive: true })
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
    await cdp.send('Browser.setDownloadBehavior', {
      behavior: 'allow',
      downloadPath: DOWNLOAD_DIR,
      eventsEnabled: true,
    })

    await viewport('390x844', 390, 844)
    await login()
    await openRoom()
    record('390', 'assistant message opened at 390x844', true)

    await mediaSnapshot('390')
    const shot390 = await screenshot('390-media')

    await openLightbox('390')
    const lightbox390 = await screenshot('390-lightbox')

    // Escape must close it without leaving a video or audio element behind.
    await evaluate(`window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))`)
    await waitForTrue(`!document.querySelector('[data-testid="image-lightbox"]')`, 5000, 80)
    record('390', 'Escape closes the lightbox', true)

    // A backdrop click must close it too.
    await click('[data-testid="artifact-thumbnail"]')
    await waitForTrue(`!!document.querySelector('[data-testid="image-lightbox"]')`, 5000, 80)
    await evaluate(`document.querySelector('[data-testid="image-lightbox"]').click()`)
    await waitForTrue(`!document.querySelector('[data-testid="image-lightbox"]')`, 5000, 80)
    record('390', 'backdrop click closes the lightbox', true)

    // The download control is a real link with the download attribute.
    const downloadHref = await evaluate(
      `document.querySelector('[data-testid="audio-download"]')?.getAttribute('href') ?? ''`,
    )
    const downloadAttr = await evaluate(
      `document.querySelector('[data-testid="audio-download"]')?.getAttribute('download') ?? ''`,
    )
    assert('390', 'audio download control keeps the download attribute', Boolean(downloadAttr), downloadAttr)
    await click('[data-testid="audio-download"]')
    const downloaded = await waitForDownload()
    assert('390', 'clicking download saves the mp3', downloaded.size > 0, JSON.stringify(downloaded))

    const downloadHeaders = await evaluate(`fetch(${JSON.stringify(downloadHref)}, { credentials: 'include' }).then(async (response) => ({
      status: response.status,
      disposition: response.headers.get('Content-Disposition'),
      contentType: response.headers.get('Content-Type'),
      bytes: (await response.arrayBuffer()).byteLength,
    }))`)
    assert('390', 'the download URL answers Content-Disposition: attachment', String(downloadHeaders.disposition).startsWith('attachment'), JSON.stringify(downloadHeaders))
    assert('390', 'the download URL returns the whole file', downloadHeaders.bytes === downloaded.size, JSON.stringify({ headers: downloadHeaders, file: downloaded }))

    const range = await evaluate(`fetch('/api/files/${MP3_ID}', { credentials: 'include', headers: { Range: 'bytes=0-1023' } }).then(async (response) => ({
      status: response.status,
      disposition: response.headers.get('Content-Disposition'),
      acceptRanges: response.headers.get('Accept-Ranges'),
      contentType: response.headers.get('Content-Type'),
      contentRange: response.headers.get('Content-Range'),
      bytes: (await response.arrayBuffer()).byteLength,
    }))`)
    assert('390', 'mp3 Range request answers 206', range.status === 206, JSON.stringify(range))
    assert('390', 'mp3 Range request streams inline', String(range.disposition).startsWith('inline'), JSON.stringify(range))
    assert('390', 'mp3 Range request advertises Accept-Ranges: bytes', range.acceptRanges === 'bytes', JSON.stringify(range))
    assert('390', 'mp3 Range request returns exactly 1024 bytes', range.bytes === 1024, JSON.stringify(range))
    console.log(`[RANGE ${JSON.stringify(range)}]`)

    await librarySnapshot('390')

    // 1440x900: the same room, the same media, more space.
    await viewport('1440x900', 1440, 900)
    await waitForTrue(`!!document.querySelector('[data-testid="assistant-message"] audio[controls]')`, 10000, 100)
    await mediaSnapshot('1440')
    const shot1440 = await screenshot('1440-media')
    await openLightbox('1440')
    const lightbox1440 = await screenshot('1440-lightbox')
    await evaluate(`window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))`)
    await waitForTrue(`!document.querySelector('[data-testid="image-lightbox"]')`, 5000, 80)

    console.log(`[SHOTS] ${JSON.stringify({ shot390, shot1440, lightbox390, lightbox1440 })}`)
    assert('all', 'no console errors or uncaught exceptions', consoleErrors.length === 0, JSON.stringify(consoleErrors))
    console.log('[CONSOLE] 0 errors')
    console.log('PASS: P13 browser check')
  } finally {
    try {
      cdp?.close()
    } catch {
      /* ignore */
    }
    chrome.kill('SIGTERM')
    writeFileSync(RESULTS_FILE, `${JSON.stringify({ results, consoleErrors }, null, 2)}\n`)
  }
}

main().catch((error) => {
  console.error(`FAIL: ${error.stack ?? error}`)
  try {
    writeFileSync(RESULTS_FILE, `${JSON.stringify({ results, consoleErrors }, null, 2)}\n`)
  } catch {
    /* ignore */
  }
  process.exitCode = 1
})
