// 에디터 브라우저 시험 (FR-513·FR-514, D80 Phase 6 완료 기준).
//
// 실제 앱의 글쓰기 화면에서 SunEditor 에 **한글을 IME 조합으로** 넣고(ㅅ→시, ㄱ→고→공…),
// 사진을 에디터 경로로 올린 뒤 저장해서, 글 화면에 같은 글자와 사진이 보이는지 본다.
//
// **조합은 CDP 의 Input.imeSetComposition 으로 흉내 낸다.** 실제 운영체제 입력기는
// 자동화로 칠 수 없다. 그리고 이 흉내는 빈 문단 자리표시(`<p><br></p>`)에서 첫 자모를
// 따로 남긴다 — 에디터가 아니라 Chrome 의 일반 contenteditable 에서도 똑같다(2026-10-08
// 대조 실험). 그래서 첫 글자는 조합 없이 확정해 넣고(입력기가 한 글자를 바로 확정한 것과
// 같다), 그 뒤를 조합으로 넣는다. 확인하는 것은 「에디터의 키·입력 처리가 조합을 깨지
// 않는가」다 — 문단 끝, 굵은 글씨 안, 확정 뒤 다음 조합. 진짜 입력기는 D85 GAP-26.
//
// 환경: UI_BASE, UI_EMAIL, UI_PASSWORD, UI_BOARD(쓸 수 있는 게시판 슬러그).
import { launch, findChrome } from './lib-browser.mjs'

const BASE = process.env.UI_BASE
const BOARD = process.env.UI_BOARD ?? 'notice'
const fail = (msg) => { console.error('  ✗ ' + msg); process.exit(1) }
setTimeout(() => fail('에디터 시험이 120초 안에 끝나지 않았다'), 120000).unref()

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
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
const compose = async (steps, commit) => {
  for (const t of steps) {
    await cdp.send('Input.imeSetComposition', { text: t, selectionStart: t.length, selectionEnd: t.length }, sessionId)
  }
  await cdp.send('Input.insertText', { text: commit }, sessionId)
}

await go(BASE + '/login')
const st = await ev(`fetch('/login',{method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded'},
  body:new URLSearchParams({email:${JSON.stringify(process.env.UI_EMAIL)},password:${JSON.stringify(process.env.UI_PASSWORD)}})}).then(r=>r.status)`)
if (st !== 200) fail(`로그인 HTTP ${st}`)

// 지도 블록 (FR-716). 카카오 키를 넣고, 카카오 SDK 요청은 **가짜 SDK 로 가로챈다** — 진짜 키·도메인 등록
// 없이 maps.js 의 불러오기·검색·클릭·블록 넣기·글 화면 그리기를 본다. 진짜 SDK 가 그리는 모습은 이
// 시험 밖이다(D85 GAP-29). 바깥(dapi.kakao.com)으로는 요청이 나가지 않는다.
const FAKE_KAKAO = `window.kakao = { maps: {
  load: function (cb) { cb(); },
  LatLng: function (a, b) { this.getLat = function () { return a }; this.getLng = function () { return b } },
  Map: function (el, o) { var me = this; me.el = el; el.dataset.fakeCenter = o.center.getLat() + ',' + o.center.getLng();
    me.setCenter = function (c) { el.dataset.fakeCenter = c.getLat() + ',' + c.getLng() }; me.getLevel = function () { return o.level };
    window.__fakeKakaoMap = me },
  Marker: function (o) { var el = o.map.el; el.dataset.fakePin = o.position.getLat() + ',' + o.position.getLng();
    this.setPosition = function (p) { el.dataset.fakePin = p.getLat() + ',' + p.getLng() } },
  event: { addListener: function (t, type, fn) { t['__on_' + type] = fn } },
  services: { Status: { OK: 'OK' }, Places: function () { this.keywordSearch = function (q, cb) {
    cb([{ y: '37.5662952', x: '126.9779451', place_name: '서울특별시청' }], 'OK') } } } } };`
await cdp.send('Fetch.enable', { patterns: [{ urlPattern: '*dapi.kakao.com*' }] }, sessionId)
// lib-browser 의 CDP 는 once() 뿐이라 계속 듣는 수신자를 직접 붙인다.
cdp.listeners.push((msg) => {
  if (msg.method !== 'Fetch.requestPaused' || msg.sessionId !== sessionId) return
  cdp.send('Fetch.fulfillRequest', { requestId: msg.params.requestId, responseCode: 200,
    responseHeaders: [{ name: 'Content-Type', value: 'application/javascript' }],
    body: Buffer.from(FAKE_KAKAO).toString('base64') }, sessionId).catch(() => {})
})
const keySet = await ev(`fetch('/admin/settings',{method:'POST',body:new URLSearchParams({'map.kakao_key':'test-kakao-key'})}).then(r=>r.status)`)
if (keySet !== 200) fail(`지도 키 저장 HTTP ${keySet}`)

await go(`${BASE}/board/${BOARD}/write`)
for (let i = 0; i < 50 && !(await ev(`!!document.querySelector('.se-wrapper-wysiwyg')`)); i++) await sleep(100)
if (!(await ev(`!!document.querySelector('.se-wrapper-wysiwyg')`))) fail('글쓰기 화면에 에디터가 뜨지 않았다')
const label = await ev(`[...document.querySelectorAll('.se-toolbar button')].map(b=>b.getAttribute('aria-label')||'').join(' ')`)
if (!label.includes('굵게')) fail(`에디터 버튼이 한국어가 아니다: ${label.slice(0, 80)}`)
console.log('  ✓ 글쓰기 화면에 에디터가 한국어로 뜬다')

await ev(`document.getElementById('title').value = '에디터 한글 시험'`)
// 첫 글자는 확정으로, 그 뒤는 조합으로 (위 머리말).
await ev(`(() => { const w = document.querySelector('.se-wrapper-wysiwyg'); w.focus();
  const r = document.createRange(); r.selectNodeContents(w.querySelector('p') || w); r.collapse(false);
  const s = getSelection(); s.removeAllRanges(); s.addRange(r); return 1 })()`)
await cdp.send('Input.insertText', { text: '시' }, sessionId)
await compose(['ㄱ', '고', '공'], '공')
await cdp.send('Input.insertText', { text: ' ' }, sessionId)
await compose(['ㅅ', '사'], '사')
await compose(['ㄹ', '레', '례'], '례')
await sleep(200)
const typed = await ev(`document.querySelector('.se-wrapper-wysiwyg').textContent`)
if (typed !== '시공 사례') fail(`조합 결과가 「${typed}」 다, want 「시공 사례」`)
console.log('  ✓ 한글 IME 조합(ㄱ→고→공, ㅅ→사, ㄹ→레→례)이 그대로 남는다')

// 사진: 붙여넣기·끌어놓기와 같은 경로(submitFile)로 서버(P-214)에 올린다.
await ev(`new Promise((ok, no) => { const c = document.createElement('canvas'); c.width = 2400; c.height = 1600;
  const g = c.getContext('2d'); g.fillStyle = '#c33'; g.fillRect(0, 0, 2400, 1600);
  c.toBlob((b) => { const ed = document.querySelector('textarea[data-editor]').sunEditor;
    const img = ed.$.plugins && ed.$.plugins.image;
    if (!img) return no(new Error('image plugin 없음'));
    Promise.resolve(img.submitFile([new File([b], '현장.png', { type: 'image/png' })])).then(() => ok(1), no) }, 'image/png') })`)
let src = ''
for (let i = 0; i < 50 && !src; i++) {
  src = await ev(`(document.querySelector('.se-wrapper-wysiwyg img') || {}).getAttribute?.('src') || ''`)
  if (!src) await sleep(100)
}
if (!/^\/media\/[0-9a-f-]{36}$/.test(src)) fail(`에디터에 들어간 사진 주소가 「${src.slice(0, 40)}…」 다 — 서버 업로드(P-214)를 거치지 않았다`)
console.log('  ✓ 사진이 서버로 올라가 /media/{id} 로 들어간다')

// 지도: 「지도 넣기」 → 장소 검색 → 지도를 눌러 핀 옮기기 → 넣기.
const waitFor = async (expr, what) => {
  for (let i = 0; i < 50; i++) { const v = await ev(expr); if (v) return v; await sleep(100) }
  fail(what)
}
const opened = await ev(`(() => { const b = [...document.querySelectorAll('button')].find((x) => x.textContent === '지도 넣기');
  if (!b) return false; b.click(); return true })()`)
if (!opened) fail('지도 키가 있는데 에디터에 「지도 넣기」 단추가 없다')
await waitFor(`document.querySelector('dialog.map-pick .map-area')?.dataset.fakeCenter`, '지도 넣기 창에 카카오 지도가 그려지지 않았다')
await ev(`(() => { const d = document.querySelector('dialog.map-pick'); d.querySelector('.map-query').value = '서울시청';
  d.querySelector('.map-search').requestSubmit(); return 1 })()`)
await waitFor(`document.querySelector('dialog.map-pick .map-area').dataset.fakePin === '37.5662952,126.9779451'`, '장소 검색이 핀을 옮기지 않았다')
await ev(`window.__fakeKakaoMap.__on_click({ latLng: new kakao.maps.LatLng(37.57, 126.98) })`)
await waitFor(`document.querySelector('dialog.map-pick .map-area').dataset.fakePin === '37.57,126.98'`, '지도를 눌러도 핀이 옮겨 가지 않았다')
const pickedLabel = await ev(`document.querySelector('dialog.map-pick .map-label').value`)
if (pickedLabel !== '서울특별시청') fail(`검색한 장소 이름이 「${pickedLabel}」 다`)
await ev(`document.querySelector('dialog.map-pick .map-ok').click()`)
const block = await waitFor(`(() => { const i = document.querySelector('.se-wrapper-wysiwyg img[data-map]');
  return i && [i.dataset.map, i.dataset.lat, i.dataset.lng, i.dataset.label].join('|') })()`, '에디터에 지도 블록이 들어가지 않았다')
if (block !== 'kakao|37.5700000|126.9800000|서울특별시청') fail(`에디터의 지도 블록이 「${block}」 다`)
console.log('  ✓ 지도 넣기: 장소 검색·지도 클릭으로 고른 자리가 지도 블록으로 들어간다')

const posted = cdp.once('Page.loadEventFired', sessionId)
posted.catch(() => {})
await ev(`document.querySelector('form.form').requestSubmit()`)
await Promise.race([posted, new Promise((r) => setTimeout(r, 8000))])
await sleep(300)
await waitFor(`document.querySelector('.page-body.rich .map-view .map-area')?.dataset.fakePin`, '글 화면의 지도 블록이 지도로 바뀌지 않았다')
const view = await ev(`(() => { const r = document.querySelector('.page-body.rich');
  return r ? { text: r.textContent, img: (r.querySelector('img:not([data-map])') || {}).getAttribute?.('src') || '',
    map: r.querySelector('.map-view .map-area').dataset.fakePin + '|' + r.querySelector('.map-caption').textContent,
    path: location.pathname } : null })()`)
if (!view) fail('저장한 글이 에디터 본문(.rich)으로 그려지지 않았다: ' + (await ev('location.pathname')))
if (!view.text.includes('시공 사례')) fail(`저장한 글의 본문이 「${view.text}」 다`)
if (view.img !== src) fail(`저장한 글의 사진이 「${view.img}」 다, want ${src}`)
if (view.map !== '37.57,126.98|서울특별시청') fail(`저장한 글의 지도가 「${view.map}」 다 — 정화가 지웠거나 maps.js 가 그리지 않았다`)
const size = await ev(`fetch(${JSON.stringify(src)}).then(r => r.blob()).then(b => createImageBitmap(b)).then(i => i.width + 'x' + i.height)`)
if (size !== '1920x1280') fail(`올린 사진이 ${size} 로 나간다, want 1920x1280 (긴 변 1920px)`)
console.log(`  ✓ 저장한 글에 같은 글자와 1920px 로 줄인 사진, 지도가 보인다 (${view.path})`)
process.exit(0)
