// 관리자 파일 칸 끌어다 놓기 브라우저 시험 (FR-721).
//
// 실제 앱의 상품 편집(A-502)·테마 업로드(A-203)에서 파일을 놓으면 칸에 들어가고, 받을 수 없는 파일은 거절되고
// 앞서 고른 것이 남으며, 칸 밖에 놓아도 페이지가 바뀌지 않는지 본다. 상품 이미지는 놓은 채로 올려 실제로
// 저장되는지까지 본다 — 칸에 들어간 것처럼 보이기만 하고 폼에 실리지 않으면 아무 소용이 없다.
//
// **놓기는 두 가지로 친다.** CDP 의 Input.dispatchDragEvent 는 운영체제가 파일을 끌어 온 것처럼 브라우저의
// 실제 끌기를 만든다 — 사람이 하는 것과 가장 가깝다. 여러 장·거절·칸 밖 같은 갈래는 페이지 안에서
// DragEvent 를 만들어 친다.
//
// 환경: UI_BASE, UI_EMAIL, UI_PASSWORD, UI_PRODUCT(상품 id).
import { launch, findChrome } from './lib-browser.mjs'
import { mkdtempSync, writeFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

const BASE = process.env.UI_BASE
const PRODUCT = process.env.UI_PRODUCT
let failed = 0
const check = (name, ok, detail = '') => {
  console.log(`  ${ok ? '✓' : '✗'} 파일 칸: ${name}${ok ? '' : ' — ' + detail}`)
  if (!ok) failed++
}
const fail = (msg) => { console.error('  ✗ 파일 칸: ' + msg); process.exit(1) }
setTimeout(() => fail('120초 안에 끝나지 않았다'), 120000).unref()
if (!BASE || !PRODUCT) fail('UI_BASE·UI_PRODUCT 가 필요하다')

// 1×1 PNG — 서버의 내용 검사(매직 바이트)를 지나는 진짜 그림이다.
const PNG = 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=='
const dir = mkdtempSync(join(tmpdir(), 'ondolith-drop-'))
process.once('exit', () => rmSync(dir, { recursive: true, force: true }))
const pngPath = join(dir, '끌어온-사진.png')
writeFileSync(pngPath, Buffer.from(PNG, 'base64'))

const { cdp, sessionId } = await launch(findChrome())
const ev = async (expr) => {
  const { result, exceptionDetails } = await cdp.send('Runtime.evaluate',
    { expression: expr, returnByValue: true, awaitPromise: true }, sessionId)
  if (exceptionDetails) throw new Error(exceptionDetails.text + ' ' + (result?.description ?? ''))
  return result.value
}
const go = async (url) => {
  const loaded = cdp.once('Page.loadEventFired', sessionId)
  loaded.catch(() => {})
  await cdp.send('Page.navigate', { url }, sessionId)
  await Promise.race([loaded, new Promise((r) => setTimeout(r, 8000))])
}
// 페이지 안에서 놓는다. files 는 [이름, 형식] — PNG 는 진짜 그림, 나머지는 몇 바이트.
const dropIn = (selector, files) => ev(`(() => {
  const png = Uint8Array.from(atob(${JSON.stringify(PNG)}), (c) => c.charCodeAt(0))
  const dt = new DataTransfer()
  for (const [name, type] of ${JSON.stringify(files)}) dt.items.add(new File([type === 'image/png' ? png : 'x'], name, { type }))
  const e = new DragEvent('drop', { dataTransfer: dt, bubbles: true, cancelable: true })
  return { prevented: !document.querySelector(${JSON.stringify(selector)}).dispatchEvent(e) }
})()`)
const chosen = (selector) => ev(`[...document.querySelector(${JSON.stringify(selector)}).files].map((f) => f.name)`)
const note = (selector) => ev(`document.querySelector(${JSON.stringify(selector)}).closest('[data-drop-zone]').querySelector('.adm-drop-note').textContent`)

await go(BASE + '/login')
const st = await ev(`fetch('/login',{method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded'},
  body:new URLSearchParams({email:${JSON.stringify(process.env.UI_EMAIL)},password:${JSON.stringify(process.env.UI_PASSWORD)}})}).then(r=>r.status)`)
if (st !== 200) fail(`로그인 HTTP ${st}`)

// ---- 상품 편집 (A-502): 여러 장 받는 사진 칸 --------------------------------------------------------
const IMG = 'input[name=image][data-drop]'
await go(`${BASE}/admin/products/${PRODUCT}`)
check('상품 사진 칸이 놓기 영역 안에 있고 스크립트가 붙었다',
  await ev(`!!document.querySelector(${JSON.stringify(IMG)})?.closest('[data-drop-zone]')?.classList.contains('adm-drop')`))
check('칸은 숨지 않았다 — 눌러 고르기·키보드가 그대로다',
  await ev(`(() => { const i = document.querySelector(${JSON.stringify(IMG)}); const s = getComputedStyle(i);
    return s.display !== 'none' && s.visibility !== 'hidden' && !i.disabled && i.tabIndex >= 0 })()`))
const before = await ev(`document.querySelectorAll('.adm-thumbs img').length`)

// 진짜 끌기 — 영역 가운데로 끌어 와 놓는다. 「파일 선택」 단추가 아니라 영역의 다른 곳이다.
const at = await ev(`(() => { const z = document.querySelector(${JSON.stringify(IMG)}).closest('[data-drop-zone]');
  z.scrollIntoView({ block: 'center' }); const r = z.getBoundingClientRect(); return { x: r.right - 12, y: r.top + r.height / 2 } })()`)
const data = { items: [], files: [pngPath], dragOperationsMask: 1 }
for (const type of ['dragEnter', 'dragOver', 'drop']) {
  await cdp.send('Input.dispatchDragEvent', { type, x: at.x, y: at.y, data }, sessionId)
}
await new Promise((r) => setTimeout(r, 300))
const real = await chosen(IMG)
check('실제 끌기: 영역에 놓은 사진이 칸에 들어갔다', real.length === 1 && real[0] === '끌어온-사진.png', JSON.stringify(real))
check('실제 끌기: 파일 이름과 미리보기가 보인다',
  (await note(IMG)).includes('끌어온-사진.png') && await ev(`document.querySelectorAll('.adm-drop-note img').length`) === 1)

// 여러 장 칸은 여러 장을 받는다.
await dropIn(IMG, [['a.png', 'image/png'], ['b.png', 'image/png']])
const two = await chosen(IMG)
check('여러 장 칸에 두 장을 놓으면 두 장이 들어간다', two.join() === 'a.png,b.png', JSON.stringify(two))
check('미리보기 두 장', await ev(`document.querySelectorAll('.adm-drop-note img').length`) === 2)

// 받을 수 없는 파일은 거절하고, 앞서 고른 두 장은 그대로 둔다.
await dropIn(IMG, [['notes.txt', 'text/plain']])
check('받을 수 없는 파일은 거절한다', (await note(IMG)).includes('넣을 수 없는 파일'), await note(IMG))
check('거절해도 앞서 고른 것이 남는다', (await chosen(IMG)).join() === 'a.png,b.png')

// 칸 밖에 놓아도 브라우저가 파일을 열지 않는다 — 쓰던 폼이 그대로다.
const url = await ev('location.href')
const outside = await dropIn('h1, .adm-card', [['c.png', 'image/png']])
check('칸 밖에 놓으면 브라우저 기본 동작(파일 열기)을 막는다', outside.prevented === true)
check('칸 밖에 놓은 파일은 칸에 들어가지 않는다', (await chosen(IMG)).join() === 'a.png,b.png')
await new Promise((r) => setTimeout(r, 300))
check('페이지가 그대로다', (await ev('location.href')) === url)

// 놓은 채로 올린다 — 폼에 실려 서버가 저장해야 끝이다.
const loaded = cdp.once('Page.loadEventFired', sessionId)
loaded.catch(() => {})
await ev(`document.querySelector(${JSON.stringify(IMG)}).form.requestSubmit()`)
await Promise.race([loaded, new Promise((r) => setTimeout(r, 8000))])
const after = await ev(`document.querySelectorAll('.adm-thumbs img').length`)
check('놓은 두 장을 올리면 상품 사진이 두 장 늘어난다', after === before + 2, `${before} → ${after}`)

// ---- 테마 업로드 (A-203): 한 개만 받는 zip 칸 ------------------------------------------------------
const ZIP = 'input[name=theme][data-drop]'
await go(`${BASE}/admin/themes/upload`)
await dropIn(ZIP, [['first.zip', 'application/zip'], ['second.zip', 'application/zip']])
const zip = await chosen(ZIP)
check('한 개 받는 칸에 둘을 놓으면 첫 것만 들어간다', zip.join() === 'first.zip', JSON.stringify(zip))
await dropIn(ZIP, [['photo.png', 'image/png']])
check('zip 칸은 그림을 거절한다', (await note(ZIP)).includes('넣을 수 없는 파일') && (await chosen(ZIP)).join() === 'first.zip')
await dropIn(ZIP, [['Theme.ZIP', '']])
check('확장자로도 받는다 (대문자·형식 정보 없음)', (await chosen(ZIP)).join() === 'Theme.ZIP')

if (failed) fail(`${failed}건 실패`)
process.exit(0)
