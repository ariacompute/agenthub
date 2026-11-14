import { chromium } from 'playwright'
import { mkdir, unlink } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

const BASE = 'http://localhost:3500'
const EMAIL = 'admin@ariacompute.com'
const PASSWORD = 'adminA9ro!h*AT22'
const VIEWPORT = { width: 1440, height: 900 }

// route -> { num, en, zh, viewportOnly? }
// `num` is the sort order prefix (Sign_in/Registry/Task_trace/Topology/
// Namespaces/Sources/Playground/Audit_trail/Administration/Docs).
// `viewportOnly` screens are captured at the current viewport size (not full
// page) to avoid excessively tall images.
const SCREENS = [
  { route: '/login', num: 1, en: 'Sign_in', zh: '登录' },
  { route: '/registry', num: 2, en: 'Registry', zh: '注册中心', viewportOnly: true },
  { route: '/trace', num: 3, en: 'Task_trace', zh: '任务追踪' },
  { route: '/topology', num: 4, en: 'Topology', zh: '拓扑视图' },
  { route: '/registries', num: 5, en: 'Namespaces', zh: '命名空间' },
  { route: '/sources', num: 6, en: 'Sources', zh: '上游注册中心' },
  { route: '/playground', num: 7, en: 'Playground', zh: '演练场' },
  { route: '/audit', num: 8, en: 'Audit_trail', zh: '审计轨迹', viewportOnly: true },
  { route: '/admin', num: 9, en: 'Administration', zh: '管理' },
  { route: '/docs', num: 10, en: 'Docs', zh: '文档', viewportOnly: true },
]

// Build a filename like "03_Task_trace.png" / "03_任务追踪.png".
function fileName(screen, lang) {
  const name = lang === 'en' ? screen.en : screen.zh
  return `${String(screen.num).padStart(2, '0')}_${name}.png`
}

// Use an already-downloaded Chromium build (the matching 1255 download was
// blocked on a stale install lock). The headless shell works for full-page shots.
const EXECUTABLE_PATH =
  'C:\\Users\\lemon\\AppData\\Local\\ms-playwright\\chromium_headless_shell-1208\\chrome-headless-shell-win64\\chrome-headless-shell.exe'

const outDir = dirname(fileURLToPath(import.meta.url))
const enDir = join(outDir, 'en')
const zhDir = join(outDir, 'zh')

async function shoot(page, url, file, fullPage) {
  // 'load' (not networkidle) to avoid hanging on the app's long-lived SSE stream.
  await page.goto(url, { waitUntil: 'load', timeout: 30000 })
  // Wait for a stable layout element so Vue + REST data have rendered.
  await page
    .waitForSelector('main, .sidebar, .login-card', { state: 'visible', timeout: 15000 })
    .catch(() => {})
  await page.waitForTimeout(1500)
  await page.screenshot({ path: file, fullPage })
  return page.url()
}

async function main() {
  await mkdir(enDir, { recursive: true })
  await mkdir(zhDir, { recursive: true })
  const browser = await chromium.launch({ executablePath: EXECUTABLE_PATH })

  const captured = []
  const skipped = []

  // 1) Logged-out /login screenshots in a clean context.
  const loginScreen = SCREENS.find((s) => s.route === '/login')
  for (const lang of ['en', 'zh']) {
    const ctx = await browser.newContext({ viewport: VIEWPORT })
    const page = await ctx.newPage()
    const file = join(lang === 'en' ? enDir : zhDir, fileName(loginScreen, lang))
    try {
      await shoot(page, `${BASE}/login?lang=${lang}`, file)
      captured.push(`login (${lang})`)
    } catch (e) {
      skipped.push(`login (${lang}): ${e.message}`)
    }
    await ctx.close()
  }

  // 2) Log in (admin/owner) via the same-origin API so the session cookie is
  //    set reliably in this context (no form/redirect timing races).
  const authCtx = await browser.newContext({ viewport: VIEWPORT })
  const authPage = await authCtx.newPage()
  await authPage.goto(`${BASE}/login?lang=en`, { waitUntil: 'load', timeout: 30000 })
  const loginResp = await authPage.evaluate(async (creds) => {
    const r = await fetch('/api/auth/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'same-origin',
      body: JSON.stringify(creds),
    })
    const data = await r.json().catch(() => null)
    return { status: r.status, user: data && data.user }
  }, { email: EMAIL, password: PASSWORD })

  if (!loginResp.user) {
    console.error(`Login failed (HTTP ${loginResp.status}). Aborting.`)
    await authCtx.close()
    await browser.close()
    process.exit(1)
  }
  const me = loginResp.user
  console.log(`Logged in as ${me.email} (isOwner=${!!me.isOwner})`)

  // 3) Authenticated screenshots, both languages, reusing the session cookie.
  for (const screen of SCREENS) {
    if (screen.route === '/login') continue
    for (const lang of ['en', 'zh']) {
      const page = await authCtx.newPage()
      const dir = lang === 'en' ? enDir : zhDir
      const file = join(dir, fileName(screen, lang))
      try {
        const finalUrl = await shoot(page, `${BASE}${screen.route}?lang=${lang}`, file, !screen.viewportOnly)
        if (finalUrl.includes('/login')) {
          // The route guard bounced us to the login screen: not authenticated.
          await unlink(file).catch(() => {})
          skipped.push(`${screen.route} (${lang}): redirected to /login (auth missing)`)
        } else {
          captured.push(`${screen.route} (${lang})`)
        }
      } catch (e) {
        skipped.push(`${screen.route} (${lang}): ${e.message}`)
      }
      await page.close()
    }
  }

  await authCtx.close()
  await browser.close()

  console.log(`\nCaptured ${captured.length} screenshots:`)
  captured.forEach((c) => console.log('  OK  ', c))
  if (skipped.length) {
    console.log(`\nSkipped ${skipped.length}:`)
    skipped.forEach((s) => console.log('  SKIP', s))
  }
}

main().catch((e) => {
  console.error(e)
  process.exit(1)
})
