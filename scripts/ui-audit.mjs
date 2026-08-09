// 모든 화면을 여러 폭에서 열어 **레이아웃을 잰다.**
//
// `make screens` 는 응답과 본문 길이를, `make crawl` 은 링크를 본다. 둘 다
// 200 을 받으면 통과하므로 **글자가 상자 밖으로 나가든 표가 화면을 넘든 초록**
// 이다. 여기서는 브라우저에 그려 놓고 좌표를 잰다 — 눈으로 보는 확인을 대신할
// 수는 없지만(그쪽은 `make shots`), 눈이 놓치는 것을 숫자로 잡는다.
//
// 실행: make ui
import { findChrome, launch, WIDTHS } from './lib-browser.mjs'

const BASE = process.env.UI_BASE ?? 'http://127.0.0.1:8099'
const EMAIL = process.env.UI_EMAIL ?? ''
const PASSWORD = process.env.UI_PASSWORD ?? ''
const ROLE = process.env.UI_ROLE ?? '?'

// ---- 페이지 안에서 도는 감사 ------------------------------------------------
//
// 반환값은 결함 문자열의 배열이다. **오탐이 하나라도 있으면 이 검사는 곧
// 꺼진다** — 그래서 제외 사유는 전부 코드에 적어 둔다.
const AUDIT = String.raw`(() => {
  const out = []
  const px = (v) => Math.round(v * 10) / 10
  const vw = document.documentElement.clientWidth

  // 1) 가로 스크롤. 화면이 옆으로 밀리면 그 페이지는 좁은 기기에서 못 쓴다.
  const over = document.documentElement.scrollWidth - vw
  if (over > 1) out.push('가로 넘침 ' + px(over) + 'px')

  const all = [...document.querySelectorAll('body *')]
  const vis = (el) => {
    const cs = getComputedStyle(el)
    return cs.display !== 'none' && cs.visibility !== 'hidden' && el.getClientRects().length > 0
  }
  const name = (el) => el.tagName.toLowerCase() +
    (el.id ? '#' + el.id : '') +
    (typeof el.className === 'string' && el.className ? '.' + el.className.trim().split(/\s+/).join('.') : '') +
    (el.textContent && el.textContent.trim() ? ' «' + el.textContent.trim().slice(0, 18) + '»' : '')

  // **가로로 스크롤되는 상자 안은 넓어도 된다.** 그것이 스크롤의 뜻이다 —
  // 관리자 표는 adm-table-wrap 의 overflow-x auto 안에서 660px 를 갖는다.
  // 이것을 빼지 않으면 스크롤되는 표마다 오탐이 나고, 오탐이 있는 검사는
  // 곧 꺼진다.
  const inScroller = (el) => {
    for (let p = el.parentElement; p && p !== document.body; p = p.parentElement) {
      const o = getComputedStyle(p).overflowX
      if (o === 'auto' || o === 'scroll') return true
    }
    return false
  }

  for (const el of all) {
    if (!vis(el)) continue
    const cs = getComputedStyle(el)
    const r = el.getBoundingClientRect()

    // 2) 뷰포트 오른쪽으로 삐져나간 요소. 스크롤이 안 생겨도(부모가 hidden)
    //    글자는 잘린다.
    if (cs.position !== 'fixed' && r.width > 0 && r.right - vw > 1 && !inScroller(el)) {
      out.push('뷰포트 밖으로 ' + px(r.right - vw) + 'px: ' + name(el))
    }

    // 3) 부모의 아래 테두리와 자식의 아래 테두리가 겹친다 — 선이 두 겹으로
    //    보이고 두께가 나머지와 달라 표가 어긋나 보인다.
    const parent = el.parentElement
    if (parent && parseFloat(cs.borderBottomWidth) > 0) {
      const pcs = getComputedStyle(parent)
      if (parseFloat(pcs.borderBottomWidth) > 0) {
        const pr = parent.getBoundingClientRect()
        if (Math.abs(r.bottom - pr.bottom) < 1.5) {
          out.push('아래 테두리가 부모와 겹침: ' + name(el))
        }
      }
    }

    // 4) 상자보다 넓은 글. overflow hidden 이면 잘리고, 말줄임표도 없으면
    //    사용자는 잘린 줄 모른다.
    if (cs.overflowX === 'hidden' && cs.textOverflow !== 'ellipsis' &&
        el.children.length === 0 && el.scrollWidth - el.clientWidth > 1) {
      out.push('글이 잘림 ' + px(el.scrollWidth - el.clientWidth) + 'px: ' + name(el))
    }
  }

  // 5) 스타일이 붙지 않은 폼 요소. 브라우저 기본 모양이 그대로 나오면 그
  //    화면만 다른 사이트처럼 보인다. 판정: 테두리도 배경도 없다.
  for (const el of document.querySelectorAll('input,select,textarea,button')) {
    if (!vis(el)) continue
    const cs = getComputedStyle(el)
    if (['hidden', 'checkbox', 'radio', 'file'].includes(el.type)) continue
    const bare = parseFloat(cs.borderTopWidth) === 0 &&
      (cs.backgroundColor === 'rgba(0, 0, 0, 0)' || cs.backgroundColor === 'transparent')
    if (bare) out.push('스타일 없는 폼 요소: ' + name(el))
  }

  // 7) **형제끼리 왼쪽이 어긋난다.** 세로로 쌓이는 상자 안에서 자식들이 서로
  //    다른 x 에서 시작하면 눈에 「정렬이 안 맞는」 것으로 보인다. 가로로
  //    늘어놓는 컨테이너(flex row·grid·inline)는 대상이 아니다 — 거기서는
  //    다른 x 가 정상이다.
  for (const box of all) {
    if (!vis(box)) continue
    const cs = getComputedStyle(box)
    const row = (cs.display === 'flex' || cs.display === 'inline-flex') &&
      !cs.flexDirection.startsWith('column')
    if (row || cs.display.includes('grid') || cs.display.includes('inline')) continue
    // 표의 행·열은 가로로 늘어선다. display table-row 는 flex 도 grid 도
    // 아니지만 자식이 나란히 서는 것이 정상이다 — 빼지 않으면 표 있는 화면마다
    // 행 수만큼 오탐이 난다 (153건이 그랬다).
    if (cs.display.startsWith('table') || cs.display === 'list-item') continue
    // **가운데·오른쪽으로 모으는 상자는 어긋난 것이 아니다.** 세로 flex 에
    // align-items 가 center 나 end 면 자식마다 x 가 다른 것이 의도다 —
    // QR 라벨(가운데 정렬)이 그래서 오탐으로 잡혔다. text-align 도 같다.
    const ai = cs.alignItems
    if (ai && !['stretch', 'normal', 'flex-start', 'start', 'baseline'].includes(ai)) continue
    if (['center', 'right', 'end'].includes(cs.textAlign)) continue
    const kids = [...box.children].filter((k) => {
      if (!vis(k)) return false
      const kc = getComputedStyle(k)
      // 뜬 요소와 인라인 요소는 줄을 따라간다 — 왼쪽이 다른 것이 정상이다.
      return kc.position === 'static' && !kc.display.startsWith('inline') && kc.float === 'none'
    })
    if (kids.length < 2) continue
    const xs = [...new Set(kids.map((k) => Math.round(k.getBoundingClientRect().x)))]
    if (xs.length > 1) {
      const spread = Math.max(...xs) - Math.min(...xs)
      if (spread > 1) {
        out.push('형제끼리 왼쪽이 ' + px(spread) + 'px 어긋남: ' + name(box).slice(0, 60))
      }
    }
  }

  // 8) **같은 화면의 버튼 높이가 제각각이다.** 두 종류까지는 크기 위계로
  //    읽히지만(주 동작과 보조 동작), 그 이상은 통일감이 없는 것이다.
  const btnH = new Set()
  for (const el of document.querySelectorAll('button, .btn, .adm-btn')) {
    if (!vis(el)) continue
    btnH.add(Math.round(el.getBoundingClientRect().height))
  }
  if (btnH.size > 2) {
    const who = [...document.querySelectorAll('button, .btn, .adm-btn')].filter(vis)
      .map((e) => Math.round(e.getBoundingClientRect().height) + '=' + name(e).slice(0, 34))
    out.push('버튼 높이가 ' + btnH.size + ' 종류: ' + [...new Set(who)].join(' | '))
  }

  // 9) **빈 표는 재도 소용없다.** 행이 없으면 열이 내용 없이 좁아져 넘치지
  //    않고, 그 표의 반응형 동작은 검사되지 않은 채 통과한다 — 감싸미를 새로
  //    붙인 표가 정작 빈 상태로만 그려지고 있었다.
  //
  //    빈 상태는 adm-empty(관리자)·empty(테마) 클래스가 표시한다 — colspan 으로
  //    판정하면 오탐이 난다: 메뉴 관리처럼 **자료 행도** 한 칸을 가로지르는
  //    화면이 있다.
  for (const t of document.querySelectorAll('table')) {
    if (!vis(t)) continue
    const rows = [...t.querySelectorAll('tbody tr')]
    const data = rows.filter((tr) => !tr.querySelector('.adm-empty, .empty'))
    // 고르기 전 상태는 「무엇을 고르라」는 안내다 — 잴 자료가 없는 것이 맞다.
    // 그 화면도 함께 열어 두고(ui.sh), 고른 뒤의 표를 재는 것이 목적이다.
    const prompt = t.querySelector('.adm-empty, .empty')
    if (data.length === 0 && !/입력하세요|고르세요|선택하세요/.test(prompt?.textContent ?? '')) {
      out.push('빈 표 — 자료가 없어 레이아웃을 재지 못했다: ' +
        (t.className || t.tagName.toLowerCase()) + ' «' +
        [...t.querySelectorAll('thead th')].map((th) => th.textContent.trim()).join('/') + '»')
    }
  }

  // 10) **머리·본문·바닥이 한 축에 서는가.**
  //
  //     main 만 가운데로 모으고 머리·바닥은 화면 전체 폭이면, 넓은 화면에서
  //     사이트 이름은 왼쪽 끝에 붙고 본문 카드는 한참 안쪽에서 시작한다 —
  //     세로줄이 두 개 생긴다. 넘치지도 잘리지도 않으므로 위의 어느 규칙도
  //     재지 않는다. 브라우저로 **찍어 보고** 알았고, 고친 뒤에도 푸터만
  //     남았다 — 단축 속성 padding 이 뒤에서 덮고 있었다.
  //
  //     좁은 화면(본문이 화면을 다 쓰는 폭)에서는 셋이 같은 여백이라 이 규칙이
  //     저절로 만족된다. 어긋남은 넓은 화면에서만 생긴다.
  {
    // **테마 화면일 때만 본다.** 관리자 트리는 사이드바가 있는 다른 레이아웃
    // 이라(main.adm-main) 띠가 없는 것이 정상이다 — 구분하지 않으면 관리자
    // 화면마다 「띠를 못 찾았다」가 뜬다. 실제로 그렇게 나왔다.
    const m = document.querySelector('main:not(.adm-main)')
    const bands = ['header.site-header', 'footer.site-footer']
    if (m && vw > 1040) {
      // 본문의 기준선은 카드가 아니라 main 의 내용 상자다.
      const ms = getComputedStyle(m)
      const mLeft = m.getBoundingClientRect().left + parseFloat(ms.paddingLeft)
      for (const sel of bands) {
        const b = document.querySelector(sel)
        // **못 찾은 것과 어긋나지 않은 것은 다르다.** 하나라도 빠지면 그만큼
        // 이 규칙은 덜 재는 것이고, 조용히 통과하면 아무도 그것을 모른다 —
        // 이 세션에서 헛도는 검사가 여러 번 나왔다. 둘 다 없을 때만 말하면
        // 띠 하나가 사라지는 것은 그대로 지나간다 (실제로 그랬다).
        if (!b || !vis(b)) {
          out.push('세로축을 잴 띠가 없다: ' + sel + ' — 그만큼 규칙이 헛돈다')
          continue
        }
        const bs = getComputedStyle(b)
        const bLeft = b.getBoundingClientRect().left + parseFloat(bs.paddingLeft)
        if (Math.abs(bLeft - mLeft) > 2) {
          out.push('본문과 세로축이 어긋남 ' + px(bLeft - mLeft) + 'px: ' + sel)
        }
      }
    }
  }

  // 6) 누를 수 없는 크기. 좁은 화면에서만 본다 — 손가락이 쓰는 자리다.
  if (vw <= 480) {
    for (const el of document.querySelectorAll('a,button,input[type=submit],select')) {
      if (!vis(el)) continue
      const r = el.getBoundingClientRect()
      // 문장 안의 링크는 줄 높이를 따르므로 대상이 아니다 — 부모가 문단이면
      // 넘긴다.
      const inText = el.tagName === 'A' && ['P', 'LI', 'SPAN', 'TD', 'DD'].includes(
        el.parentElement?.tagName)
      if (!inText && r.height > 0 && r.height < 24) {
        out.push('누르기 어려운 크기 ' + px(r.height) + 'px: ' + name(el))
      }
    }
  }
  return out
})()`

// ---- 실행 -----------------------------------------------------------------

async function main() {
  const bin = findChrome()
  if (!bin) {
    console.error('Chromium 을 찾지 못했다 (Playwright 캐시). UI 감사를 건너뛰지 않고 중단한다.')
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
    if (exceptionDetails) throw new Error(exceptionDetails.text + ' ' + (result?.description ?? ''))
    return result.value
  }

  // 역할의 세션을 만든다. 익명은 로그인하지 않는다 — 그 상태가 곧 그 역할이다.
  await go(BASE + '/login')
  if (EMAIL) {
    await evaluate(`fetch('/login',{method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded'},
      body:new URLSearchParams({email:${JSON.stringify(EMAIL)},password:${JSON.stringify(PASSWORD)}})}).then(r=>r.status)`)
  }

  const urls = [...new Set(JSON.parse(process.env.UI_URLS ?? '[]'))]
  if (urls.length < 3) {
    console.error(`검사할 주소가 ${urls.length} 개다 — 감사가 헛돈다`)
    process.exit(1)
  }

  // UI_MEASURE 가 있으면 그 선택자의 자식 폭을 재서 낸다 — 결함의 원인을
  // 눈이 아니라 숫자로 찾기 위한 것이고, 평소에는 돌지 않는다.
  const MEASURE = process.env.UI_MEASURE
  let bad = 0
  let checked = 0
  for (const [i, width] of WIDTHS.entries()) {
    // 상한 창(1분)이 지나가길 기다린다. 폭 하나가 수십 장이라 쉬지 않으면
    // 두 번째 폭부터 전부 오류 화면을 재게 된다.
    if (i > 0) await new Promise((r) => setTimeout(r, 61000))
    let sinceRest = 0
    await cdp.send('Emulation.setDeviceMetricsOverride',
      { width, height: 900, deviceScaleFactor: 1, mobile: width <= 480 }, sessionId)
    for (const path of urls) {
      // 관리자 트리는 **분당 60건**이다 (D15 4.3-2). 50장마다 창이 지나가길
      // 기다린다 — 기다리지 않으면 그 뒤 화면이 전부 오류 문구가 되고,
      // 오류 문구에는 잴 것이 없어 **감사가 조용히 통과한다.**
      if (path.startsWith('/admin') && ++sinceRest >= 45) {
        sinceRest = 0
        await new Promise((r) => setTimeout(r, 61000))
      }
      await go(BASE + path)
      const status = await evaluate(`document.querySelector('h1,h2')?.textContent?.trim() ?? ''`)
      if (MEASURE) {
        const m = await evaluate(`(() => { const p = document.querySelector(${JSON.stringify(MEASURE)});
          if (!p) return null;
          const b = (e) => { const r = e.getBoundingClientRect();
            return e.tagName.toLowerCase() + (e.className ? '.' + String(e.className).trim().split(/\\s+/)[0] : '') +
              ' x=' + Math.round(r.x) + ' w=' + Math.round(r.width) + ' [' + getComputedStyle(e).width + '/' + getComputedStyle(e).minWidth + '/' + getComputedStyle(e).boxSizing + ']' };
          return { self: b(p), pad: getComputedStyle(p).padding, box: getComputedStyle(p).boxSizing, cols: getComputedStyle(p).gridTemplateColumns, kids: [...p.children].map(b) } })()`)
        if (m) console.log(`  · ${width}px ${path}: ${JSON.stringify(m)}`)
      }
      const defects = await evaluate(AUDIT)
      checked++
      // **화면이 아니라 오류를 재고 있지 않은지 본다.** 관리자 트리는 분당
      // 60건이라(D15 4.3-2) 폭마다 수십 장을 열면 반드시 걸리는데, 429 의
      // 본문은 짧은 `<pre>` 라 결함이 하나도 안 나온다 — **감사가 통과한다.**
      // 실제로 그랬다: `/admin/webhooks` 부터 뒤쪽 화면 전부가 오류 문구였고
      // 그 표의 감싸미를 걷어내는 변이가 물지 않았다.
      const wall = await evaluate(`document.body.textContent.includes('요청이 너무 잦습니다')`)
      if (wall) {
        console.error(`  ✗ ${width}px ${path} → 요청 상한에 걸렸다. 이후 화면은 ` +
          '감사되지 않으므로 폭 사이에 쉬거나 요청을 줄여야 한다')
        process.exit(1)
      }
      if (defects.length) {
        bad += defects.length
        console.log(`  ✗ ${width}px ${path}${status ? ' (' + status.slice(0, 20) + ')' : ''}`)
        for (const d of [...new Set(defects)].slice(0, 6)) console.log(`      ${d}`)
      }
    }
  }

  // 브라우저와 임시 디렉터리 정리는 lib-browser 가 프로세스 종료에 맞춰 한다.
  console.log(`  ${ROLE}: ${checked} 조합 확인 · 결함 ${bad} 건`)
  process.exit(bad === 0 ? 0 : 1)
}

main().catch((e) => {
  console.error(e)
  process.exit(1)
})
