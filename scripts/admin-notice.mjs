// 관리자 저장 알림·확인 창 브라우저 시험 (FR-724, FR-725).
//
// 실제 앱에서 단추를 마우스로 눌러 본다. A-604 자동 백업 설정을 저장하면 돌아간 화면 위쪽에 무엇이 됐는지 한 줄이
// role="status" 로 보이고, 새로 고침하면 사라지는지. 휴대폰 폭에서도 화면이 옆으로 넘치지 않는지. 사용자 상세의
// 「비활성화」를 누르면 확인 창이 뜨고, 취소하면 아무것도 바뀌지 않으며, 확인하면 처리되고 알림이 뜨는지.
// 주문 상태 「변경」의 확인 창이 고른 상태를 말하는지.
//
// 확인 창은 페이지의 스크립트를 멈춘다 — 누르기 명령은 창에 답한 뒤에야 끝나므로, 창이 뜨기를 기다렸다 답한다.
//
// 환경: UI_BASE, UI_EMAIL, UI_PASSWORD, UI_MEMBER(회원 id), UI_ORDER(주문번호).
import { launch, findChrome } from './lib-browser.mjs'

const BASE = process.env.UI_BASE
const MEMBER = process.env.UI_MEMBER
const ORDER = process.env.UI_ORDER
let failed = 0
const check = (name, ok, detail = '') => {
  console.log(`  ${ok ? '✓' : '✗'} 알림·확인 창: ${name}${ok ? '' : ' — ' + detail}`)
  if (!ok) failed++
}
const fail = (msg) => { console.error('  ✗ 알림·확인 창: ' + msg); process.exit(1) }
setTimeout(() => fail('120초 안에 끝나지 않았다'), 120000).unref()
if (!BASE || !MEMBER || !ORDER) fail('UI_BASE·UI_MEMBER·UI_ORDER 가 필요하다')

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
const notice = () => ev(`(() => { const n = document.querySelector('.adm-main > [role=status]');
  if (!n) return null; const r = n.getBoundingClientRect();
  return { text: n.textContent.trim(), top: r.top, height: r.height, inView: r.top >= 0 && r.bottom <= innerHeight } })()`)

// 단추를 글자로 찾아 그 가운데를 진짜 마우스로 누른다. answer 가 있으면 뜬 확인 창에 그렇게 답한다.
// 돌려주는 것: 확인 창의 문구(없으면 null)와, 누른 뒤 페이지가 바뀌었는지.
const press = async (label, answer) => {
  const at = await ev(`(() => { const b = [...document.querySelectorAll('.adm-main button')]
      .find((b) => b.textContent.trim() === ${JSON.stringify(label)});
    if (!b) return null; b.scrollIntoView({ block: 'center' }); const r = b.getBoundingClientRect();
    return { x: r.left + r.width / 2, y: r.top + r.height / 2 } })()`)
  if (!at) fail(`「${label}」 단추가 없다`)
  const before = await ev('location.href + "#" + performance.timeOrigin')
  const dialog = cdp.once('Page.javascriptDialogOpening', sessionId)
  dialog.catch(() => {})
  const nav = loaded()
  const mouse = { x: at.x, y: at.y, button: 'left', clickCount: 1 }
  await cdp.send('Input.dispatchMouseEvent', { type: 'mousePressed', ...mouse }, sessionId)
  const release = cdp.send('Input.dispatchMouseEvent', { type: 'mouseReleased', ...mouse }, sessionId)
  const d = await Promise.race([dialog, wait(3000).then(() => null)])
  if (d) await cdp.send('Page.handleJavaScriptDialog', { accept: answer === true }, sessionId)
  await release
  if (d && answer === undefined) fail(`「${label}」 은 묻지 않아야 하는데 확인 창이 떴다: ${d.message}`)
  if (!d || answer) await nav
  else await wait(800) // 취소했으면 아무 요청도 나가지 않는다 — 혹시 나가면 바뀐 것을 잡도록 잠깐 둔다
  const after = await ev('location.href + "#" + performance.timeOrigin')
  return { dialog: d?.message ?? null, moved: before !== after }
}

await go(BASE + '/login')
const st = await ev(`fetch('/login',{method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded'},
  body:new URLSearchParams({email:${JSON.stringify(process.env.UI_EMAIL)},password:${JSON.stringify(process.env.UI_PASSWORD)}})}).then(r=>r.status)`)
if (st !== 200) fail(`로그인 HTTP ${st}`)

// ---- 저장 알림 (FR-724): A-604 — 운영자가 저장됐는지 몰라 네 번 누른 화면 -------------------------------
await go(BASE + '/admin/backup')
check('저장하기 전에는 알림이 없다', (await notice()) === null)
await ev(`(() => { const f = document.querySelector('input[name="backup.hour"]').form;
  f.querySelector('input[name="backup.auto"]').checked = true
  f.querySelector('input[name="backup.hour"]').value = '4'
  f.querySelector('input[name="backup.keep"]').value = '7' })()`)
const saved = await press('저장')
const want = '자동 백업 설정을 저장했습니다 — 켜짐 · 매일 4시 · 7개 보관'
const n = await notice()
check('저장하면 묻지 않고 바로 보낸다', saved.dialog === null && saved.moved, JSON.stringify(saved))
check('돌아간 화면에 무엇이 됐는지 보인다', n?.text === want, JSON.stringify(n))
check('알림이 화면 안, 제목 아래에 보인다', !!n && n.height > 0 && n.inView, JSON.stringify(n))
const done = loaded()
await cdp.send('Page.reload', {}, sessionId)
await done
check('새로 고침하면 알림이 없다', (await notice()) === null)

// 휴대폰 폭: 알림을 다시 띄워 화면이 옆으로 넘치지 않는지 본다.
await cdp.send('Emulation.setDeviceMetricsOverride', { width: 375, height: 800, deviceScaleFactor: 2, mobile: true }, sessionId)
await go(BASE + '/admin/backup')
await press('저장')
const m = await notice()
const overflow = await ev('document.documentElement.scrollWidth - document.documentElement.clientWidth')
check('휴대폰 폭에서도 알림이 보인다', m?.text === want, JSON.stringify(m))
check('휴대폰 폭에서 화면이 옆으로 넘치지 않는다', overflow <= 0, `${overflow}px 넘침`)
await cdp.send('Emulation.clearDeviceMetricsOverride', {}, sessionId)

// ---- 확인 창 (FR-725): 사용자 비활성화 -------------------------------------------------------------
const active = () => ev(`[...document.querySelectorAll('.adm-main button')].some((b) => b.textContent.trim() === '비활성화')`)
await go(`${BASE}/admin/users/${MEMBER}`)
check('활성 회원의 상세에 「비활성화」가 있다', await active())
const no = await press('비활성화', false)
check('「비활성화」를 누르면 묻는다', no.dialog === '이 사용자를 비활성화할까요? 로그인할 수 없게 됩니다.', String(no.dialog))
check('취소하면 요청이 나가지 않는다', !no.moved)
await go(`${BASE}/admin/users/${MEMBER}`)
check('취소한 뒤에도 회원은 활성이다', await active())
const yes = await press('비활성화', true)
check('확인하면 처리된다', yes.moved && !(await active()))
check('처리한 뒤 알림이 뜬다', (await notice())?.text === '사용자를 비활성화했습니다 — 로그인할 수 없습니다.', JSON.stringify(await notice()))
// 되돌린다 — 활성화는 묻지 않는다(되돌릴 수 있는 동작).
const back = await press('활성화')
check('「활성화」는 묻지 않는다', back.dialog === null && back.moved, JSON.stringify(back))
check('다시 활성이 됐다', await active())

// ---- 확인 창: 주문 상태 변경은 고른 상태를 말한다 ---------------------------------------------------
// 취소했는지는 주소가 아니라 주문 상태를 다시 읽어 본다 — confirm 앞의 return 이 빠지면 취소해도 폼이 나가고,
// 그 왕복이 느리면 주소로는 알아채지 못한다.
const status = () => ev(`[...document.querySelectorAll('dt')].find((t) => t.textContent.trim() === '상태')?.nextElementSibling?.textContent.trim() ?? null`)
await go(`${BASE}/admin/orders/${ORDER}`)
const was = await status()
const to = await ev(`document.querySelector('select[name=to]')?.value ?? null`)
if (to === null) {
  check('주문 상세에 다음 상태 고르기가 있다', false, `「${was}」 에서 옮길 수 있는 상태가 없다`)
} else {
  const ask = await press('변경', false)
  check('「변경」을 누르면 고른 상태로 바꿀지 묻는다',
    ask.dialog === `주문 상태를 「${to}」(으)로 바꿀까요? 구매자 화면에도 바로 반영됩니다.`, String(ask.dialog))
  await go(`${BASE}/admin/orders/${ORDER}`)
  check('취소하면 주문 상태가 그대로다', !ask.moved && (await status()) === was, `${was} → ${await status()}`)
}

process.exit(failed ? 1 : 0)
