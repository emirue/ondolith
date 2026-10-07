// 리치 텍스트 에디터 붙이기 (FR-513, D17 「본문 그리기」).
//
// `<textarea data-editor data-upload="…">` 를 SunEditor 로 바꾼다. 제출할 때 HTML 은
// 같은 폼의 `<input name="body_html">` 에, 그 글자는 원래 textarea(`body`)에 싣는다.
// **JS 가 없으면 아무 일도 일어나지 않는다** — textarea 가 그대로 남아 평문으로 저장된다.
// 서버는 body_html 을 허용목록으로 정화한다(D60) — 여기서 거르는 것은 없다.
(function () {
  'use strict';

  function escapeHTML(s) {
    return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
  }

  // 평문 글을 처음 에디터로 고칠 때: 줄마다 문단으로. 꺾쇠는 글자 그대로다.
  function textToHTML(text) {
    var lines = text.replace(/\r\n/g, '\n').split('\n');
    var out = lines.map(function (l) { return l === '' ? '<p><br></p>' : '<p>' + escapeHTML(l) + '</p>'; });
    return out.join('');
  }

  function plainText(html) {
    var d = document.createElement('div');
    d.innerHTML = html;
    return d.textContent || '';
  }

  function attach(ta) {
    var form = ta.form;
    var htmlInput = form && form.querySelector('input[name="body_html"]');
    if (!form || !htmlInput || typeof SUNEDITOR === 'undefined') return;

    var P = SUNEDITOR.plugins;
    var editor = SUNEDITOR.create(ta, {
      lang: window.SUNEDITOR_LANG && SUNEDITOR_LANG.ko,
      // 운영자가 쓰는 것만 — PDF·수식·그리기·파일 탐색기는 뺀다.
      plugins: [P.align, P.fontSize, P.fontColor, P.backgroundColor, P.list, P.blockStyle,
        P.blockquote, P.hr, P.table, P.link, P.image, P.video, P.embed],
      buttonList: [
        ['undo', 'redo'],
        ['blockStyle', 'fontSize'],
        ['bold', 'underline', 'italic', 'strike', 'fontColor', 'backgroundColor', 'removeFormat'],
        ['align', 'list', 'blockquote', 'hr', 'table'],
        ['link', 'image', 'video', 'embed'],
      ],
      value: htmlInput.value || textToHTML(ta.value),
      width: '100%',
      minHeight: '360px',
      // 사진은 서버로 올린다. 주소가 없으면 에디터가 base64 로 본문에 박는다 —
      // 서버는 data: 주소를 받지 않는다(D60).
      image: { uploadUrl: ta.getAttribute('data-upload') },
    });

    // 브라우저 시험(scripts/editor-ime.mjs)이 인스턴스에 닿는 자리다. 쓰지 않으면 해가 없다.
    ta.sunEditor = editor;

    form.addEventListener('submit', function () {
      var html = editor.$.html.get();
      htmlInput.value = html;
      ta.value = plainText(html);
    });
  }

  document.querySelectorAll('textarea[data-editor]').forEach(attach);
})();
