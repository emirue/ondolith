// 화면을 **실제 브라우저로 찍는다.**
//
// `make ui` 는 좌표를 재지만 숫자만 낸다 — 색이 안 맞거나 글자가 어색하거나
// 여백이 이상한 것은 좌표에 안 나온다. 여기서는 같은 브라우저로 PNG 를 남겨
// **사람이(그리고 내가) 눈으로 본다.**
//
// 브라우저와 CDP 는 ui-audit.mjs 와 같은 것을 쓴다 — 두 벌로 두면 한쪽만
// 고쳐지고, 그러면 「찍은 화면」과 「잰 화면」이 서로 다른 사이트가 된다.
//
// 실행: make shots
import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { findChrome, launch, WIDTHS } from './lib-browser.mjs'

const BASE = process.env.SHOT_BASE ?? 'http://127.0.0.1:8102'
const EMAIL = process.env.SHOT_EMAIL ?? ''
const PASSWORD = process.env.SHOT_PASSWORD ?? ''
const OUT = process.env.SHOT_DIR ?? 'shots'
const ROLE = process.env.SHOT_ROLE ?? 'anon'
// 라이트/다크. 파일 이름에 들어가므로 두 벌이 서로를 덮지 않는다.
const SCHEME = process.env.SHOT_SCHEME ?? 'light'

async function main() {
  const bin = findChrome()
  if (!bin) {
    console.error('Chromium 을 찾지 못했다 (Playwright 캐시).')
    process.exit(1)
  }
  const { cdp, sessionId } = await launch(bin)

  const go = async (url) => {
    const loaded = cdp.once('Page.loadEventFired', sessionId)
    await cdp.send('Page.navigate', { url }, sessionId)
    await Promise.race([loaded, new Promise((r) => setTimeout(r, 8000))])
  }
  const evaluate = async (expr) => {
    const { result, exceptionDetails } = await cdp.send(
      'Runtime.evaluate',
      { expression: expr, returnByValue: true, awaitPromise: true },
      sessionId,
    )
    if (exceptionDetails) throw new Error(exceptionDetails.text)
    return result.value
  }

  await cdp.send('Emulation.setEmulatedMedia',
    { features: [{ name: 'prefers-color-scheme', value: SCHEME }] }, sessionId)

  await go(BASE + '/login')
  if (EMAIL) {
    await evaluate(`fetch('/login',{method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded'},
      body:new URLSearchParams({email:${JSON.stringify(EMAIL)},password:${JSON.stringify(PASSWORD)}})}).then(r=>r.status)`)
  }

  const urls = JSON.parse(process.env.SHOT_URLS ?? '[]')
  if (!urls.length) {
    console.error('찍을 주소가 없다')
    process.exit(1)
  }
  // **여기서 지우지 않는다.** 역할 셋이 같은 디렉터리에 차례로 찍으므로,
  // 이 안에서 비우면 두 번째 역할이 첫 번째의 사진을 지운다. 지난 실행을
  // 치우는 것은 ui.sh 가 세 번 부르기 **전에** 한 번 한다.
  mkdirSync(OUT, { recursive: true })

  let n = 0
  for (const [i, width] of WIDTHS.entries()) {
    // `make ui` 와 같은 이유로 상한 창을 기다린다 (D15 4.3-2, 분당 60건).
    if (i > 0) await new Promise((r) => setTimeout(r, 61000))
    await cdp.send(
      'Emulation.setDeviceMetricsOverride',
      { width, height: 900, deviceScaleFactor: 1, mobile: width <= 480 },
      sessionId,
    )
    let since = 0
    for (const path of urls) {
      if (path.startsWith('/admin') && ++since >= 45) {
        since = 0
        await new Promise((r) => setTimeout(r, 61000))
      }
      await go(BASE + path)
      // **오류 화면을 찍고 「확인했다」고 하지 않는다.** 상한에 걸리면 본문이
      // 오류 문구뿐이라 볼 것이 없다.
      const wall = await evaluate(`document.body.textContent.includes('요청이 너무 잦습니다')`)
      if (wall) {
        console.error(`상한에 걸렸다: ${width}px ${path} — 찍기를 멈춘다`)
        process.exit(1)
      }
      const { data } = await cdp.send(
        'Page.captureScreenshot',
        { format: 'png', captureBeyondViewport: true },
        sessionId,
      )
      const slug = path.replace(/[^a-zA-Z0-9]+/g, '-').replace(/^-|-$/g, '') || 'home'
      const file = join(OUT, `${ROLE}-${SCHEME}-${width}-${slug}.png`)
      writeFileSync(file, Buffer.from(data, 'base64'))
      n++
    }
  }
  console.log(`  ✓ ${ROLE}(${SCHEME}): ${n} 장 (${OUT})`)
  process.exit(0)
}

main().catch((e) => {
  console.error(e)
  process.exit(1)
})
