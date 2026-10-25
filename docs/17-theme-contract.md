# D17. 테마 계약

**테마 개발자가 읽는 유일한 문서다.** Go를 몰라도 이 문서만으로 테마를 만들 수 있어야 한다.
테마 시스템의 설계 근거는 [D40](40-theme.md), 화면 목록은 [D11](11-screens.md).

## 계약의 요지

| # | 규칙 |
|---|---|
| 1 | 템플릿은 `html/template` 문법이다. 코드 생성·빌드 단계가 없다 ([DEC-3.1](../.ai/DECISIONS.md)) |
| 2 | **필수 파일은 `base.html` 하나다.** 나머지는 없으면 내장 테마가 대신 그린다 (FR-308) |
| 3 | 템플릿은 **핸들러가 준비한 데이터만** 받는다. DB·파일·HTTP에 접근할 수 없다 (FR-305) |
| 4 | 출력은 기본 이스케이프된다. `template.HTML`은 코어만 쓴다 |
| 5 | 정적 자산은 테마의 `static/` 아래 두면 `/static/...`으로 서빙된다 (FR-309, P-906) |

> **폴백이 있으므로 테마를 점진적으로 만들 수 있다.** `base.html`과 `home.html`만 바꾼 테마도
> 동작한다. 나머지는 내장 테마 모습으로 나온다.

### 호환성

**이 문서에 적힌 것이 계약이고, 코어는 그것을 깨지 않는 쪽으로만 바꾼다.** 화면에 새 데이터 키나
새 선택 템플릿·조각을 더하는 것은 호환 변경이다 — 쓰지 않는 테마는 그대로 동작하고, 없는 조각은
내장 테마의 것이 그린다(규칙 2). 2026-10-07 의 `partials/pager.html` 이 그 예다. 키·템플릿·함수의
이름이나 뜻을 바꾸거나 지우는 것, 필수 파일을 늘리는 것은 **깨는 변경**이고 [CHANGELOG](../CHANGELOG.md)에
`⚠️ 파괴적 변경`으로 적는다. 이 문서에 없는 키(핸들러가 우연히 넘기는 값)에 기대지 않는다 — 그것은
예고 없이 바뀐다. 계약 버전 번호는 두지 않는다: 폴백이 있어 테마가 지금 코어를 모를 수는 있어도
코어가 테마를 거부할 이유가 없다.

### 폼

필드 이름과 검증은 화면별로 [D19](19-screen-io.md)에 있다. 하나만 여기 적는다: **HTML 폼은 PATCH·
DELETE 를 보낼 수 없어서 장바구니(`/cart/items/…`)의 수량 변경·삭제는 POST 에 `_method=PATCH`·
`_method=DELETE` 를 실어 보낸다.** 서버는 그 경로의 POST 에서만 이 값을 읽는다 — 다른 경로에 실으면
무시된다. 내장 테마의 `shop/cart.html` 이 본보기다.

**글쓰기 에디터(FR-513).** 글쓰기·글수정 폼(`board/form.html`)이 에디터를 쓰려면 본문 칸을
`<textarea name="body" data-editor data-upload="/board/{{.Board.Slug}}/media">` 로 두고, 같은 폼에
`<input type="hidden" name="body_html" value="{{.Post.BodyHTML}}">` 을 두고, 내장 자산
`vendor/suneditor/suneditor.min.css`·`suneditor.min.js`·`ko.js` 와 `js/editor.js` 를 `{{asset …}}` 으로 건다
(내장 테마가 본보기다 — 사용자 테마에 그 파일이 없으면 내장 것이 나간다). 셋 중 하나라도 빠지면 그 테마는
**평문 칸으로 동작한다** — 깨지지 않는다.

**지도 블록(FR-716).** 본문의 지도는 `<img src="/static/img/map.svg" data-map="kakao|naver|google" data-lat data-lng
data-zoom data-label>` 자리표시다. 내장 `partials/head.html` 이 A-201 의 지도 키가 있을 때 `js/maps.js` 를 키와 함께
싣고(`<script … data-naver data-kakao data-google data-google-map-id>`), 그 스크립트가 블록을 그 지도사 SDK 의 지도로
바꾸며 에디터에 「지도 넣기」 를 붙인다(`editor.js` 보다 먼저 와야 한다). `<head>` 를 덮어쓴 테마는 그 블록을 옮겨
적어야 지도가 그려진다 — 빠뜨려도 깨지지 않고 자리표시 그림이 남는다.

**스팸 방지(FR-216).** 가입(`auth/signup.html`)·재설정 요청(`auth/password-reset-request.html`)·새 글
(`board/form.html`)·새 댓글(`partials/comment-form.html`) 폼 안에 `{{template "partials/spam.html" .Site.Turnstile}}`
을 둔다. 숨은 칸(`website-url`)과, A-201 에 Turnstile 키가 있으면 위젯과 Cloudflare 스크립트를 그린다.
**키가 켜졌는데 이 줄이 없는 테마는 그 폼의 제출이 전부 거부된다**(토큰이 오지 않는다) — 사용자 테마가 위 폼을
덮어쓴다면 이 줄을 옮겨 적는다. 숨은 칸만 빠뜨린 테마는 거부되지 않는다(빈 칸과 같다).

## 디렉터리 구조

```
themes/{이름}/
  theme.json          메타데이터 (아래)
  base.html           레이아웃 — 유일한 필수 파일
  partials/           조각
  auth/ account/ board/ comment/ shop/ order/
  static/             CSS·JS·이미지 → /static/...
```

`theme.json`

| 키 | 뜻 |
|---|---|
| `name` | 표시 이름 |
| `version` | 테마 버전 |
| `author` | 제작자 |
| `requires` | 이 테마가 요구하는 Ondolith 최소 버전 |
| `logo` | 로고 이미지 — `static/` 아래 경로(예: `img/logo.svg`). svg·png·jpg·jpeg·webp·gif (FR-310) |
| `favicon` | 파비콘 — `static/` 아래 경로(예: `img/favicon.png`). ico·png·svg (FR-310) |

**`logo`·`favicon`** (FR-310). 지정하면 그 테마가 활성인 동안 **관리자 화면** 머리글의 마크와 파비콘이 그것으로
바뀌고, 뷰 모델의 `.Site.Logo`·`.Site.Favicon` 에 자산 주소(해시가 붙은 `/static/…`)로 온다 — 내장
`partials/header.html` 은 로고를 이름 앞에, 내장 `partials/head.html` 은 파비콘을 싣는다. 머리글·`<head>` 를
덮어쓴 테마는 그 값을 직접 쓴다. 지정이 없거나 틀리면(`static/` 밖·이미지가 아님·파일 없음) 빈 값이고 기본
마크·아이콘이 나오며, 활성화(A-202)가 경고한다(거부하지 않는다). **로고는 `<img>` 로만 그린다** — 남이 만든
테마의 SVG 를 문서에 끼우면 그 안의 스크립트가 사이트 출처로 돈다.

**`requires` 비교 규칙**

- `major.minor.patch` 숫자 비교만 한다. 프리릴리즈·빌드 메타데이터 규칙은 넣지 않는다 —
  테마 호환성 판단에 그 정밀도가 필요하지 않다
- 현재 버전이 `requires`보다 낮으면 **활성화를 거부한다** (A-202). 활성화한 뒤 깨지는 것보다
  낫다
- 개발 빌드(`ondolith -version`이 `dev`로 시작)는 **비교를 건너뛰고 경고만** 한다.
  개발 중에 테마를 못 쓰면 곤란하다
- `requires`가 없으면 제한 없음으로 본다

**정적 자산 캐시 무효화**

`asset` 함수는 **파일 내용의 해시**를 쿼리 문자열로 붙인다. 테마 버전을 쓰지 않는 이유는
**버전을 올리지 않고 파일만 고치는 것이 가장 흔한 작업**이기 때문이다 — 그때 캐시가 안 깨지면
"왜 안 바뀌지"로 시간을 버린다.

- 운영 모드: 기동 시 한 번 계산해 캐시 (NFR-104)
- 개발 모드: 매 요청 계산 (FR-306)

---

## 구조 약속

**`base.html` 은 두 개의 띠를 이 클래스로 낸다.**

| 요소 | 클래스 | 왜 |
|---|---|---|
| 상단 띠 | `header.site-header` | |
| 하단 띠 | `footer.site-footer` | |

머리·본문·바닥이 한 축에 서는지를 `make ui`가 이 두 선택자로 잰다 — 못 찾으면
그만큼 규칙이 덜 재고, 조용히 통과하면 아무도 그것을 모른다. **도구가 전제하는
것을 계약이 약속하지 않으면 그 전제는 언제든 깨진다** — 실제로 이 두 클래스는
`ui-audit.mjs`만 알고 있었고 계약 어디에도 없었다.

테마를 활성화할 때 `base.html`에 이 둘이 있는지 **코어가 대조한다.** 없으면
경고가 뜬다 — 거부하지 않는 이유는 규칙 2가 `base.html` 하나만 필수로 정했고,
그 계약을 검사가 조용히 좁히면 안 되기 때문이다.

---

## 템플릿 목록

`필수` = 없으면 사이트가 뜨지 않는다. `폴백` = 없으면 내장 테마가 대신 그린다.
`코어 생성` = 테마가 갈아끼울 수 없다.

`sitemap.xml`·`robots.txt` 가 코어 생성인 이유: FR-510 이 "발행된 페이지·글만"
을 요구하는데, 그 집합은 **익명 권한으로 질의한 결과**여야 한다 (크롤러는 로그인
하지 않는다). 템플릿에 넘기면 무엇을 넘길지가 곧 무엇이 새는지가 되고, 비공개
게시판·비밀글·초안이 테마 실수 하나로 색인된다.

### 레이아웃·조각

| 템플릿 | 화면 | 구분 | 내용 |
|---|---|---|---|
| `base.html` | (전체) | 필수 | 문서 뼈대. 다른 템플릿을 `{{block "content"}}`로 감싼다 |
| `partials/head.html` | (전체) | 폴백 | `<head>` 내용. 메타·OG 태그 (FR-511) |
| `partials/header.html` | (전체) | 폴백 | 상단. 메뉴(FR-405)와 로그인 상태 |
| `partials/footer.html` | (전체) | 폴백 | 하단. 사업자 정보(FR-711)가 여기 들어간다 |
| `partials/pagination.html` | (목록) | 폴백 | 페이지 이동 (FR-508). 전체 건수를 세는 게시판(P-203)·통합 검색(P-212). 데이터 `{Total, Capped, CapTotal, LastPage, Pages[{No, URL, Current}], FirstURL, PrevURL, NextURL, LastURL}` — 번호는 열 개씩, 빈 주소는 링크 없음, 주소에 정렬·검색어가 실려 있다. 검색은 맞는 글을 `CapTotal`(10,000)까지만 센다 — 넘으면 `Capped` 가 참이고 `Total` 은 `CapTotal`+1 이다(「10,000건 이상」으로 그린다). 앞 판의 `{Base, Query, PageNo, PrevPage, NextPage, HasPrev, HasNext}` 도 함께 들어온다 |
| `partials/pager.html` | P-301, P-302, P-305, P-501 | 폴백 | 「이전·다음」. 건수를 세지 않고 한 행 더 읽는 목록. 데이터 `{PageNo, PrevURL, NextURL}` — 빈 주소는 링크 없음 |
| `partials/flash.html` | (전체) | 폴백 | 성공·오류 메시지 |
| `partials/stock-badge.html` | P-301, P-302, P-305, P-201 | 폴백 | **상품의 판매 상태 배지** — 조합이 없으면 「판매 준비 중」, 재고가 없으면 「품절」(상품 화면과 같은 말). 데이터 상품 `{HasVariants, InStock}` |
| `partials/field.html` | P-203, P-204, P-205 | 폴백 | **커스텀 필드 하나를 그린다** (FR-503). 타입별 분기가 여기 있다. 데이터 `{Field, Value, Has}` — `Has "값"` 이 저장된 선택인지 답한다(여러 개 선택을 되살릴 때) |
| `partials/variant.html` | P-303, P-304 | 폴백 | 고른 옵션 조합의 가격·재고·hidden `variant_id` (FR-602). **상품 화면이 처음 그릴 때와 htmx 가 갈아 끼울 때가 같은 조각을 쓴다** — 두 벌이면 「고르기 전」과 「고른 뒤」가 갈라진다 |

### 콘텐츠

| 템플릿 | 화면 | 구분 |
|---|---|---|
| `home.html` | P-201 | 폴백 |
| `page.html` | P-202 | 폴백 |
| `search.html` | P-212 | 폴백 |
| `error.html` | P-903, P-904 | 폴백 · 데이터 `{Message, Detail}` — `Message` 는 화면이 주는 문구(없으면 일반 문구), `Detail` 은 개발 모드의 원인 |
| `sitemap.xml` | P-901 | **코어 생성** |
| `robots.txt` | P-902 | **코어 생성** |

### 인증·계정

| 템플릿 | 화면 | 구분 |
|---|---|---|
| `auth/login.html` | P-101 | 폴백 |
| `auth/signup.html` | P-103 | 폴백 |
| `auth/signup-sent.html` | P-103 | 폴백 |
| `auth/password-reset-request.html` | P-104 | 폴백 |
| `auth/password-reset.html` | P-105 | 폴백 |
| `auth/password-reset-sent.html` | P-104 | 폴백 |
| `auth/verify.html` | P-112 | 폴백 |
| `account/profile.html` | P-108 | 폴백 |
| `account/password.html` | P-109 | 폴백 |
| `account/delete.html` | P-110 | 폴백 |
| `account/connections.html` | P-111 | 폴백 |

### 게시판

| 템플릿 | 화면 | 구분 |
|---|---|---|
| `board/list.html` | P-203 | 폴백 |
| `board/view.html` | P-204 | 폴백 |
| `board/form.html` | P-205, P-206 | 폴백 |
| `comment/form.html` | P-209 | 폴백 |

### 상품 (커머스 모듈)

| 템플릿 | 화면 | 구분 |
|---|---|---|
| `shop/list.html` | P-301, P-302 | 폴백 |
| `shop/search.html` | P-305 | 폴백 |
| `shop/product.html` | P-303 | 폴백 |
| `shop/variant.html` | P-304 | 폴백 |
| `shop/cart.html` | P-402 | 폴백 |
| `shop/checkout.html` | P-405 | 폴백 |
| `shop/pay.html` | P-407 | 폴백 |
| `shop/complete.html` | P-410 | 폴백 |
| `shop/terms.html` | P-411 | 폴백 |

> **P-408 이 `fail.html` 을 쓰는 이유.** 승인에 성공하면 P-410 으로 리다이렉트하므로
> 자기 템플릿이 없다. 실패·만료·금액 불일치만 화면을 그리고, 그 화면이 P-409 와 같다 —
> 사용자가 보는 것도 할 일도 같기 때문이다.
| `shop/fail.html` | P-408, P-409 | 폴백 |

### 주문 (커머스 모듈)

| 템플릿 | 화면 | 구분 |
|---|---|---|
| `order/list.html` | P-501 | 폴백 |
| `order/view.html` | P-502 | 폴백 |
| `order/guest-form.html` | P-503 | 폴백 |
| `order/shipping.html` | P-505 | 폴백 |
| `order/refunds.html` | P-508 | 폴백 |
| `order/receipt.html` | P-509 | 폴백 |
| `order/return-form.html` | P-511, P-512 | 폴백 |
| `order/returns.html` | P-513 | 폴백 |
| `order/exchange-pay.html` | P-514 | 폴백 |

> `cms` 모드에서는 `shop/`·`order/` 템플릿이 쓰이지 않는다. 커머스 라우트가 등록되지
> 않기 때문이다 ([D11](11-screens.md) 모듈 구성). 테마에 있어도 무해하다.

### 템플릿이 없는 공개 화면

| 화면 | 이유 |
|---|---|
| P-001, P-002 | 설치 트리다. 테마가 로드되기 전이라 코어 템플릿을 쓴다 ([D20](20-architecture.md)) |
| P-107 | 리다이렉트만 한다 |
| P-211 | 파일을 그대로 내보낸다 |
| P-306 | 〃 — 상품 이미지 |
| P-213 | 〃 — 에디터로 올린 본문 사진 |
| P-906 | 정적 자산 자체다 |
| P-907 | 두 글자 평문이다. 로드밸런서가 읽으므로 테마가 관여할 것이 없고, 테마가 그린다면 테마 오류가 곧 인스턴스 장애로 보고된다 |

**POST·PATCH·DELETE 전용 화면은 템플릿이 없다.** 처리 후 리다이렉트하거나
`partials/`의 조각으로 응답한다(htmx).

---

## 함수맵 (FR-304)

코어가 제공한다. **여기 없는 것은 템플릿에서 쓸 수 없다.**

### URL

| 함수 | 뜻 |
|---|---|
| `url "page" .Slug` | 페이지 URL |
| `url "board" .Slug` | 게시판 목록 URL |
| `url "post" .BoardSlug .ID` | 글 URL |
| `url "product" .Slug` | 상품 URL (`/shop/p/{slug}`, P-303) |
| `url "order" .OrderNo` | 주문 URL (`/orders/{orderNo}`, P-502) |
| `asset "css/style.css"` | 테마 정적 자산 URL. **파일 내용 해시**를 붙인다 → `/static/css/style.css?v=a1b2c3d4` |

> **`isCurrent`·`can`을 함수로 두지 않는다.** 둘 다 요청마다 달라지는 값인데,
> 함수맵은 템플릿을 파싱할 때 묶이고 파싱 결과는 캐시된다 (NFR-104). 프로세스에
> 로더가 하나뿐인 이상 함수가 닫아 잡은 값은 **첫 요청의 것으로 고정**되거나,
> 요청마다 갈아끼우면 동시 요청끼리 경쟁한다. 조용히 틀린 값을 주는 함수는
> 없는 함수보다 나쁘다.
>
> 같은 정보가 뷰 모델에 이미 있다 — 아래 「뷰 모델 규약」의 `.Path` 와 `.Can` 이다.
>
> ```
> {{if eq $.Path .URL}}class="current"{{end}}
> {{if index $.Can "post.write"}}<a href="...">글쓰기</a>{{end}}
> ```

### 포맷

| 함수 | 뜻 |
|---|---|
| `date .T "2006-01-02"` | 날짜 포맷 |
| `dateAgo .T` | "3분 전" |
| `money .Amount` | 금액. **정수 minor unit을 받아 통화 표기로** ([D30](30-data-model.md)) |
| `number .N` | 천단위 구분 |
| `filesize .Bytes` | 파일 크기 |

### 문자열·구조

| 함수 | 뜻 |
|---|---|
| `truncate .S 100` | 자르기 |
| `nl2br .S` | 줄바꿈 → `<br>`. **이스케이프 후 변환한다** |
| `field .Post .Key` | 커스텀 필드 값을 **사람이 읽는 문자열로** (FR-503) — 숫자는 지수 표기 없이(쉼표 없음), 체크는 「예」(해제는 빈 값), 여러 개 선택은 「빨강, 파랑」 |
| `fields .Board` | 커스텀 필드 스키마 순회 (FR-503) |
| `pages .Pagination` | 표시할 페이지 번호 목록 |

### 함수맵에 넣지 않는 것

| 넣지 않는 것 | 이유 |
|---|---|
| DB 조회 | 테마 하나가 사이트를 무너뜨린다 (FR-305) |
| 파일 읽기·쓰기 | 〃 |
| HTTP 요청 | 〃 |
| 임의 코드 실행 | 〃 |
| `raw`/`safeHTML` | `template.HTML`은 코어만 쓴다. 테마가 이스케이프를 끌 수 있으면 저장형 XSS 경로가 열린다 (NFR-203) |

> **`.Can` 이 있다고 권한 검사가 되는 것이 아니다.** 버튼을 숨기는 용도이고, 라우트는
> 스스로 다시 검사한다. 테마 작성자가 `.Can` 을 빠뜨려도 뚫리지 않아야 한다.

---

## 본문 그리기 (FR-513)

글(`board/view.html` 의 `.Post`)과 페이지(`page.html` 의 `.Data`)는 **두 가지 본문**을 가진다.

| 이름 | 내용 |
|---|---|
| `.Rendered` | 에디터로 쓴 본문의 HTML(코어가 정화한 것). 평문 글이면 빈 값 |
| `.Body` | 평문. 에디터 이전의 글은 이것뿐이고, 에디터로 쓴 글이면 HTML 의 글자다 |

```
{{with .Post.Rendered}}<div class="page-body rich">{{.}}</div>{{else}}<div class="page-body">{{nl2br .Post.Body}}</div>{{end}}
```

`.Rendered` 를 쓰지 않는 테마도 깨지지 않는다 — 에디터로 쓴 글이 평문으로 보일 뿐이다.
본문 사진은 원본 폭이 클 수 있으니 테마가 `img { max-width: 100% }` 를 준다(내장 테마의 `.rich`).

## 갤러리 목록 (FR-515)

`board/list.html` 은 게시판의 `.Board.ListStyle`(`list`·`gallery`)로 모양을 고른다. 글마다
`.CoverMediaID` 가 본문 첫 사진의 id 이고(없으면 빈 값) 썸네일은 `/media/{id}/thumb` 다.

```
{{if and .CoverMediaID (not .IsSecret)}}<img src="/media/{{.CoverMediaID}}/thumb" alt="">{{end}}
```

**비밀글의 사진은 싣지 않는다.** 목록은 비밀글 제목을 누구에게나 보이지만 사진은 본문이다 —
`(not .IsSecret)` 를 빠뜨리면 읽을 권한이 없는 사람에게 본문 사진이 나간다.

## 뷰 모델 규약

모든 템플릿이 공통으로 받는 것.

| 이름 | 내용 |
|---|---|
| `.Site` | 사이트 이름, 기본 메타, 사이트 유형(`cms`/`shop`), 사업자 정보, Turnstile 사이트 키(`.Site.Turnstile`, 꺼져 있으면 빈 값 — FR-216), GA4 측정 ID(`.Site.Analytics`, 없으면 빈 값 — FR-714. 내장 `partials/head.html` 이 gtag.js 를 붙인다. `<head>` 를 덮어쓴 테마는 그 블록을 옮겨 적어야 측정된다), 테마 로고·파비콘(`.Site.Logo`·`.Site.Favicon` — FR-310. `theme.json` 이 지정했을 때의 자산 주소, 없으면 빈 값), 검색 노출(`.Site.SEO` — FR-718. 소유 확인 `GoogleVerify`·`NaverVerify`·`BingVerify` 와 `NoIndex`. 내장 `partials/head.html` 이 메타 태그로 싣는다. `<head>` 를 덮어쓴 테마는 그 블록을 옮겨 적어야 소유 확인이 된다), 가입 닫힘(`.Site.SignupClosed` — FR-217. 내장 테마는 머리글·로그인 화면의 가입 링크를 감추고 `auth/signup.html` 이 폼 대신 이유를 보인다. 막는 것은 서버다 — 링크를 남긴 테마는 403 화면으로 안내할 뿐이다) |
| `.Menu` | 메뉴 트리 (FR-405) |
| `.User` | 로그인 사용자 (미로그인이면 nil). 이메일·표시이름만. **역할·권한 원본은 넘기지 않는다** |
| `.Can` | 미리 계산된 권한 불리언 맵 ([D15](15-access-control.md) 4.3) |
| `.Flash` | 이번 요청의 메시지 |
| `.Meta` | 이 화면의 제목·설명·OG 이미지 (FR-511), canonical 주소 `URL`(site_url 이 없으면 빈 값 — 그때는 싣지 않는다), og:type `Type`(`website`·글은 `article`), 구조화 데이터 `JSONLD`(FR-719, 없으면 nil — `<script type="application/ld+json">{{.}}</script>` 로 그대로 싣는다. html/template 이 JSON 으로 이스케이프한다) |
| `.Path` | 현재 경로 |

화면별 데이터는 `.Data` 아래 온다. 각 화면이 무엇을 넘기는지는
[D12](12-screens-public.md)의 **데이터** 항목에 적혀 있다.

**상품 이미지** (FR-601). 테마가 읽는 것은 둘이다.

| 어디서 | 이름 | 내용 |
|---|---|---|
| 상품 목록의 각 상품 (`shop/list.html`·`shop/search.html`·`home.html`의 `.Products`) | `.ImageID` | 대표 이미지의 id. **없으면 빈 문자열** — `{{if .ImageID}}`로 가른다 |
| `shop/product.html` | `.Data.Images` | 그 상품의 이미지 전부, 올린 순서. 각 항목의 `.ID` |

주소는 `/shop/images/{id}` (P-306)다. **코어는 원본을 그대로 내보내고 폭·높이를 알려 주지 않는다** —
썸네일을 만들지 않는다. 그래서 자리를 잡는 것은 테마의 몫이다: `<img>`에 `width`·`height` 속성이나
CSS `aspect-ratio`를 주어 그림이 늦게 와도 레이아웃이 밀리지 않게 하고, 비율이 다른 원본은
`object-fit`으로 맞춘다. `alt`에는 상품 이름을 넣고, 목록의 썸네일에는 `loading="lazy"`를 건다.
내장 테마가 그 본보기다 (`.product-thumb`·`.product-gallery`).

**규약**

1. **nil 검사를 템플릿에 미루지 않는다.** 목록은 비어 있어도 빈 슬라이스로 넘긴다
2. **템플릿이 계산하게 하지 않는다.** 합계·개수·표시 여부는 핸들러가 정한다
3. 없는 키를 참조하면 렌더링이 실패한다 — 운영 모드에서는 500, 개발 모드에서는 원인을 보여준다

---

## 폴백 동작 (FR-308)

```
템플릿 이름 요청
 └→ 1. 활성 테마의 디스크 경로에 있는가?  → 쓴다
    2. 내장 테마에 있는가?                → 쓴다
    3. 둘 다 없다                         → 오류 (내장 테마에 없는 이름은 코어 버그다)
```

- **부분 오버라이드가 정상 사용법이다.** `board/view.html` 하나만 만들어도 된다
- 테마를 활성화하기 전에 코어가 `base.html` 존재를 확인한다. 없으면 활성화를 거부한다 (A-202)
- 개발 모드에서는 매 요청 재파싱, 운영 모드에서는 캐시 (FR-306, NFR-104)

## 아직 정하지 않은 것

미결은 [D18 미결 대장](18-open-decisions.md)에 모아 둔다. 문서마다 표를 두면 결정을 내려도 일부만 지워져 낡은 항목이 남는다.
