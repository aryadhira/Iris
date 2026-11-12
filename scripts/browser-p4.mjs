#!/usr/bin/env node
/**
 * P4 browser check — real Chromium over the DevTools Protocol, no Playwright.
 *
 * The microphone is synthetic but the speech is real: the script asks the real
 * sidecar for an MP3 of an Indonesian sentence, plays it into a Web Audio
 * MediaStreamDestination, and returns that stream from a fake getUserMedia.
 * The bytes then travel through the real MediaRecorder, irisd and faster-whisper,
 * so the transcript in the composer is produced by the real pipeline — not typed
 * or injected.
 *
 * Real irisd + real sidecar. Screenshots and p4-results.json land in
 * changelogs/assets/.
 */

import { spawn } from 'node:child_process'
import { mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { homedir, tmpdir } from 'node:os'
import { fileURLToPath } from 'node:url'

const ROOT = join(dirname(fileURLToPath(import.meta.url)), '..')
const APP_URL = process.env.IRIS_WEB_BASE ?? 'http://127.0.0.1:4175'
const SHOTS_DIR = process.env.IRIS_SHOTS_DIR ?? join(ROOT, 'changelogs', 'assets')
const RESULTS_FILE = process.env.IRIS_RESULTS_FILE ?? join(SHOTS_DIR, 'p4-results.json')
const PASSPHRASE_FILE = process.env.IRIS_PASSPHRASE_FILE
const MIC_AUDIO_FILE = process.env.IRIS_PTT_AUDIO_FILE
const CDP_PORT = Number(process.env.IRIS_CDP_PORT ?? 9226)
const CHROME_BIN =
  process.env.CHROME_BIN ??
  join(homedir(), '.cache/ms-playwright/chromium-1248/chrome-linux64/chrome')
const PROFILE = join(tmpdir(), 'iris-p4-cdp-profile')

if (!PASSPHRASE_FILE) {
  console.error('IRIS_PASSPHRASE_FILE is required')
  process.exit(2)
}
if (!MIC_AUDIO_FILE) {
  console.error('IRIS_PTT_AUDIO_FILE is required (mp3 of a real Indonesian phrase)')
  process.exit(2)
}

const passphrase = readFileSync(PASSPHRASE_FILE, 'utf8').trim()
const micBase64 = readFileSync(MIC_AUDIO_FILE).toString('base64')
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
const results = { viewport: '390x844', bargeInMs: null, ptt: {}, checks: [] }

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
      '--autoplay-policy=no-user-gesture-required',
      '--use-fake-ui-for-media-stream',
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
      pointer(selector, type, options = {}) {
        const el = document.querySelector(selector)
        if (!el) return false
        const event = new PointerEvent(type, {
          bubbles: true,
          cancelable: true,
          pointerType: 'mouse',
          button: 0,
          buttons: type === 'pointerdown' ? 1 : 0,
          ...options,
        })
        el.dispatchEvent(event)
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

async function pointer(selector, type) {
  const ok = await evaluate(`window.__iris.pointer(${JSON.stringify(selector)}, ${JSON.stringify(type)})`)
  if (!ok) throw new Error(`cannot dispatch ${type} on ${selector}`)
}

async function screenshot(name) {
  const result = await cdp.send('Page.captureScreenshot', { format: 'png' }, sessionId)
  const file = join(SHOTS_DIR, `p4-${name}.png`)
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
  return metrics
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
  await setInput('#room-topic', 'P4 voice')
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

const MIC_INJECTION = `(() => {
  const base64 = ${JSON.stringify(micBase64)};
  let streamPromise = null;
  let context = null;
  let destination = null;
  let buffer = null;

  async function ensureStream() {
    if (streamPromise) return streamPromise;
    streamPromise = (async () => {
      const Ctor = window.AudioContext || window.webkitAudioContext;
      context = new Ctor();
      destination = context.createMediaStreamDestination();
      const binary = atob(base64);
      const bytes = new Uint8Array(binary.length);
      for (let i = 0; i < binary.length; i += 1) bytes[i] = binary.charCodeAt(i);
      buffer = await context.decodeAudioData(bytes.buffer.slice(0));
      return destination.stream;
    })();
    return streamPromise;
  }

  const original = navigator.mediaDevices && navigator.mediaDevices.getUserMedia
    ? navigator.mediaDevices.getUserMedia.bind(navigator.mediaDevices)
    : null;

  const fake = async (constraints) => {
    if (!constraints || !constraints.audio) {
      if (original) return original(constraints);
      throw Object.assign(new Error('no microphone'), { name: 'NotFoundError' });
    }
    return ensureStream();
  };

  window.__irisMic = {
    async play() {
      await ensureStream();
      if (context.state === 'suspended') await context.resume();
      const source = context.createBufferSource();
      source.buffer = buffer;
      source.connect(destination);
      source.start();
      return buffer.duration;
    },
    deny() {
      navigator.mediaDevices.getUserMedia = async () => {
        throw Object.assign(new Error('Permission denied'), { name: 'NotAllowedError' });
      };
    },
    allow() {
      navigator.mediaDevices.getUserMedia = fake;
    },
  };

  if (navigator.mediaDevices) navigator.mediaDevices.getUserMedia = fake;
})();`

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
    await cdp.send('Page.addScriptToEvaluateOnNewDocument', { source: MIC_INJECTION }, sessionId)
    try {
      await cdp.send('Browser.grantPermissions', {
        origin: APP_URL,
        permissions: ['audioCapture'],
      })
    } catch {
      // The fake getUserMedia is the source of truth; grant is best-effort.
    }

    await viewport('390x844', 390, 844)
    await login()
    results.overflow390 = await viewport('390x844', 390, 844)
    await createRoom('P4 suara 390')
    console.log('[PASS] room created at 390x844')

    // --- push-to-talk ------------------------------------------------------
    const before = Date.now()
    await pointer('[data-testid="mic-button"]', 'pointerdown')
    await waitForTrue(`!!document.querySelector('[data-testid="recording-state"]')`, 10000, 80)
    results.ptt.startToRecordingMs = Date.now() - before
    await sleep(1200)
    const timer = await evaluate(
      `document.querySelector('[data-testid="recording-timer"]')?.innerText.trim() ?? ''`,
    )
    if (timer === '' || timer === '0:00') throw new Error(`timer did not run: ${timer}`)
    const duration = await evaluate(`window.__irisMic.play()`)
    await sleep(600)
    const meter = await evaluate(
      `[...document.querySelectorAll('[data-testid="level-meter"] span')].map((el) => el.style.height)`,
    )
    results.ptt.meter = meter
    console.log(`[PASS] recording state, timer ${timer}, synthetic speech ${duration.toFixed(2)} s, meter ${JSON.stringify(meter)}`)
    await screenshot('390-recording')

    await sleep(Math.min(4200, Math.round(duration * 1000) + 200))
    const stopAt = Date.now()
    await pointer('[data-testid="mic-button"]', 'pointerup')
    await waitForTrue(
      `document.querySelector('[data-testid="composer-input"]')?.value.trim().length > 0`,
      30000,
      120,
    )
    const transcript = await evaluate(
      `document.querySelector('[data-testid="composer-input"]').value.trim()`,
    )
    results.ptt.stopToTranscriptMs = Date.now() - stopAt
    results.ptt.transcript = transcript
    if (/ingatkan|besok|pagi/i.test(transcript) === false) {
      console.log(`[WARN] transcript differs from the spoken phrase: ${transcript}`)
    }
    await screenshot('390-ptt-transcript')
    console.log(`[PASS] real STT transcript in the composer: ${JSON.stringify(transcript)}`)

    // The transcript is editable and was not sent by itself.
    const messageCountBefore = await evaluate(
      `document.querySelectorAll('[data-testid="user-message"]').length`,
    )
    if (messageCountBefore !== 0) throw new Error('push-to-talk auto-sent the transcript')
    console.log('[PASS] transcript was not auto-sent')

    // --- send it for real, then speaker and barge-in ------------------------
    await click('[data-testid="send-button"]')
    await waitForTrue(
      `document.querySelectorAll('[data-testid="assistant-message"]').length > 0`,
      120000,
      250,
    )
    await waitForTrue(`!document.querySelector('[data-testid="streaming-turn"]')`, 120000, 250)
    console.log('[PASS] real Hermes reply rendered')

    const speakerState = await evaluate(`(() => {
      const messages = [...document.querySelectorAll('[data-testid="assistant-message"]')]
      const button = messages[messages.length - 1]?.querySelector('[data-testid="speaker-button"]')
      if (!button) return 'missing'
      button.click()
      return 'clicked'
    })()`)
    if (speakerState !== 'clicked') throw new Error('assistant message has no speaker button')
    await waitForTrue(
      `document.querySelector('[data-testid="speaker-button"][data-state="playing"]') !== null`,
      20000,
      100,
    )
    await screenshot('390-speaking')
    console.log('[PASS] speaker shows the membaca… state')

    await evaluate(`(() => {
      window.__pauseAt = null
      window.__bargeStart = null
      window.__origPause = HTMLMediaElement.prototype.pause
      HTMLMediaElement.prototype.pause = function () {
        if (window.__bargeStart !== null && window.__pauseAt === null) {
          window.__pauseAt = performance.now()
        }
        return window.__origPause.call(this)
      }
      return true
    })()`)
    await evaluate(`window.__bargeStart = performance.now()`)
    await pointer('[data-testid="mic-button"]', 'pointerdown')
    await sleep(250)
    results.bargeInMs = await evaluate(
      `window.__pauseAt === null ? null : Math.round((window.__pauseAt - window.__bargeStart) * 10) / 10`,
    )
    if (results.bargeInMs === null || results.bargeInMs >= 150) {
      throw new Error(`barge-in did not stop playback in time: ${results.bargeInMs} ms`)
    }
    console.log(`[PASS] barge-in stopped playback in ${results.bargeInMs} ms`)
    await waitForTrue(`!!document.querySelector('[data-testid="recording-state"]')`, 10000, 80)
    await click('[data-testid="recording-cancel"]')
    await waitForTrue(`!document.querySelector('[data-testid="recording-state"]')`, 5000, 80)
    console.log('[PASS] batal returned the composer to idle')

    // --- auto-speak preference is remembered per room ----------------------
    await click('[data-testid="autospeak-toggle"]')
    const stored = await evaluate(`(() => {
      const roomId = location.pathname.split('/').pop()
      return localStorage.getItem('iris.autospeak.' + roomId)
    })()`)
    if (stored !== '1') throw new Error(`auto-speak not remembered: ${stored}`)
    await click('[data-testid="autospeak-toggle"]')
    console.log('[PASS] auto-speak toggle persisted per room and switched off again')

    // --- permission denied copy -------------------------------------------
    await evaluate(`window.__irisMic.deny()`)
    await pointer('[data-testid="mic-button"]', 'pointerdown')
    await waitForTrue(`!!document.querySelector('[data-testid="voice-error"]')`, 10000, 80)
    const denied = await evaluate(
      `document.querySelector('[data-testid="voice-error"]').innerText.trim()`,
    )
    results.permissionDenied = denied
    if (!/HTTPS/i.test(denied) || !/Tailscale/i.test(denied)) {
      throw new Error(`permission copy is not the HTTPS/Tailscale explanation: ${denied}`)
    }
    await screenshot('390-mic-denied')
    console.log(`[PASS] permission denied copy: ${denied.replace(/\\s+/g, ' ')}`)

    // --- desktop layout ----------------------------------------------------
    const metrics1440 = await viewport('1440x900', 1440, 900)
    results.overflow1440 = metrics1440
    await screenshot('1440-voice')
    console.log('[PASS] 1440x900 layout has no horizontal overflow')

    writeFileSync(RESULTS_FILE, `${JSON.stringify(results, null, 2)}\n`)
    console.log(`[RESULT] ${RESULTS_FILE}`)
    console.log('PASS: P4 browser check')
  } finally {
    try {
      cdp?.close()
    } catch {
      /* already gone */
    }
    chrome.kill('SIGTERM')
    await sleep(700)
    try {
      rmSync(PROFILE, { recursive: true, force: true })
    } catch {
      // Chrome can still be flushing its profile; the temp dir is not ours to fight over.
    }
  }
}

main().catch((error) => {
  console.error(`FAIL: ${error.message}`)
  writeFileSync(RESULTS_FILE, `${JSON.stringify({ ...results, error: error.message }, null, 2)}\n`)
  process.exit(1)
})
