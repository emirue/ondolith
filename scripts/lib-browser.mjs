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
export function findChrome() {
  const root = join(process.env.HOME, 'Library/Caches/ms-playwright')
  if (!existsSync(root)) return null
  const dirs = readdirSync(root)
    .filter((d) => d.startsWith('chromium-'))
    .sort((a, b) => Number(b.split('-')[1]) - Number(a.split('-')[1]))
  const apps = ['Google Chrome for Testing', 'Chromium']
  const arches = ['chrome-mac-arm64', 'chrome-mac', 'chrome-linux']
  for (const d of dirs) {
    for (const arch of arches) {
      for (const app of apps) {
        const p = join(root, d, arch, app + '.app/Contents/MacOS/' + app)
        if (existsSync(p)) return p
      }
      const linux = join(root, d, arch, 'chrome')
      if (existsSync(linux)) return linux
    }
  }
  return null
}

// ---- CDP ------------------------------------------------------------------

export class CDP {
  constructor(ws) {
    this.ws = ws
    this.id = 0
    this.waiting = new Map()
    this.listeners = []
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
  }
  send(method, params = {}, sessionId) {
    const id = ++this.id
    return new Promise((resolve, reject) => {
      this.waiting.set(id, { resolve, reject })
      this.ws.send(JSON.stringify({ id, method, params, sessionId }))
    })
  }
  once(method, sessionId) {
    return new Promise((resolve) => {
      const l = (msg) => {
        if (msg.method === method && (!sessionId || msg.sessionId === sessionId)) {
          this.listeners = this.listeners.filter((x) => x !== l)
          resolve(msg.params)
        }
      }
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
