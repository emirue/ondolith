// CDP 의 **비동기 정리 로직**을 검증한다.
//
// 여기 있는 것은 타이머와 이벤트 정리다 — 깨져도 화면에는 아무 표시가 없고,
// 감사가 조용히 매달리는 것으로만 드러난다. 실제로 감사 하나가 CPU 0% 로 한
// 시간을 매달려 있었고 로그는 마지막 줄에서 멈춘 채였다. **멈추는 게이트는
// 실패하는 게이트보다 나쁘다**: 신호를 주지 않으면서 CI 잡의 시간을 다 쓴다.
//
// 브라우저를 띄우지 않는다. `CDP` 가 받는 것은 `addEventListener`/`send` 를 가진
// 객체뿐이므로 가짜 소켓으로 충분하고, 그래야 이 검사가 `make check` 에 들어간다
// (오프라인 게이트에는 브라우저가 없다).
//
// 실행: node --test scripts/   (make check 의 test 단계가 부른다)
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { CDP } from './lib-browser.mjs'

// 가짜 WebSocket. 보낸 것을 기록하고, 원할 때 답을 흘려 넣는다.
class FakeWS {
  constructor() {
    this.handlers = {}
    this.sent = []
  }
  addEventListener(type, fn) {
    ;(this.handlers[type] ??= []).push(fn)
  }
  send(data) {
    this.sent.push(JSON.parse(data))
  }
  emit(type, ev) {
    for (const fn of this.handlers[type] ?? []) fn(ev)
  }
  reply(id, body) {
    this.emit('message', { data: JSON.stringify({ id, ...body }) })
  }
}

const timers = () =>
  process.getActiveResourcesInfo().filter((r) => r === 'Timeout').length

// **이 파일의 모든 테스트에 시간 제한을 건다.**
//
// 여기서 지키는 것이 「무한 대기」이므로, 그 대상을 깨뜨렸을 때 검사가 실패
// 대신 **멈추면 안 된다** — 고치려는 병과 같은 모양이다. abort 를 지운 변이가
// 실제로 `not ok` 대신 무응답을 냈다.
//
// 타임아웃 시험도 예외가 아니다: `send()` 안의 타이머가 깨지면 그 프라미스는
// 영영 안 풀린다. Node 는 이벤트 루프가 비면 취소해 주지만 **타이머가 하나라도
// 살아 있으면 그 방어는 돌지 않는다.** Makefile 의 `--test-timeout` 이 바깥
// 그물이고, 이것이 안쪽 그물이다.
const FAST = { timeout: 2000 }

test('답이 오지 않으면 타임아웃으로 죽는다', FAST, async () => {
  const ws = new FakeWS()
  const cdp = new CDP(ws)
  await assert.rejects(
    () => cdp.send('Runtime.evaluate', {}, 's', 40),
    /CDP 무응답 40ms: Runtime\.evaluate/,
    '답이 없는데 프라미스가 풀렸다 — 이것이 감사를 한 시간 매달았다',
  )
})

test('답이 오면 풀리고, **타이머가 남지 않는다**', FAST, async () => {
  const ws = new FakeWS()
  const cdp = new CDP(ws)
  const before = timers()
  const p = cdp.send('Page.navigate', { url: 'about:blank' }, 's', 60000)
  ws.reply(ws.sent[0].id, { result: { frameId: 'f1' } })
  assert.deepEqual(await p, { frameId: 'f1' })

  // **`clearTimeout` 이 빠지면 promise 상태로는 드러나지 않는다** — 이미 풀린
  // 프라미스를 다시 reject 해도 아무 일이 없기 때문이다. 드러나는 곳은 이벤트
  // 루프다: 60초짜리 타이머가 살아남아 프로세스를 붙잡는다.
  assert.equal(timers(), before, '응답 뒤에도 타이머가 남았다 (clearTimeout 누락)')
  assert.equal(cdp.waiting.size, 0, '풀린 요청이 대기 목록에 남았다')
})

test('CDP 오류 응답은 reject 되고, 타이머가 남지 않는다', FAST, async () => {
  const ws = new FakeWS()
  const cdp = new CDP(ws)
  const before = timers()
  const p = cdp.send('Runtime.evaluate', {}, 's', 60000)
  ws.reply(ws.sent[0].id, { error: { code: -32000, message: '없는 세션' } })
  await assert.rejects(p, /없는 세션/)
  assert.equal(timers(), before, '오류 응답 뒤에도 타이머가 남았다')
  assert.equal(cdp.waiting.size, 0, '오류로 끝난 요청이 대기 목록에 남았다')
})

test('소켓이 닫히면 **기다리던 것을 전부** 깨운다', FAST, async () => {
  const ws = new FakeWS()
  const cdp = new CDP(ws)
  // 타임아웃을 길게 준다 — 깨우는 것이 close 인지 타임아웃인지 구분하려면
  // 타임아웃이 이 시험 안에서 발화하지 않아야 한다.
  const waiting = [
    cdp.send('A', {}, 's', 600000),
    cdp.send('B', {}, 's', 600000),
    cdp.send('C', {}, 's', 600000),
  ]
  ws.emit('close', {})
  const results = await Promise.allSettled(waiting)
  assert.equal(results.filter((r) => r.status === 'rejected').length, 3,
    '연결이 끊겼는데 매달린 요청이 있다')
  for (const r of results) assert.match(r.reason.message, /연결이 끊겼다: close/)
  assert.equal(cdp.waiting.size, 0, '끊긴 뒤에도 대기 목록이 남았다')
})

test('소켓 오류도 같은 자리에서 깨운다', FAST, async () => {
  const ws = new FakeWS()
  const cdp = new CDP(ws)
  const p = cdp.send('A', {}, 's', 600000)
  ws.emit('error', {})
  await assert.rejects(p, /연결이 끊겼다: error/)
})

test('끊긴 뒤에도 타이머가 남지 않는다', FAST, async () => {
  const ws = new FakeWS()
  const cdp = new CDP(ws)
  const before = timers()
  const p = cdp.send('A', {}, 's', 600000)
  ws.emit('close', {})
  await assert.rejects(p)
  assert.equal(timers(), before, '끊긴 요청의 타이머가 남았다 — 10분간 프로세스를 붙잡는다')
})

test('**`once()` 대기자도** 연결이 끊기면 깨어난다', FAST, async () => {
  const ws = new FakeWS()
  const cdp = new CDP(ws)
  // `once` 는 `waiting` 이 아니라 `listeners` 에 있다. 요청만 깨우면 이쪽은
  // 그대로 매달리고, 호출부가 레이스 타임아웃을 걸어 둔 덕에 「지금은」
  // 괜찮은 상태로 남는다 — 그 방어를 잊은 다음 호출부에서 되살아난다.
  const loaded = cdp.once('Page.loadEventFired', 's')
  ws.emit('close', {})
  await assert.rejects(loaded, /연결이 끊겼다: close/)
  assert.equal(cdp.listeners.length, 0,
    '끊긴 뒤에도 죽은 리스너가 남았다 — 배열에 영구히 쌓인다')
})

test('끊긴 뒤의 `send()`·`once()`는 **기다리지 않고** 바로 실패한다', FAST, async () => {
  const ws = new FakeWS()
  const cdp = new CDP(ws)
  ws.emit('error', {})

  const before = timers()
  // 「무응답 60초」로 끝나면 읽는 사람은 브라우저가 느린 줄 안다. 소켓이 죽은
  // 것과는 다른 진단이고, 60초를 기다릴 이유도 없다.
  await assert.rejects(cdp.send('A', {}, 's'), /연결이 끊겼다: error/)
  await assert.rejects(cdp.once('Page.loadEventFired', 's'), /연결이 끊겼다: error/)
  assert.equal(timers(), before, '끊긴 뒤의 요청이 새 타이머를 걸었다')
})

test('정상 흐름에서는 `once()`가 이벤트로 풀리고 리스너가 빠진다', FAST, async () => {
  const ws = new FakeWS()
  const cdp = new CDP(ws)
  const loaded = cdp.once('Page.loadEventFired', 's')
  assert.equal(cdp.listeners.length, 1)
  ws.emit('message', {
    data: JSON.stringify({ method: 'Page.loadEventFired', sessionId: 's', params: { t: 1 } }),
  })
  assert.deepEqual(await loaded, { t: 1 })
  assert.equal(cdp.listeners.length, 0, '이벤트로 풀린 리스너가 남았다')
})
