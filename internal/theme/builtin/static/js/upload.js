// 관리자 파일 칸 끌어다 놓기 (FR-721).
//
// 칸이 「파일 선택」 단추만큼 좁고, 칸 밖에 놓으면 브라우저가 그림을 열어 쓰던 폼이 날아갔다. 그래서
//   - 칸을 둘러싼 영역(data-drop-zone)이 통째로 놓는 곳이 되고,
//   - 파일을 끄는 동안은 페이지 어디에 놓아도 브라우저가 파일을 열지 않으며,
//   - 고른 파일 이름과 사진 미리보기를 보여 준다.
// **덧붙임이다.** 스크립트가 없어도 칸은 그대로 눌러 고르고 올린다. 받을 형식(accept)과 여러 장(multiple)은
// 칸의 규칙을 따르고, 서버는 지금처럼 다시 검사한다 — 여기서 거르는 것은 사람이 바로 알게 하려는 것이다.
(() => {
  const inputs = [...document.querySelectorAll('input[type=file][data-drop]')]
  if (!inputs.length) return

  const carriesFiles = (e) => [...(e.dataTransfer?.types || [])].includes('Files')
  // 칸 밖에 놓아도 페이지가 바뀌지 않는다. 파일을 끌 때만 막는다 — 글자 끌기는 그대로 둔다.
  for (const type of ['dragover', 'drop']) {
    window.addEventListener(type, (e) => { if (carriesFiles(e)) e.preventDefault() })
  }

  // accept 는 「.zip」 같은 확장자, 「image/*」, 「image/png」 를 쉼표로 섞어 쓴다 (HTML 규칙과 같다).
  const accepts = (input, file) => {
    const rules = (input.accept || '').split(',').map((s) => s.trim().toLowerCase()).filter(Boolean)
    if (!rules.length) return true
    const name = file.name.toLowerCase()
    const type = (file.type || '').toLowerCase()
    return rules.some((r) => r.startsWith('.') ? name.endsWith(r)
      : r.endsWith('/*') ? type.startsWith(r.slice(0, -1)) : type === r)
  }
  const size = (n) => n >= 1 << 20 ? (n / (1 << 20)).toFixed(1) + 'MB' : Math.max(1, Math.round(n / 1024)) + 'KB'

  for (const input of inputs) {
    const zone = input.closest('[data-drop-zone]') || input.parentElement
    zone.classList.add('adm-drop')
    const hint = document.createElement('span')
    hint.className = 'adm-drop-hint'
    hint.textContent = input.multiple ? '파일을 여기에 끌어다 놓아도 됩니다 (여러 개 가능)' : '파일을 여기에 끌어다 놓아도 됩니다'
    const note = document.createElement('div')
    note.className = 'adm-drop-note'
    note.setAttribute('aria-live', 'polite')
    input.after(hint, note)

    let previews = []
    const show = () => {
      for (const url of previews) URL.revokeObjectURL(url)
      previews = []
      note.replaceChildren()
      zone.classList.remove('adm-drop--refused')
      for (const f of input.files) {
        const item = document.createElement('span')
        item.className = 'adm-drop-file'
        if (f.type.startsWith('image/')) {
          const img = document.createElement('img')
          const url = URL.createObjectURL(f)
          previews.push(url)
          img.src = url
          img.alt = ''
          img.width = 64
          img.height = 64
          item.append(img)
        }
        item.append(document.createTextNode(`${f.name} (${size(f.size)})`))
        note.append(item)
      }
    }
    input.addEventListener('change', show)

    // dragenter·dragleave 는 안쪽 요소를 지날 때마다 오간다 — 깊이를 세어 영역을 실제로 떠날 때만 끈다.
    let depth = 0
    zone.addEventListener('dragenter', (e) => {
      if (!carriesFiles(e)) return
      depth++
      zone.classList.add('adm-drop--over')
    })
    zone.addEventListener('dragleave', () => {
      if (--depth <= 0) {
        depth = 0
        zone.classList.remove('adm-drop--over')
      }
    })
    zone.addEventListener('drop', (e) => {
      if (!carriesFiles(e)) return
      e.preventDefault()
      depth = 0
      zone.classList.remove('adm-drop--over')
      const files = [...e.dataTransfer.files].filter((f) => accepts(input, f))
      if (!files.length) {
        // 고른 것은 그대로 둔다 — 잘못 놓은 한 번이 앞서 고른 파일을 지우면 안 된다.
        zone.classList.add('adm-drop--refused')
        note.replaceChildren(document.createTextNode('이 칸에 넣을 수 없는 파일입니다' +
          (input.accept ? ` (${input.accept})` : '') + '.'))
        return
      }
      const dt = new DataTransfer()
      for (const f of input.multiple ? files : files.slice(0, 1)) dt.items.add(f)
      input.files = dt.files
      input.dispatchEvent(new Event('change', { bubbles: true }))
    })
  }
})()
