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
| `partials/pagination.html` | (목록) | 폴백 | 페이지 이동 (FR-508) |
| `partials/flash.html` | (전체) | 폴백 | 성공·오류 메시지 |
| `partials/field.html` | P-203, P-204, P-205 | 폴백 | **커스텀 필드 하나를 그린다** (FR-503). 타입별 분기가 여기 있다 |

### 콘텐츠

| 템플릿 | 화면 | 구분 |
|---|---|---|
| `home.html` | P-201 | 폴백 |
| `page.html` | P-202 | 폴백 |
| `search.html` | P-212 | 폴백 |
| `error.html` | P-903, P-904 | 폴백 |
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
| `shop/complete.html` | P-408, P-410 | 폴백 |
| `shop/fail.html` | P-409 | 폴백 |

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
| P-906 | 정적 자산 자체다 |

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
| `url "product" .Slug` | 상품 URL |
| `url "order" .OrderNo` | 주문 URL |
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
| `field .Post .Key` | 커스텀 필드 값 (FR-503) |
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

## 뷰 모델 규약

모든 템플릿이 공통으로 받는 것.

| 이름 | 내용 |
|---|---|
| `.Site` | 사이트 이름, 기본 메타, 사이트 유형(`cms`/`shop`), 사업자 정보 |
| `.Menu` | 메뉴 트리 (FR-405) |
| `.User` | 로그인 사용자 (미로그인이면 nil). 이메일·표시이름만. **역할·권한 원본은 넘기지 않는다** |
| `.Can` | 미리 계산된 권한 불리언 맵 ([D15](15-access-control.md) 4.3) |
| `.Flash` | 이번 요청의 메시지 |
| `.Meta` | 이 화면의 제목·설명·OG 이미지 (FR-511) |
| `.Path` | 현재 경로 |

화면별 데이터는 `.Data` 아래 온다. 각 화면이 무엇을 넘기는지는
[D12](12-screens-public.md)의 **데이터** 항목에 적혀 있다.

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
