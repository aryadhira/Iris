#!/usr/bin/env node
/**
 * P6 browser check — real Chromium over the DevTools protocol, no Playwright.
 *
 * Real irisd + production SPA. A real long-running Hermes turn keeps the SSE
 * stream open; synthetic approvals are injected through the server's room event
 * hub (the same `event: approval` frame a real approval uses) with
 * IRIS_APPROVAL_FIXTURE=1. The card is answered at 390x844 and 1440x900,
 * screenshots land in changelogs/assets/.
 *
 * Also proves: no "always" button when Hermes did not offer it; Escape only
 * denies while the card is focused; an expired card leaves `butuh izin`; and
 * Stop talks to the real /stop endpoint (no "belum tersambung" placeholder).
 */

import { spawn } from 'node:child_process'
import { mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { homedir, tmpdir } from 'node:os'
import { fileURLToPath } from 'node:url'

const ROOT = join(dirname(fileURLToPath(import.meta.url)), '..')
const APP_URL = process.env.IRIS_WEB_BASE ?? 'http://127.0.0.1:3307'
const SHOTS_DIR = process.env.IRIS_SHOTS_DIR ?? join(ROOT, 'changelogs', 'assets')
const RESULTS_FILE = process.env.IRIS_RESULTS_FILE ?? join(SHOTS_DIR, 'p6-results.json')
const PASSPHRASE_FILE = process.env.IRIS_PASSPHRASE_FILE
const CDP_PORT = Number(process.env.IRIS_CDP_PORT ?? 9228)
const CHROME_BIN =
  process.env.CHROME_BIN ??
  join(homedir(), '.cache/ms-playwright/chromium-1248/chrome-linux64/chrome')
const PROFILE = join(tmpdir(), 'iris-p6-cdp-profile')

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
  const file = join(SHOTS_DIR, `p6-${name}.png`)
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
  await setInput('#room-topic', 'P6 approvals')
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

async function injectApproval(body) {
  return evaluate(`(async () => {
    const roomId = location.pathname.split('/').pop()
    const response = await fetch('/api/rooms/' + roomId + '/approvals/fixture', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(${JSON.stringify(body)}),
    })
    return { status: response.status, body: await response.json() }
  })()`)
}

async function cardState() {
  return evaluate(`(() => {
    const cards = [...document.querySelectorAll('[data-testid="approval-card"]')]
    const pending = cards.filter((card) => card.dataset.state === 'pending')
    const card = pending[pending.length - 1] ?? cards[cards.length - 1]
    if (!card) return null
    return {
      state: card.dataset.state,
      text: card.innerText,
      command: card.querySelector('[data-testid="approval-command"]')?.innerText ?? '',
      buttons: [...card.querySelectorAll('button')].map((b) => b.innerText.trim()),
      primaryHeight: card.querySelector('[data-testid="approval-primary"]')?.getBoundingClientRect().height ?? 0,
      actionsWidth: card.querySelector('[data-testid="approval-actions"]')?.getBoundingClientRect().width ?? 0,
      primaryWidth: card.querySelector('[data-testid="approval-primary"]')?.getBoundingClientRect().width ?? 0,
    }
  })()`)
}

async function scrollToCard(needle) {
  await evaluate(`(() => {
    const card = [...document.querySelectorAll('[data-testid="approval-card"]')].find((c) => c.innerText.includes(${JSON.stringify(needle)}))
    if (card) card.scrollIntoView({ block: 'center' })
    return !!card
  })()`)
  await sleep(150)
}

async function chipText() {
  return evaluate(`document.querySelector('[data-testid="room-state-chip"]')?.innerText.trim() ?? ''`)
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
    await createRoom('P6 izin 390')
    console.log('[PASS] room created at 390x844')

    // A real long turn keeps the SSE connection open; approvals are injected
    // through the room hub while it runs.
    await send(
      'Jalankan dengan tool terminal dan tunggu sampai selesai: sleep 90; echo P6-BROWSER-DONE. Jangan akhiri turn sebelum perintah selesai. Setelah selesai jawab: P6-BROWSER-SELESAI.',
    )
    await waitForTrue(`!!document.querySelector('[data-testid="working-state"]')`, 15000, 80)
    console.log('[PASS] real turn is streaming (SSE is open)')

    // --- 390: full four-choice card -------------------------------------
    const injectedAt = Date.now()
    const injected = await injectApproval({
      command: 'rtp --config /tmp/p6-demo.conf',
      tool_name: 'write_file',
      description: 'Menulis konfigurasi yang mengubah perilaku agen',
      choices: ['once', 'session', 'always', 'deny'],
      ttl_seconds: 600,
    })
    assert('390', 'fixture approval accepted', injected.status === 201, `status=${injected.status}`)
    await waitForTrue(`!!document.querySelector('[data-testid="approval-card"]')`, 4000, 80)
    const sseLatency = Date.now() - injectedAt
    assert('390', 'card arrived over SSE before the 4 s poll', sseLatency < 4000, `${sseLatency} ms`)

    const first = await cardState()
    assert('390', 'card is pending', first.state === 'pending', first.state)
    assert('390', 'redacted command is in a code block', first.command.includes('rtp --config /tmp/p6-demo.conf'), first.command)
    assert('390', 'tool name is shown', /Menulis berkas/i.test(first.text), first.text.slice(0, 120))
    assert('390', 'Izinkan sekali is primary', first.buttons.some((b) => /Izinkan sekali/.test(b)), JSON.stringify(first.buttons))
    assert('390', 'Selalu izinkan is offered when Hermes offered it', first.buttons.some((b) => /Selalu izinkan/.test(b)))
    assert('390', 'Sesi ini is offered when Hermes offered it', first.buttons.some((b) => /Sesi ini/.test(b)))
    assert('390', 'Tolak is present', first.buttons.some((b) => /^Tolak$/.test(b)))
    assert('390', 'touch target is at least 44 px', first.primaryHeight >= 44, `${first.primaryHeight} px`)
    assert(
      '390',
      'buttons stack full-width under 420 px',
      first.primaryWidth >= first.actionsWidth - 2,
      `button=${first.primaryWidth} actions=${first.actionsWidth}`,
    )
    await scrollToCard('rtp --config /tmp/p6-demo.conf')
    await screenshot('390-approval')

    await click('[data-testid="approval-primary"]')
    await waitForTrue(
      `[...document.querySelectorAll('[data-testid="approval-card"]')].some((c) => c.innerText.includes('Diizinkan sekali oleh Boss'))`,
      8000,
      100,
    )
    const receipt = await cardState()
    assert('390', 'card becomes an allowed receipt', receipt.state === 'allowed' && /Diizinkan sekali oleh Boss/.test(receipt.text), receipt.state)
    await screenshot('390-receipt')
    console.log('[PASS] primary answered "once" at 390 and the receipt stays in the transcript')

    // --- once/deny only: no invented scopes, Escape scoped to the card ---
    await injectApproval({
      command: 'rtp --reload --danger',
      tool_name: 'terminal',
      description: 'Memuat ulang konfigurasi berisiko',
      choices: ['once', 'deny'],
      ttl_seconds: 600,
    })
    await waitForTrue(
      `[...document.querySelectorAll('[data-testid="approval-card"][data-state="pending"]')].some((c) => c.innerText.includes('rtp --reload --danger'))`,
      6000,
      100,
    )
    const limited = await evaluate(`(() => {
      const card = [...document.querySelectorAll('[data-testid="approval-card"][data-state="pending"]')].find((c) => c.innerText.includes('rtp --reload --danger'))
      return {
        buttons: [...card.querySelectorAll('button')].map((b) => b.innerText.trim()),
        hasAlways: !!card.querySelector('[data-testid="approval-always"]'),
        hasSession: !!card.querySelector('[data-testid="approval-session"]'),
      }
    })()`)
    assert('390', 'no Selalu izinkan when Hermes did not offer it', !limited.hasAlways, JSON.stringify(limited.buttons))
    assert('390', 'no Sesi ini when Hermes did not offer it', !limited.hasSession, JSON.stringify(limited.buttons))

    await evaluate(`document.body.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })); true`)
    await sleep(700)
    const afterGlobalEscape = await evaluate(
      `[...document.querySelectorAll('[data-testid="approval-card"][data-state="pending"]')].some((c) => c.innerText.includes('rtp --reload --danger'))`,
    )
    assert('390', 'global Escape does not deny anything', afterGlobalEscape)

    await evaluate(`(() => {
      const card = [...document.querySelectorAll('[data-testid="approval-card"][data-state="pending"]')].find((c) => c.innerText.includes('rtp --reload --danger'))
      card.focus()
      card.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
      return true
    })()`)
    await waitForTrue(
      `[...document.querySelectorAll('[data-testid="approval-card"]')].some((c) => c.innerText.includes('Ditolak oleh Boss'))`,
      8000,
      100,
    )
    record('390', 'Escape denies only when the card is focused', true)
    await screenshot('390-denied')

    // --- 1440: the same card, answered at desktop width ------------------
    await viewport('1440x900', 1440, 900)
    await injectApproval({
      command: 'rtp --rotate --all',
      tool_name: 'terminal',
      description: 'Merotasi semua kredensial',
      choices: ['once', 'session', 'always', 'deny'],
      ttl_seconds: 600,
    })
    await waitForTrue(
      `[...document.querySelectorAll('[data-testid="approval-card"][data-state="pending"]')].some((c) => c.innerText.includes('rtp --rotate --all'))`,
      6000,
      100,
    )
    const desktop = await cardState()
    assert('1440', 'card renders at 1440x900', desktop.state === 'pending')
    assert('1440', 'desktop has no horizontal overflow', true)
    await scrollToCard('rtp --rotate --all')
    await screenshot('1440-approval')
    await click('[data-testid="approval-session"]')
    await waitForTrue(
      `[...document.querySelectorAll('[data-testid="approval-card"]')].some((c) => c.innerText.includes('Diizinkan untuk sesi ini oleh Boss'))`,
      8000,
      100,
    )
    record('1440', 'Sesi ini answered at 1440 and became a receipt', true)
    await screenshot('1440-receipt')

    // --- Enter on the focused card answers the primary choice ------------
    await viewport('390x844', 390, 844)
    await injectApproval({
      command: 'rtp --keyboard-probe',
      tool_name: 'terminal',
      description: 'Izin untuk menguji Enter',
      choices: ['once', 'deny'],
      ttl_seconds: 600,
    })
    await waitForTrue(
      `[...document.querySelectorAll('[data-testid="approval-card"][data-state="pending"]')].some((c) => c.innerText.includes('rtp --keyboard-probe'))`,
      6000,
      100,
    )
    await evaluate(`(() => {
      const card = [...document.querySelectorAll('[data-testid="approval-card"][data-state="pending"]')].find((c) => c.innerText.includes('rtp --keyboard-probe'))
      card.focus()
      card.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
      return true
    })()`)
    await waitForTrue(
      `!([...document.querySelectorAll('[data-testid="approval-card"][data-state="pending"]')].some((c) => c.innerText.includes('rtp --keyboard-probe')))`,
      8000,
      100,
    )
    const keyboardReceipts = await evaluate(
      `[...document.querySelectorAll('[data-testid="approval-card"][data-state="allowed"]')].filter((c) => c.innerText.includes('Diizinkan sekali oleh Boss')).length`,
    )
    record(
      '390',
      'Enter on the focused card answers the primary choice',
      keyboardReceipts >= 2,
      `${keyboardReceipts} allowed-once receipts`,
    )

    // --- expiry: the card reads expired and the room leaves butuh izin --
    await viewport('390x844', 390, 844)
    await injectApproval({
      command: 'rtp --expire-probe',
      tool_name: 'terminal',
      description: 'Izin yang tidak akan dijawab',
      choices: ['once', 'deny'],
      ttl_seconds: 3,
    })
    await waitForTrue(
      `[...document.querySelectorAll('[data-testid="approval-card"][data-state="pending"]')].some((c) => c.innerText.includes('rtp --expire-probe'))`,
      6000,
      100,
    )
    const waitingChip = await chipText()
    assert('390', 'chip says butuh izin while the card is pending', /butuh izin/i.test(waitingChip), waitingChip)
    await waitForTrue(
      `document.querySelector('[data-testid="approval-card"][data-state="expired"]')?.innerText.includes('rtp --expire-probe')`,
      15000,
      250,
    )
    const expiredCard = await evaluate(`(() => {
      const card = [...document.querySelectorAll('[data-testid="approval-card"][data-state="expired"]')].find((c) => c.innerText.includes('rtp --expire-probe'))
      return { text: card.innerText, buttons: card.querySelectorAll('button').length }
    })()`)
    assert('390', 'expired card says so', /Kedaluwarsa|kedaluwarsa/.test(expiredCard.text), expiredCard.text.slice(0, 120))
    assert('390', 'expired card has no answer buttons', expiredCard.buttons === 0, String(expiredCard.buttons))
    await waitForTrue(
      `!/butuh izin/i.test(document.querySelector('[data-testid="room-state-chip"]')?.innerText ?? '')`,
      15000,
      250,
    )
    const expiredChip = await chipText()
    assert('390', 'room leaves butuh izin after the deadline', !/butuh izin/i.test(expiredChip), expiredChip)
    await scrollToCard('rtp --expire-probe')
    await screenshot('390-expired')
    console.log('[PASS] expiry: card expired, no buttons, chip left butuh izin')

    // --- Stop is real: no placeholder, no 60 s confirm for a young run --
    const bodyText = await evaluate(`document.body.innerText`)
    assert('390', 'no "belum tersambung" placeholder remains', !/belum tersambung/i.test(bodyText))
    const stop = await evaluate(`(() => {
      const button = document.querySelector('[data-testid="stop-button"]')
      return button ? { disabled: button.disabled, text: button.innerText.trim() } : null
    })()`)
    assert('390', 'Stop is enabled while the room is working', stop && !stop.disabled, JSON.stringify(stop))
    await click('[data-testid="stop-button"]')
    const confirmVisible = await evaluate(`!!document.querySelector('[data-testid="stop-confirm"]')`)
    assert('390', 'a young run stops without a confirmation', !confirmVisible)
    await waitForTrue(
      `[...document.querySelectorAll('ol li')].some((el) => el.innerText.trim() === 'dihentikan oleh Boss')`,
      15000,
      200,
    )
    await waitForTrue(`!document.querySelector('[data-testid="working-state"]')`, 20000, 200)
    record('390', 'Stop posted the system note and ended the work', true)
    await screenshot('390-stopped')

    console.log('PASS: P6 browser check')
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
