// 브라우저를 띄우고 CDP 로 몬다 — `make ui` 와 `make shots` 가 **같은 것**을 쓴다.
//
// 두 벌로 두면 한쪽만 고쳐지고, 그러면 「찍은 화면」과 「잰 화면」이 서로 다른
// 사이트가 된다. 브라우저 정리(고아 프로세스)도 한 곳에만 있어야 한다.
//
// **의존성을 늘리지 않는다.** Playwright 가 받아 둔 Chromium 을 CDP 로 직접
// 몰고, WebSocket 은 Node 22 내장을 쓴다 — npm 설치가 없다.
import { spawn } from 'node:child_process'
import { mkdtempSync, readdirSync, existsSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

// 좁은 폭·태블릿·데스크톱. 좁은 쪽이 결함을 가장 많이 드러낸다.
export const WIDTHS = [375, 768, 1280]

// Playwright 가 받아 둔 브라우저를 찾는다. 이름과 배치가 버전마다 달라서
// (`Chromium.app` 과 `Google Chrome for Testing.app`, arm64 와 x64) 후보를
// 훑는다 — 하나로 박아 두면 다음 버전에서 조용히 「없다」가 된다.
// **캐시 위치는 OS 마다 다르다.** macOS 경로 하나만 보던 판은 Linux 에서 늘
// null 을 돌려줬고, 그래서 이 감사는 CI 에 올릴 수 없었다 (D85 GAP-06 이 그
// 상태였다). 같은 부류로 이미 두 번 당했다: PATH 로 docker 를 가리려던 검사도,
// 릴리즈 산출물 실행 검증도 개발 기계의 배치를 전제했다.
//
// `PLAYWRIGHT_BROWSERS_PATH` 를 먼저 본다 — 설치할 때 그것으로 옮길 수 있고,
// CI 는 캐시를 그렇게 고정한다.
export function findChrome() {
  const home = process.env.HOME || ''
  const roots = [
    process.env.PLAYWRIGHT_BROWSERS_PATH,
    join(home, 'Library/Caches/ms-playwright'), // macOS
    join(home, '.cache/ms-playwright'), // Linux
    join(home, 'AppData/Local/ms-playwright'), // Windows
  ].filter((r) => r && existsSync(r))

  // **아키텍처 디렉터리 이름을 열거하지 않는다.** 목록으로 두었더니
  // `chrome-linux` 는 있고 `chrome-linux64` 는 없어서 CI 에서 못 찾았다 —
  // Playwright 가 이름을 바꾸면 조용히 「없다」가 되는 자리다. 대신 그 안을
  // 훑는다: `chromium-NNNN` 아래의 디렉터리는 어차피 그 하나뿐이다.
  const apps = ['Google Chrome for Testing', 'Chromium']
  // **못 찾았을 때 어디를 봤는지 말한다.** "찾지 못했다" 만 남기면 CI 로그를
  // 보고도 고칠 수 없다 — 캐시가 비었는지, 경로를 안 봤는지, 그 안의 배치가
  // 다른지가 구분되지 않는다. 실제로 그 상태로 한 번 돌려 보고 알았다.
  searched.length = 0
  for (const root of roots) {
    let dirs = []
    try {
      dirs = readdirSync(root)
    } catch (e) {
      searched.push(root + ' — 읽지 못함: ' + e.code)
      continue
    }
    const chromiums = dirs
      .filter((d) => d.startsWith('chromium-'))
      .sort((a, b) => Number(b.split('-')[1]) - Number(a.split('-')[1]))
    if (chromiums.length === 0) {
      searched.push(root + ' — chromium-* 없음 (있는 것: ' + dirs.join(', ') + ')')
      continue
    }
    for (const d of chromiums) {
      let inner = []
      try {
        inner = readdirSync(join(root, d))
      } catch { /* 아래에서 배치를 적는다 */ }
      // `chrome-*` 로 시작하는 디렉터리를 전부 본다 — mac·linux·linux64·win64
      // 무엇이든 같은 규칙으로 걸린다.
      for (const arch of inner.filter((n) => n.startsWith('chrome-'))) {
        for (const app of apps) {
          const mac = join(root, d, arch, app + '.app/Contents/MacOS/' + app)
          if (existsSync(mac)) return mac
        }
        for (const exe of ['chrome', 'chrome.exe', 'headless_shell']) {
          const p = join(root, d, arch, exe)
          if (existsSync(p)) return p
        }
      }
      searched.push(join(root, d) + ' — 아는 배치가 없음 (안에 있는 것: ' + inner.join(', ') + ')')
    }
  }
  return null
}

// findChrome 이 마지막으로 훑은 자리들. 못 찾았을 때 부르는 쪽이 그대로 찍는다.
export const searched = []

// ---- CDP ------------------------------------------------------------------

export class CDP {
  constructor(ws) {
    this.ws = ws
    this.id = 0
    this.waiting = new Map()
    this.listeners = []
    // 끊긴 뒤라는 표시. 값은 끊긴 이유다.
    this.dead = null
    ws.addEventListener('message', (e) => {
      const msg = JSON.parse(e.data)
      if (msg.id && this.waiting.has(msg.id)) {
        const { resolve, reject } = this.waiting.get(msg.id)
        this.waiting.delete(msg.id)
        msg.error ? reject(new Error(JSON.stringify(msg.error))) : resolve(msg.result)
        return
      }
      for (const l of this.listeners) l(msg)
    })
    // **연결이 끊기면 기다리던 것을 전부 깨운다.** 소켓이 닫혀도 대기 중인
    // 프라미스는 아무도 풀어 주지 않아 그대로 매달린다.
    //
    // 「전부」에는 `once()` 대기자도 든다. 그쪽은 `waiting` 이 아니라 `listeners`
    // 에 있어서, 요청만 깨우면 이벤트를 기다리던 프라미스는 그대로 매달리고
    // 죽은 클로저가 배열에 영구히 쌓인다 — 호출부가 스스로 레이스 타임아웃을
    // 걸어 두어 지금은 드러나지 않을 뿐이고, 그 방어를 잊은 다음 호출부에서
    // 같은 무한 대기가 조용히 되살아난다.
    const abort = (why) => {
      const err = () => new Error('CDP 연결이 끊겼다: ' + why)
      // **다시 보내지 못하게 먼저 표시한다.** 이 뒤의 send 는 60초를 기다린 뒤
      // 「무응답」이라고 말하는데, 응답이 없는 것이 아니라 소켓이 이미 죽었다.
      this.dead ??= why
      for (const [id, { reject }] of this.waiting) {
        this.waiting.delete(id)
        reject(err())
      }
      const pending = this.listeners
      this.listeners = []
      for (const l of pending) l.abort?.(err())
    }
    ws.addEventListener('close', () => abort('close'))
    ws.addEventListener('error', () => abort('error'))
  }

  // **답이 안 오면 기다리지 않고 죽는다.**
  //
  // 타임아웃이 없으면 브라우저가 응답을 멈추는 순간(렌더러 정지·소켓 유실)
  // 프라미스가 영영 풀리지 않고 게이트가 그대로 멈춘다 — 실측으로 감사 하나가
  // CPU 0% 로 한 시간을 매달려 있었고, 로그는 마지막 줄에서 멈춘 채였다.
  // **멈추는 게이트는 실패하는 게이트보다 나쁘다**: 신호를 주지 않으면서 CI
  // 잡의 시간을 통째로 쓴다.
  //
  // 값은 넉넉하다 — 느린 화면을 오탐으로 죽이는 것이 목적이 아니라, 영원히
  // 매달리는 것을 막는 것이 목적이다.
  send(method, params = {}, sessionId, timeoutMs = 60000) {
    // 이미 끊긴 뒤라면 기다릴 것이 없다. 60초를 세고 「무응답」이라고 말하면
    // 읽는 사람은 브라우저가 느린 줄 안다 — 소켓이 죽은 것과는 다른 진단이다.
    if (this.dead) {
      return Promise.reject(new Error('CDP 연결이 끊겼다: ' + this.dead))
    }
    const id = ++this.id
    return new Promise((resolve, reject) => {
      const t = setTimeout(() => {
        this.waiting.delete(id)
        reject(new Error(`CDP 무응답 ${timeoutMs}ms: ${method}`))
      }, timeoutMs)
      const done = (fn) => (v) => {
        clearTimeout(t)
        fn(v)
      }
      this.waiting.set(id, { resolve: done(resolve), reject: done(reject) })
      this.ws.send(JSON.stringify({ id, method, params, sessionId }))
    })
  }
  // 이벤트 하나를 기다린다. **연결이 끊기면 이쪽도 깨어난다** — 그러지 않으면
  // 이 프라미스는 영원히 매달리고, 호출부가 레이스 타임아웃을 걸어 둔 덕에
  // 「지금은」 괜찮은 상태로 남는다.
  once(method, sessionId) {
    if (this.dead) {
      return Promise.reject(new Error('CDP 연결이 끊겼다: ' + this.dead))
    }
    return new Promise((resolve, reject) => {
      const l = (msg) => {
        if (msg.method === method && (!sessionId || msg.sessionId === sessionId)) {
          this.listeners = this.listeners.filter((x) => x !== l)
          resolve(msg.params)
        }
      }
      l.abort = reject
      this.listeners.push(l)
    })
  }
}

// **브라우저는 이 프로세스와 함께 죽어야 한다.**
//
// 감사가 중단되면(타임아웃·Ctrl-C·검사 실패) 브라우저는 부모를 잃고 살아남는다.
// 몇 번 반복하면 Chrome 프로세스가 수십 개 쌓여 서로 자원을 다투고, 그 다음
// 실행은 「그냥 느린 것」처럼 보인다 — 실제로 72개까지 쌓여 감사가 40분을
// 넘겼다. 어떻게 끝나든 반드시 정리한다.
function killOnExit(proc) {
  // `detached: true` 로 띄웠으므로 자식은 자기 프로세스 그룹의 리더다.
  // **그룹을 죽인다** — Chromium 은 렌더러·GPU·유틸리티로 갈라지고, 부모
  // 하나만 죽이면 그 자식들이 고아로 남는다. 실제로 SIGTERM 으로 부모만
  // 죽였을 때 28개가 살아남았다.
  const kill = () => {
    for (const target of [-proc.pid, proc.pid]) {
      try {
        process.kill(target, 'SIGKILL')
      } catch {}
    }
  }
  for (const sig of ['SIGINT', 'SIGTERM', 'SIGHUP']) {
    process.once(sig, () => {
      kill()
      process.exit(1)
    })
  }
  process.once('exit', kill)
  process.once('uncaughtException', (e) => {
    kill()
    console.error(e)
    process.exit(1)
  })
}

function spawnChrome(bin, userDataDir) {
  const proc = spawn(bin, [
    '--headless=new',
    '--remote-debugging-port=0',
    `--user-data-dir=${userDataDir}`,
    '--no-first-run',
    '--no-default-browser-check',
    '--disable-gpu',
    '--hide-scrollbars',
  ], { detached: true })
  killOnExit(proc)
  return new Promise((resolve, reject) => {
    let buf = ''
    const t = setTimeout(() => reject(new Error('브라우저가 뜨지 않았다:\n' + buf)), 30000)
    proc.stderr.on('data', (d) => {
      buf += d
      const m = buf.match(/ws:\/\/\S+/)
      if (m) {
        clearTimeout(t)
        resolve({ proc, wsURL: m[0] })
      }
    })
    proc.on('exit', (c) => reject(new Error(`브라우저가 종료됐다 (code ${c}):\n` + buf)))
  })
}


// 브라우저를 띄우고 빈 탭에 붙은 CDP 세션까지 돌려준다.
export async function launch(bin) {
  const dir = mkdtempSync(join(tmpdir(), 'ondolith-browser-'))
  process.once('exit', () => {
    try { rmSync(dir, { recursive: true, force: true, maxRetries: 3 }) } catch {}
  })
  const { wsURL } = await spawnChrome(bin, dir)
  const ws = new WebSocket(wsURL)
  await new Promise((r) => ws.addEventListener('open', r))
  const cdp = new CDP(ws)
  const { targetId } = await cdp.send('Target.createTarget', { url: 'about:blank' })
  const { sessionId } = await cdp.send('Target.attachToTarget', { targetId, flatten: true })
  await cdp.send('Page.enable', {}, sessionId)
  await cdp.send('Runtime.enable', {}, sessionId)
  return { cdp, sessionId }
}
