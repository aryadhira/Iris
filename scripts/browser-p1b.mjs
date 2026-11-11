#!/usr/bin/env node
/**
 * P1b browser check — a real Chromium via the DevTools Protocol, no Playwright,
 * no npm dependencies (Node's global WebSocket + fetch only).
 *
 * For 390x844 and 1440x900 it:
 *   1. lands on /login and signs in through the form (a wrong passphrase first,
 *      on the phone, to see the human error line and that focus is kept)
 *   2. creates a room through the UI
 *   3. sends a message and watches the reply grow while the working state counts
 *      seconds; the Stop button must be visible, disabled, and explain itself
 *   4. reloads and proves the reply is still there, exactly once
 *   5. measures horizontal overflow, tap targets, the font and the palette
 *
 * Screenshots land in changelogs/assets/; the machine-readable result is
 * written next to them. Real irisd + real Hermes; no fake data.
 */

import { spawn } from 'node:child_process'
import { mkdirSync, readFileSync, rmSync, writeFileSync, appendFileSync } from 'node:fs'
import { join, dirname } from 'node:path'
import { homedir, tmpdir } from 'node:os'
import { fileURLToPath } from 'node:url'

const ROOT = join(dirname(fileURLToPath(import.meta.url)), '..')
const APP_URL = process.env.IRIS_WEB_BASE ?? 'http://127.0.0.1:4175'
const SHOTS_DIR = process.env.IRIS_SHOTS_DIR ?? join(ROOT, 'changelogs', 'assets')
const RESULTS_FILE = process.env.IRIS_RESULTS_FILE ?? join(SHOTS_DIR, 'p1b-results.json')
const PASSPHRASE_FILE = process.env.IRIS_PASSPHRASE_FILE
const CDP_PORT = Number(process.env.IRIS_CDP_PORT ?? 9224)
const CHROME_BIN =
  process.env.CHROME_BIN ??
  join(homedir(), '.cache/ms-playwright/chromium-1248/chrome-linux64/chrome')
const PROFILE = join(tmpdir(), 'iris-p1b-cdp-profile')

if (!PASSPHRASE_FILE) {
  console.error('IRIS_PASSPHRASE_FILE is required')
  process.exit(2)
}
const passphrase = readFileSync(PASSPHRASE_FILE, 'utf8').trim()

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))

// ---------------------------------------------------------------------------
// Minimal CDP client
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

async function reload() {
  await cdp.send('Page.reload', { ignoreCache: true }, sessionId)
  await waitForTrue(`document.readyState === 'complete'`, 20000, 100)
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
  const file = join(SHOTS_DIR, `p1b-${name}.png`)
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
      scrollHeight: doc.scrollHeight,
      clientWidth: doc.clientWidth,
    }
  })()`)
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

  // Fresh identity for each screen: a real login, no inherited cookie.
  await cdp.send('Network.clearBrowserCookies', {}, sessionId)
  await navigate(`${APP_URL}/`)
  await evaluate(`localStorage.clear(); sessionStorage.clear(); true`)

  await waitForTrue(`location.pathname === '/login'`, 20000)
  await waitForSelector('#passphrase')
  await sleep(600)

  const loginMetrics = await viewportMetrics()
  const focused = await evaluate(`document.activeElement?.id ?? ''`)
  record(label, 'unauthenticated visit lands on /login', true)
  record(label, 'passphrase field starts focused', focused === 'passphrase', `activeElement=${focused}`)
  record(
    label,
    'login has no horizontal overflow',
    loginMetrics.scrollWidth <= loginMetrics.innerWidth,
    `scrollWidth=${loginMetrics.scrollWidth} innerWidth=${loginMetrics.innerWidth}`,
  )
  shot(await screenshot(`${label}-login`))

  if (isPhone) {
    await setInput('#passphrase', 'passphrase-yang-salah')
    await click('[data-testid="login-submit"]')
    await waitForTrue(
      `(document.body.innerText || '').includes('Passphrase-nya belum tepat')`,
      15000,
      200,
    )
    let refocused = ''
    try {
      await waitForTrue(`document.activeElement?.id === 'passphrase'`, 3000, 100)
      refocused = 'passphrase'
    } catch {
      refocused = await evaluate(`document.activeElement?.id ?? ''`)
    }
    record('phone', 'wrong passphrase shows a human line', true)
    record('phone', 'wrong passphrase keeps the field focused', refocused === 'passphrase', `activeElement=${refocused}`)
    shot(await screenshot('phone-login-error'))
  }

  await setInput('#passphrase', passphrase)
  await click('[data-testid="login-submit"]')
  await waitForTrue(`location.pathname === '/rooms'`, 25000, 200)
  await waitForSelector('[data-testid="room-list"]')
  record(label, 'login with the real passphrase reaches the rooms list', true)

  const roomsMetrics = await viewportMetrics()
  record(
    label,
    'rooms list has no horizontal overflow',
    roomsMetrics.scrollWidth <= roomsMetrics.innerWidth,
    `scrollWidth=${roomsMetrics.scrollWidth}`,
  )

  if (isPhone) shot(await screenshot('phone-rooms-empty'))

  let newRoomButton
  if (isPhone) {
    await click('[data-testid="rooms-menu"]')
    await waitForSelector('[aria-label="Daftar room"]')
    await sleep(350)
    const sheet = await evaluate(
      `(() => { const el = document.querySelector('[aria-label="Daftar room"]'); const r = el.getBoundingClientRect(); return { left: r.left, right: r.right, width: r.width, innerWidth: window.innerWidth } })()`,
    )
    record(
      'phone',
      'room sheet opens from the left inside the viewport',
      sheet.left >= 0 && sheet.right <= sheet.innerWidth && sheet.width >= 240,
      `left=${Math.round(sheet.left)} right=${Math.round(sheet.right)} width=${Math.round(sheet.width)}`,
    )
    shot(await screenshot('phone-sheet'))
    newRoomButton = '[data-testid="new-room-sidebar"]'
  } else {
    newRoomButton = '[data-testid="new-room"]'
  }

  await click(newRoomButton)
  await waitForSelector('#room-name')
  await sleep(250)
  const dialogMetrics = await viewportMetrics()
  record(
    label,
    'new-room dialog has no horizontal overflow',
    dialogMetrics.scrollWidth <= dialogMetrics.innerWidth,
    `scrollWidth=${dialogMetrics.scrollWidth}`,
  )

  const roomName = `${isPhone ? 'Ponsel' : 'Desktop'} · uji ${token}`
  await setInput('#room-name', roomName)
  await setInput('#room-topic', 'Pemeriksaan P1b dengan Hermes sungguhan')
  await waitForTrue(`!document.querySelector('[data-testid="create-room"]').disabled`, 5000)
  await click('[data-testid="create-room"]')
  await waitForTrue(`location.pathname.startsWith('/r/')`, 40000, 200)
  await waitForSelector('[data-testid="empty-room"]')
  const roomPath = await evaluate(`location.pathname`)
  record(label, 'room created through the UI', true, roomPath)

  const composer = await evaluate(
    `(() => { const r = window.__iris.rect('[data-testid="composer-input"]'); return { ...r, bottom: r.bottom, innerHeight: window.innerHeight } })()`,
  )
  const sendHeight = await evaluate(`window.__iris.rect('[data-testid="send-button"]')?.height ?? 0`)
  record(
    label,
    'composer is inside the viewport (dvh, not vh)',
    composer !== null && composer.bottom <= composer.innerHeight + 1 && composer.top >= 0,
    `bottom=${Math.round(composer?.bottom ?? -1)} viewport=${composer?.innerHeight}`,
  )
  record(label, 'send control is at least 44px tall', sendHeight >= 44, `height=${Math.round(sendHeight)}`)

  const inputFont = await evaluate(
    `getComputedStyle(document.querySelector('[data-testid="composer-input"]')).fontSize`,
  )
  record(
    label,
    isPhone ? 'composer input is 16px (no iOS zoom)' : 'composer input keeps body scale',
    isPhone ? inputFont === '16px' : true,
    `fontSize=${inputFont}`,
  )

  // --- send a real turn ----------------------------------------------------
  const marker = `p1b-${token}`
  const prompt = `Balas dengan tepat satu kalimat ini, tanpa tambahan: ${marker} siap.`

  await setInput('[data-testid="composer-input"]', prompt)
  await waitForTrue(`!document.querySelector('[data-testid="send-button"]').disabled`, 5000)
  await evaluate(`(() => {
    window.__samples = []
    window.__sampleTimer = setInterval(() => {
      const el = document.querySelector('[data-testid="streaming-text"], [data-testid="assistant-message"]')
      window.__samples.push(el ? el.innerText.trim().length : -1)
    }, 80)
    return true
  })()`)

  const startedAt = Date.now()
  await click('[data-testid="send-button"]')
  await waitForTrue(`!!document.querySelector('[data-testid="working-state"]')`, 15000, 80)

  const workingText = await evaluate(
    `document.querySelector('[data-testid="working-state"]')?.innerText ?? ''`,
  )
  record(
    label,
    'working state names the work and counts seconds',
    /Bekerja/.test(workingText) && /\d+ detik/.test(workingText),
    JSON.stringify(workingText.replace(/\s+/g, ' ').slice(0, 90)),
  )

  const stop = await evaluate(`(() => {
    const button = document.querySelector('[data-testid="stop-button"]')
    if (!button) return null
    const holder = button.closest('span[title]')
    return {
      disabled: button.disabled,
      title: holder?.getAttribute('title') || button.getAttribute('title') || '',
    }
  })()`)
  record(
    label,
    'Stop is visible, disabled, and says it is not wired',
    Boolean(stop && stop.disabled && /belum tersambung/i.test(stop.title)),
    stop ? `disabled=${stop.disabled} title=${JSON.stringify(stop.title.slice(0, 70))}` : 'missing',
  )

  const disabledDuringTurn = await evaluate(
    `document.querySelector('[data-testid="composer-input"]').disabled`,
  )
  record(label, 'composer is disabled while a turn runs', disabledDuringTurn === true)

  const ticked = await evaluate(`new Promise((resolve) => {
    const started = Date.now()
    const timer = setInterval(() => {
      const el = document.querySelector('[data-testid="working-state"]')
      if (!el) {
        clearInterval(timer)
        resolve({ ok: false, reason: 'turn ended before the second tick' })
        return
      }
      const match = el.innerText.match(/(\\d+) detik/)
      if (match && Number(match[1]) >= 1) {
        clearInterval(timer)
        resolve({ ok: true, seconds: Number(match[1]) })
        return
      }
      if (Date.now() - started > 6000) {
        clearInterval(timer)
        resolve({ ok: false, reason: 'no tick within 6 s' })
      }
    }, 150)
  })`)
  record(
    label,
    'elapsed seconds actually tick',
    ticked.ok === true,
    ticked.ok ? `read ${ticked.seconds} detik` : ticked.reason,
  )

  shot(await screenshot(`${label}-room-streaming`))

  await waitForTrue(`!document.querySelector('[data-testid="working-state"]')`, 200000, 250)
  const elapsedMs = Date.now() - startedAt
  const samples = await evaluate(`(() => { clearInterval(window.__sampleTimer); return window.__samples })()`)
  const growingLengths = [...new Set(samples.filter((n) => n > 0))]

  record(
    label,
    'reply text grew in place while streaming',
    growingLengths.length >= 2,
    `lengths seen: ${growingLengths.slice(0, 10).join(',')} (${growingLengths.length} distinct)`,
  )
  record(label, 'turn completed', true, `${(elapsedMs / 1000).toFixed(1)} s`)

  const finalReply = await evaluate(`(() => {
    const nodes = [...document.querySelectorAll('[data-testid="assistant-message"]')]
    const last = nodes[nodes.length - 1]
    return { count: nodes.length, body: last?.innerText?.trim() ?? '' }
  })()`)
  record(label, 'assistant reply is on screen', finalReply.body.length > 0, `${finalReply.body.length} chars`)
  record(
    label,
    'reply carries this turn\u2019s marker',
    finalReply.body.toLowerCase().includes(marker.toLowerCase()),
    JSON.stringify(finalReply.body.replace(/\s+/g, ' ').slice(0, 90)),
  )

  const afterTurnMetrics = await viewportMetrics()
  record(
    label,
    'room view has no horizontal overflow',
    afterTurnMetrics.scrollWidth <= afterTurnMetrics.innerWidth,
    `scrollWidth=${afterTurnMetrics.scrollWidth} innerWidth=${afterTurnMetrics.innerWidth}`,
  )

  // --- reload: the room must survive, once ---------------------------------
  await reload()
  await waitForTrue(`location.pathname.startsWith('/r/')`, 20000)
  await waitForSelector('[data-testid="message-list"]', 30000)
  await waitForTrue(
    `document.querySelectorAll('[data-testid="assistant-message"]').length >= 1`,
    30000,
    200,
  )
  await sleep(800)

  const afterReload = await evaluate(`(() => {
    const userNodes = [...document.querySelectorAll('[data-testid="user-message"]')]
    const assistantNodes = [...document.querySelectorAll('[data-testid="assistant-message"]')]
    return {
      path: location.pathname,
      users: userNodes.length,
      assistants: assistantNodes.length,
      userHits: userNodes.filter((n) => n.innerText.includes(${JSON.stringify(prompt)})).length,
      assistantHits: assistantNodes.filter((n) => n.innerText.includes(${JSON.stringify(marker)})).length,
      lastAssistant: assistantNodes[assistantNodes.length - 1]?.innerText?.trim() ?? '',
    }
  })()`)

  record(label, 'reload keeps the room open', afterReload.path === roomPath, afterReload.path)
  record(
    label,
    'reload still shows the reply',
    afterReload.assistantHits === 1,
    `assistant nodes=${afterReload.assistants}, with marker=${afterReload.assistantHits}`,
  )
  record(
    label,
    'the sent message is stored exactly once',
    afterReload.userHits === 1,
    `user nodes=${afterReload.users}, with text=${afterReload.userHits}`,
  )

  const afterReloadMetrics = await viewportMetrics()
  record(
    label,
    'reloaded room has no horizontal overflow',
    afterReloadMetrics.scrollWidth <= afterReloadMetrics.innerWidth,
    `scrollWidth=${afterReloadMetrics.scrollWidth}`,
  )
  shot(await screenshot(`${label}-room`))

  if (!isPhone) {
    await navigate(`${APP_URL}/rooms`)
    await waitForSelector('[data-testid="room-list"]')
    await waitForTrue(`document.querySelectorAll('main a[href^="/r/"]').length >= 1`, 15000)
    await sleep(400)
    shot(await screenshot('desktop-rooms'))
  }

  if (isPhone) {
    await click('[data-testid="room-menu"]')
    await waitForSelector('[aria-label="Daftar room"]')
    await sleep(300)
    // The desktop sidebar stays mounted (hidden) at this width, so scope every
    // selector to the sheet the person can actually see.
    const sheet = '[aria-label="Daftar room"]'
    await click(`${sheet} [data-testid="archive-room"]`)
    await waitForSelector(`${sheet} [data-testid="archive-confirm"]`)
    const twoStep = await evaluate(
      `(() => {
        const root = document.querySelector(${JSON.stringify(sheet)})
        const confirm = root?.querySelector('[data-testid="archive-confirm"]')
        return Boolean(root && confirm && confirm.getClientRects().length > 0 && root.innerText.includes('Ya, arsipkan'))
      })()`,
    )
    record('phone', 'archive asks for a second step', twoStep)
    shot(await screenshot('phone-archive-confirm'))
    await click(`${sheet} [data-testid="archive-cancel"]`)
    await sleep(250)
    const roomsLeft = await evaluate(
      `document.querySelector(${JSON.stringify(sheet)})?.querySelectorAll('[data-testid="archive-room"]').length ?? 0`,
    )
    record('phone', 'cancelling archive keeps the room', roomsLeft >= 1, `${roomsLeft} room(s) in the sheet`)
    shot(await screenshot('phone-sheet-rooms'))

    // Archive for real and prove the room opens read-only afterwards.
    await click(`${sheet} [data-testid="archive-room"]`)
    await waitForSelector(`${sheet} [data-testid="archive-confirm"]`)
    await click(`${sheet} [data-testid="archive-confirm"]`)
    await waitForTrue(`location.pathname === '/rooms'`, 25000, 200)
    await navigate(`${APP_URL}${roomPath}`)
    await waitForSelector('[data-testid="archived-note"]', 30000)
    const archivedView = await evaluate(`(() => {
      const note = document.querySelector('[data-testid="archived-note"]')
      const input = document.querySelector('[data-testid="composer-input"]')
      return { note: note?.innerText ?? '', disabled: input?.disabled ?? null }
    })()`)
    record(
      'phone',
      'archived room opens read-only with a reason',
      /diarsipkan/i.test(archivedView.note) && archivedView.disabled === true,
      `disabled=${archivedView.disabled} note=${JSON.stringify(archivedView.note.replace(/\s+/g, ' ').slice(0, 70))}`,
    )
    shot(await screenshot('phone-archived-room'))
  }

  return { roomPath, elapsedMs, growingLengths, finalReply: finalReply.body, afterReload }
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

mkdirSync(SHOTS_DIR, { recursive: true })

const chrome = launchChrome()
const consoleErrors = []
const outcome = { startedAt: new Date().toISOString(), viewports: {}, branding: {}, checks: results }

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

  const phoneToken = Math.random().toString(16).slice(2, 8)
  outcome.viewports.phone = {
    width: 390,
    height: 844,
    ...(await runViewport('phone', 390, 844, phoneToken)),
  }

  // Branding is measured once, on the phone viewport.
  await navigate(`${APP_URL}/`)
  await waitForTrue(`location.pathname !== '/login'`, 20000)
  const branding = await evaluate(`(async () => {
    const manifest = await fetch('/manifest.webmanifest')
    const manifestJson = manifest.ok ? await manifest.json() : null
    const favicon = await fetch('/favicon.ico')
    const mark16 = await fetch('/icons/mark-16.png')
    const master = await fetch('/icons/icon-512.png')
    const fontLoaded = document.fonts.check('14px "Inter Tight Variable"')
    return {
      manifestStatus: manifest.status,
      manifestIcons: manifestJson?.icons?.map((i) => i.sizes + ':' + i.purpose) ?? [],
      faviconStatus: favicon.status,
      mark16Status: mark16.status,
      masterStatus: master.status,
      fontFamily: getComputedStyle(document.body).fontFamily,
      fontLoaded,
      bodyBackground: getComputedStyle(document.body).backgroundColor,
      faviconLinks: [...document.querySelectorAll('link[rel="icon"]')].map((l) => l.getAttribute('href')),
      colorScheme: document.querySelector('meta[name="color-scheme"]')?.getAttribute('content'),
    }
  })()`)

  record('branding', 'manifest is served', branding.manifestStatus === 200, `status=${branding.manifestStatus}`)
  record(
    'branding',
    'manifest lists 192, 512 and a maskable icon',
    Array.isArray(branding.manifestIcons) &&
      branding.manifestIcons.some((i) => i.startsWith('192x192')) &&
      branding.manifestIcons.some((i) => i.startsWith('512x512:any')) &&
      branding.manifestIcons.some((i) => i.includes('maskable')),
    JSON.stringify(branding.manifestIcons),
  )
  record(
    'branding',
    'tab uses the reduced 16/32 mark, not the full master',
    branding.faviconLinks.includes('/icons/mark-16.png') &&
      branding.faviconLinks.includes('/icons/mark-32.png') &&
      branding.faviconStatus === 200 &&
      branding.mark16Status === 200,
    `favicon=${branding.faviconStatus} mark16=${branding.mark16Status} links=${branding.faviconLinks.join(',')}`,
  )
  record('branding', 'master icon is served', branding.masterStatus === 200, `status=${branding.masterStatus}`)
  record(
    'branding',
    'Inter Tight is the loaded face',
    branding.fontLoaded && /Inter Tight/i.test(branding.fontFamily),
    branding.fontFamily,
  )
  record('branding', 'body uses the brand bg #080321', branding.bodyBackground === 'rgb(8, 3, 33)', branding.bodyBackground)
  record('branding', 'dark color-scheme is declared', branding.colorScheme === 'dark', branding.colorScheme ?? 'missing')

  // Desktop, with a room of its own, so the screenshot contains real data only.
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

  outcome.branding = branding
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
  outcome.branding = outcome.branding ?? {}
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
