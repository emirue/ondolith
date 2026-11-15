// 관리자 화면 공통 스크립트. admin.css 처럼 실행 파일에 들어 있고 layout 이 인라인한다 — 테마의 /static/ 은 활성
// 테마가 덮어쓸 수 있어, 관리자 화면의 동작을 남의 파일이 정하게 된다 (adminRenderer 주석).
//
// **기다리는 동안 다시 눌러도 한 번만 보낸다** (FR-727, D19 C13). 응답을 기다리다 단추를 또 누르면 브라우저는 앞
// 요청을 끊고 같은 것을 다시 보낸다 — 백업 내려받기에서는 끊긴 앞 요청이 잡고 있던 잠금에 뒤 요청이 걸려 「다른 백업이
// 진행 중」으로 끝날 수 있었다. 보낸 폼을 기억해 두고 그 폼의 다음 제출은 막는다.
//   - 단추를 disabled 로 만들지 않는다. 제출하는 중에 끄면 그 단추의 name=value 가 폼 데이터에서 빠진다
//   - 확인 창을 취소했거나 필수 칸이 비었으면 submit 이 일어나지 않아 잠기지 않는다. onsubmit 이 막은 제출도 건너뛴다
//   - GET 폼(조회·필터)은 잠그지 않는다. 다시 보내도 바뀌는 것이 없다
//   - 내려받기 폼(data-download)은 화면이 넘어가지 않는다. 서버가 파일을 보내기 직전에 돌려주는 쿠키를 보고 푼다
//   - 뒤로 가기로 얼어 있던 화면이 돌아오면(bfcache) 푼다
(() => {
  const COOKIE = 'ondolith_download'
  const held = new Map() // 폼 → { button, kids }

  const release = (f) => {
    const h = held.get(f)
    held.delete(f)
    f.removeAttribute('aria-busy')
    if (h?.button) {
      h.button.replaceChildren(...h.kids)
      h.button.removeAttribute('aria-disabled')
    }
  }

  // 표는 서버가 그대로 돌려주는 난수다. crypto.randomUUID 는 HTTPS 에서만 있어 IP 로 여는 서버에서 없다.
  const token = () => [...crypto.getRandomValues(new Uint8Array(16))].map((x) => x.toString(16).padStart(2, '0')).join('')

  const watchDownload = (f) => {
    const t = token()
    let input = f.querySelector('input[name="download_token"]')
    if (!input) {
      input = Object.assign(document.createElement('input'), { type: 'hidden', name: 'download_token' })
      f.append(input)
    }
    input.value = t // 폼 데이터는 submit 처리가 끝난 뒤에 모이므로 이번 제출에 실린다
    const timer = setInterval(() => {
      if (!document.cookie.split('; ').includes(COOKIE + '=' + t)) return
      clearInterval(timer)
      document.cookie = COOKIE + '=; Max-Age=0; Path=/admin'
      release(f)
    }, 250)
  }

  document.addEventListener('submit', (e) => {
    const f = e.target
    if (f.method !== 'post') return
    if (held.has(f)) {
      e.preventDefault()
      return
    }
    if (e.defaultPrevented) return
    const b = e.submitter
    held.set(f, { button: b, kids: b ? [...b.childNodes] : [] })
    f.setAttribute('aria-busy', 'true')
    if (b) {
      b.replaceChildren(b.dataset.busyLabel || '처리 중…')
      b.setAttribute('aria-disabled', 'true')
    }
    if ('download' in f.dataset) watchDownload(f)
  })

  addEventListener('pageshow', (e) => {
    if (e.persisted) for (const f of [...held.keys()]) release(f)
  })
})()
