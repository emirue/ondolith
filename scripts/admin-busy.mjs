// 관리자 단추 두 번 누르기 방지 브라우저 시험 (FR-727).
//
// 서버가 느린 순간을 만든다: CDP Fetch 로 POST 를 붙잡아 두고 그동안 단추를 두 번 누른다. 브라우저는 앞 제출을
// 끊고 뒤 제출을 다시 보내므로, 막지 않으면 붙잡힌 POST 가 둘이 된다 — 운영자가 백업 내려받기를 기다리다 한 번 더
// 누르면 앞 요청이 끊기고 뒤 요청은 「다른 백업이 진행 중」에 걸리던 자리다. 보는 것:
//   - 두 번 눌러도 POST 는 하나이고, 기다리는 동안 단추가 「처리 중」을 말한다
//   - 보낸 값은 그대로 도착한다(저장 알림 문구가 그것을 말한다)
//   - 내려받기는 화면이 넘어가지 않는다 — 파일이 오기 시작하면 단추가 다시 살아나고, 또 받을 수 있다
//   - 확인 창을 취소하면 잠기지 않는다
//   - 뒤로 가기(bfcache)로 돌아온 화면의 단추는 살아 있다
//
// 환경: UI_BASE, UI_EMAIL, UI_PASSWORD, UI_MEMBER(회원 id). /admin/backup 에 저장된 자동 백업이 하나 있어야 한다.
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { launch, findChrome } from './lib-browser.mjs'

const BASE = process.env.UI_BASE
const MEMBER = process.env.UI_MEMBER
let failed = 0
const check = (name, ok, detail = '') => {
  console.log(`  ${ok ? '✓' : '✗'} 두 번 누르기: ${name}${ok ? '' : ' — ' + detail}`)
  if (!ok) failed++
}
const fail = (msg) => { console.error('  ✗ 두 번 누르기: ' + msg); process.exit(1) }
setTimeout(() => fail('120초 안에 끝나지 않았다'), 120000).unref()
if (!BASE || !MEMBER) fail('UI_BASE·UI_MEMBER 가 필요하다')

const downloads = mkdtempSync(join(tmpdir(), 'ondolith-busy-'))
process.once('exit', () => rmSync(downloads, { recursive: true, force: true }))

const { cdp, sessionId } = await launch(findChrome())
const wait = (ms) => new Promise((r) => setTimeout(r, ms))
const ev = async (expr) => {
  const { result, exceptionDetails } = await cdp.send('Runtime.evaluate',
    { expression: expr, returnByValue: true, awaitPromise: true }, sessionId)
  if (exceptionDetails) throw new Error(exceptionDetails.text + ' ' + (result?.description ?? ''))
  return result.value
}
const loaded = () => {
  const p = cdp.once('Page.loadEventFired', sessionId)
  p.catch(() => {})
  return Promise.race([p, wait(8000)])
}
const go = async (url) => {
  const done = loaded()
  await cdp.send('Page.navigate', { url }, sessionId)
  await done
}
const until = async (what, ms = 3000) => {
  for (const end = Date.now() + ms; Date.now() < end; await wait(100)) if (await ev(what)) return true
  return false
}

// ---- 붙잡기: /admin/ 아래 POST 는 놓아 줄 때까지 서버에 가지 않는다 ------------------------------------
let held = []
let posts = 0
let started = 0
cdp.listeners.push((msg) => {
  if (msg.method === 'Browser.downloadWillBegin') started++
  if (msg.method !== 'Fetch.requestPaused' || msg.sessionId !== sessionId) return
  const { requestId, request } = msg.params
  if (request.method === 'POST') {
    posts++
    held.push(requestId)
  } else {
    cdp.send('Fetch.continueRequest', { requestId }, sessionId).catch(() => {})
  }
})
const release = async () => {
  const ids = held
  held = []
  // 브라우저가 이미 끊은 요청은 놓아 줄 수 없다 — 그 실패는 결과가 아니다.
  for (const requestId of ids) await cdp.send('Fetch.continueRequest', { requestId }, sessionId).catch(() => {})
}
await cdp.send('Browser.setDownloadBehavior', { behavior: 'allowAndName', downloadPath: downloads, eventsEnabled: true })

await go(BASE + '/login')
const st = await ev(`fetch('/login',{method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded'},
  body:new URLSearchParams({email:${JSON.stringify(process.env.UI_EMAIL)},password:${JSON.stringify(process.env.UI_PASSWORD)}})}).then(r=>r.status)`)
if (st !== 200) fail(`로그인 HTTP ${st}`)
await cdp.send('Fetch.enable', { patterns: [{ urlPattern: `${BASE}/admin/*`, requestStage: 'Request' }] }, sessionId)

// 단추를 글자로 찾는다. 돌려주는 것: 가운데 좌표.
const at = async (label) => {
  const p = await ev(`(() => { const b = [...document.querySelectorAll('.adm-main button')]
      .find((b) => b.textContent.trim() === ${JSON.stringify(label)});
    if (!b) return null; b.scrollIntoView({ block: 'center' }); const r = b.getBoundingClientRect();
    return { x: r.left + r.width / 2, y: r.top + r.height / 2 } })()`)
  if (!p) fail(`「${label}」 단추가 없다`)
  return p
}
// 진짜 마우스로 누르고, 기다리다 한 번 더 누른다 — 운영자가 백업을 기다리다 한 것이 이것이다. 더블클릭(clickCount 2)은
// Chrome 이 제출을 한 번만 해서 이 결함을 보이지 않는다(실측: 고치기 전에도 POST 하나). 따로 누르면 앞 요청이 끊기고
// POST 가 둘이 된다.
const clickTwice = async (p) => {
  for (const i of [1, 2]) {
    const m = { x: p.x, y: p.y, button: 'left', clickCount: 1 }
    await cdp.send('Input.dispatchMouseEvent', { type: 'mousePressed', ...m }, sessionId)
    await cdp.send('Input.dispatchMouseEvent', { type: 'mouseReleased', ...m }, sessionId)
    if (i === 1) await wait(500)
  }
}
const form = (sel) => `document.querySelector(${JSON.stringify(sel)}).form`
const state = (f) => `{ busy: ${f}.getAttribute('aria-busy') === 'true', label: ${f}.querySelector('button.adm-btn').textContent.trim() }`
const busy = (sel) => ev(`(() => { const f = ${form(sel)}; return ${state('f')} })()`)
// **제출한 화면이 기다리는 동안에는 CDP 로 그 화면을 읽을 수 없다** — Chrome 이 이동이 끝날 때까지 그 탭으로 가는
// 명령을 미뤄 둔다(실측: 붙잡힌 동안 Runtime.evaluate 와 DOM.getDocument 가 답하지 않았다). 마우스와 Fetch 는
// 지나간다. 그래서 화면 안의 탐침이 제출 0.2초 뒤의 단추를 sessionStorage 에 적고, 이동이 끝난 뒤 읽는다.
const probe = (sel) => ev(`(() => { const f = ${form(sel)}; sessionStorage.removeItem('busyProbe')
  f.addEventListener('submit', () => setTimeout(() => sessionStorage.setItem('busyProbe', JSON.stringify(${state('f')})), 200)) })()`)
const probed = async () => JSON.parse(await ev(`sessionStorage.getItem('busyProbe')`) ?? 'null')

// ---- 저장: 기다리는 동안 두 번 눌러도 하나만 간다 --------------------------------------------------------
await go(BASE + '/admin/backup')
await ev(`(() => { const f = ${form('input[name="backup.hour"]')};
  f.querySelector('input[name="backup.auto"]').checked = true
  f.querySelector('input[name="backup.hour"]').value = '5'
  f.querySelector('input[name="backup.keep"]').value = '6' })()`)
await probe('input[name="backup.hour"]')
// bfcache 로 돌아왔는지 알기 위한 표시 — 같은 페이지가 살아 돌아오면 이 값이 남아 있다.
await ev('window.__busyMark = 1')
posts = 0
await clickTwice(await at('저장'))
await wait(600)
check('기다리다 다시 눌러도 저장 요청은 하나다', posts === 1, `POST ${posts}번`)
let done = loaded()
await release()
await done
const s = await probed()
check('기다리는 동안 단추가 「처리 중」을 말한다', s?.busy && s.label === '처리 중…', JSON.stringify(s))
const n = await ev(`document.querySelector('.adm-main > [role=status]')?.textContent.trim() ?? null`)
check('보낸 값이 그대로 도착했다', n === '자동 백업 설정을 저장했습니다 — 켜짐 · 매일 5시 · 6개 보관', String(n))

// ---- 뒤로 가기: 잠근 채로 얼어 있던 화면이 돌아와도 단추는 살아 있다 ----------------------------------------
done = loaded()
await ev('history.back()')
await done
await wait(300)
if (await ev('window.__busyMark === 1')) {
  const b = await busy('input[name="backup.hour"]')
  check('뒤로 가기로 돌아온 화면의 단추가 살아 있다', !b.busy && b.label === '저장', JSON.stringify(b))
} else {
  // **헤드리스 Chrome 은 폼 제출 뒤의 화면을 bfcache 에 넣지 않는다** (실측: Page.backForwardCacheNotUsed 의
  // BrowsingInstanceNotSwapped). 데스크톱 Chrome·Safari 는 넣을 수 있으므로, 같은 화면에서 잠근 뒤 복귀 이벤트를 흉내
  // 낸다 — 진짜 복귀를 잰 것이 아니라고 적는다. 흉내 낸 submit 은 폼을 보내지 않는다.
  console.log('  · 두 번 누르기: 헤드리스는 폼 제출 뒤 bfcache 를 쓰지 않아 복귀 이벤트(pageshow persisted)를 흉내 낸다')
  const r = await ev(`(() => { const f = ${form('input[name="backup.hour"]')};
    f.dispatchEvent(new SubmitEvent('submit', { bubbles: true, cancelable: true, submitter: f.querySelector('button.adm-btn') }))
    const before = ${state('f')}
    dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true }))
    return { before, after: ${state('f')} } })()`)
  check('bfcache 로 돌아온 화면의 단추는 풀린다(흉내)', r.before.busy && !r.after.busy && r.after.label === '저장', JSON.stringify(r))
}

// ---- 내려받기: 화면은 그대로이고, 파일이 오기 시작하면 다시 누를 수 있다 -------------------------------------
await go(BASE + '/admin/backup')
const stored = 'input[name="name"]'
check('내려받기 폼 둘이 내려받기라고 밝힌다', (await ev(`document.querySelectorAll('form[data-download]').length`)) === 2)
await probe(stored)
posts = 0
started = 0
await clickTwice(await at('선택한 백업 내려받기'))
await wait(600)
check('기다리다 다시 눌러도 내려받기 요청은 하나다', posts === 1, `POST ${posts}번`)
await release()
await wait(300)
const d1 = await probed()
check('기다리는 동안 단추가 백업을 준비한다고 말한다', d1?.busy && d1.label === '백업 파일을 준비하는 중…', JSON.stringify(d1))
const back = await until(`(() => { const f = ${form(stored)}; return f.getAttribute('aria-busy') !== 'true'
  && f.querySelector('button.adm-btn').textContent.trim() === '선택한 백업 내려받기' })()`)
check('파일이 오기 시작하면 단추가 다시 살아난다', back && started === 1, `풀림 ${back}, 내려받기 ${started}번`)
check('내려받는 동안 화면은 그대로다', (await ev('location.pathname')) === '/admin/backup' &&
  (await ev(`document.querySelector('.adm-main > [role=alert]')`)) === null)
check('알림 쿠키는 쓰고 지운다', !(await ev(`document.cookie.includes('ondolith_download')`)))
posts = 0
const p2 = await at('선택한 백업 내려받기')
for (const type of ['mousePressed', 'mouseReleased']) {
  await cdp.send('Input.dispatchMouseEvent', { type, x: p2.x, y: p2.y, button: 'left', clickCount: 1 }, sessionId)
}
await wait(400)
await release()
for (let i = 0; i < 30 && started < 2; i++) await wait(100)
check('다시 누르면 또 받는다', posts === 1 && started === 2, `POST ${posts}번, 내려받기 ${started}번`)

// ---- 확인 창을 취소하면 잠기지 않는다 ------------------------------------------------------------------
await cdp.send('Fetch.disable', {}, sessionId)
await go(`${BASE}/admin/users/${MEMBER}`)
const answer = async (label, accept) => {
  const p = await at(label)
  const dialog = cdp.once('Page.javascriptDialogOpening', sessionId)
  dialog.catch(() => {})
  const nav = loaded()
  const m = { x: p.x, y: p.y, button: 'left', clickCount: 1 }
  await cdp.send('Input.dispatchMouseEvent', { type: 'mousePressed', ...m }, sessionId)
  const up = cdp.send('Input.dispatchMouseEvent', { type: 'mouseReleased', ...m }, sessionId)
  const d = await Promise.race([dialog, wait(3000).then(() => null)])
  if (d) await cdp.send('Page.handleJavaScriptDialog', { accept }, sessionId)
  await up
  if (accept || !d) await nav
  else await wait(500)
  return d
}
const active = () => ev(`[...document.querySelectorAll('.adm-main button')].some((b) => b.textContent.trim() === '비활성화')`)
await answer('비활성화', false)
check('확인 창을 취소한 단추는 잠기지 않는다', (await ev(`[...document.querySelectorAll('form')].every((f) => f.getAttribute('aria-busy') !== 'true')`)))
await answer('비활성화', true)
check('취소한 뒤 다시 눌러 확인하면 처리된다', !(await active()))
await answer('활성화', true)
check('되돌렸다', await active())

process.exit(failed ? 1 : 0)
