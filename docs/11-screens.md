# D11. 화면 인벤토리

**모든 화면은 여기에 한 번만 정의된다.** 상세는 [D12](12-screens-public.md)(공개) /
[D13](13-screens-admin.md)(관리자), 연계는 [D14](14-screen-flows.md), 권한 모델은
[D15](15-access-control.md)에 있다. 추가 절차는 [D90](90-conventions.md) 6-1절.

## 이 표를 읽는 법

| 열 | 뜻 |
|---|---|
| `접근` | **`공개` · `로그인` · `본인` · `권한:{key}` 넷 중 하나만.** 화면은 역할이 아니라 권한으로 잠근다 ([D15](15-access-control.md) P1) |
| `상태변경` | `있음`이면 비-GET 메서드가 있다 → CSRF 적용 대상이고 서버측 권한 재검증이 필수다 |
| `유형` | `SC-1`~`SC-8`. 유형이 곧 그 화면이 통과해야 할 보안 체크리스트다 ([D15](15-access-control.md) 7절) |

> **`접근`이 `본인`인 화면은 권한 검사로 끝나지 않는다.** 소유권은 권한이 아니라 `WHERE` 절이다
> ([D15](15-access-control.md) P4). 조회 후 Go에서 비교하지 않는다.

> **`접근`은 "이 화면에 도달하는 데 필요한 권한"이지 화면 안의 모든 동작에 필요한 권한이 아니다.**
> 예를 들어 A-302 페이지 편집의 `접근`은 `권한:page.update`지만, 그 화면의 삭제 버튼은
> `page.delete`를, 생성은 `page.create`를 따로 요구한다. **메서드·동작별 권한은 각 화면의
> 상세([D12](12-screens-public.md)/[D13](13-screens-admin.md))와 [D15](15-access-control.md)
> 2.2절의 `사용 화면` 열에 적는다.** 표 한 칸에 여러 권한을 욱여넣으면 기계가 못 읽는다.

## 모듈 구성 (FR-710)

**사이트는 일반 사이트(`cms`) 또는 쇼핑몰(`shop`)로 설정된다.** 설치 시 고르고 A-201에서
바꿀 수 있다. 커머스를 쓰지 않는 설치처가 대다수이므로 기본값은 `cms`다.

| 모듈 | 포함 대역 | `cms`일 때 |
|---|---|---|
| **핵심** | `P-0xx` `P-1xx` `P-2xx` `P-9xx`(P-905 제외) `A-1xx` `A-2xx` `A-3xx` `A-4xx` `A-6xx` | 항상 등록 |
| **커머스** | `P-3xx` `P-4xx` `P-5xx` `A-5xx` + **P-905** | **라우트를 등록하지 않는다** |

> **숨기는 것이 아니라 등록하지 않는다.** 메뉴만 숨기고 라우트를 살려두면 주소를 아는 사람이
> 그대로 들어간다 — 템플릿에서 버튼을 숨긴 것을 권한 검사로 치지 않는 것과 같은 원칙이다
> ([D15](15-access-control.md) 4.3). 설치/운영 트리를 나눈 것과도 같은 사고방식이다
> ([D20](20-architecture.md)).

**P-905(결제 웹훅)는 `P-9xx` 대역이지만 커머스 모듈이다.** 대역이 곧 모듈이라는 규칙의
유일한 예외이며, 커머스가 꺼진 사이트에 결제 웹훅 엔드포인트를 열어둘 이유가 없다.

**끌 때의 안전장치:** 주문 데이터가 있는 상태에서 `cms`로 바꿔도 **A-504·A-505(주문 조회)는
남긴다.** 정산·분쟁 대응이 필요하기 때문이다. 새 주문을 받는 공개 화면만 닫힌다.
자세한 규칙은 [D13](13-screens-admin.md) A-201.

## 대역

| 대역 | 영역 | Phase |
|---|---|---|
| `P-0xx` | 설치 (설치 전에만 존재하는 라우트 트리) | 0 |
| `P-1xx` | 인증·계정 | 1 |
| `P-2xx` | 콘텐츠 (페이지·게시판·검색) | 1~2 |
| `P-3xx` | 상품 | 3 |
| `P-4xx` | 장바구니·주문·결제 | 3 |
| `P-5xx` | 주문조회·취소환불 | 3 |
| `P-9xx` | 시스템 (sitemap·오류·웹훅) | 1~3 |
| `A-1xx` | 관리자 셸 | 1 |
| `A-2xx` | 사이트 설정·테마 | 1~2 |
| `A-3xx` | 콘텐츠 관리 | 1~2 |
| `A-4xx` | 사용자·역할 | 1 |
| `A-5xx` | 커머스 관리 | 3 |
| `A-6xx` | 운영 | 2~4 |

---

## 공개 화면

### P-0xx 설치

> **설치 트리는 운영 트리와 완전히 별개다** ([D20](20-architecture.md)). 설치가 끝나면 이
> 두 화면은 사라지고 `/install`은 404가 된다. Phase 0에서 이미 구현됐다.

| ID | 화면 | 경로 | 메서드 | 접근 | 상태변경 | 유형 | 관련 FR |
|---|---|---|---|---|---|---|---|
| P-001 | 설치 마법사 | `/install` | GET, POST | 공개 | 있음 | SC-2 | FR-102, FR-103, FR-104, FR-105, FR-106, FR-107, FR-108, FR-109, FR-710 |
| P-002 | 설치 유도 리다이렉트 | `/*` | GET, POST | 공개 | 없음 | SC-1 | FR-101, FR-110 |

### P-1xx 인증·계정

| ID | 화면 | 경로 | 메서드 | 접근 | 상태변경 | 유형 | 관련 FR |
|---|---|---|---|---|---|---|---|
| P-101 | 로그인 | `/login` | GET, POST | 공개 | 있음 | SC-2 | FR-201, FR-202, FR-204, FR-209 |
| P-102 | 로그아웃 | `/logout` | POST | 로그인 | 있음 | SC-2 | FR-203 |
| P-103 | 회원가입 | `/signup` | GET, POST | 공개 | 있음 | SC-2 | FR-210 |
| P-104 | 비밀번호 재설정 요청 | `/password/reset` | GET, POST | 공개 | 있음 | SC-2 | FR-207 |
| P-105 | 비밀번호 재설정 수행 | `/password/reset/{token}` | GET, POST | 공개 | 있음 | SC-2 | FR-207 |
| P-106 | 소셜 로그인 시작 | `/auth/{provider}` | POST | 공개 | 있음 | SC-2 | FR-208 |
| P-107 | 소셜 로그인 콜백 | `/auth/{provider}/callback` | GET | 공개 | 있음 | SC-2 | FR-208, FR-204 |
| P-108 | 내 정보 | `/me` | GET, POST | 본인 | 있음 | SC-3 | FR-211 |
| P-109 | 비밀번호 변경 | `/me/password` | GET, POST | 본인 | 있음 | SC-3 | FR-204, FR-211 |
| P-110 | 회원 탈퇴 | `/me/delete` | GET, POST | 본인 | 있음 | SC-3 | FR-212 |
| P-111 | 소셜 계정 연결 관리 | `/me/connections` | GET, POST | 본인 | 있음 | SC-3 | FR-213 |
| P-112 | 이메일 인증 확인 | `/verify/{token}` | GET | 공개 | 있음 | SC-2 | FR-214 |
| P-113 | 인증 메일 재발송 | `/verify/resend` | POST | 로그인 | 있음 | SC-2 | FR-214 |

### P-2xx 콘텐츠

| ID | 화면 | 경로 | 메서드 | 접근 | 상태변경 | 유형 | 관련 FR |
|---|---|---|---|---|---|---|---|
| P-201 | 홈 | `/{$}` | GET | 공개 | 없음 | SC-1 | FR-301, FR-308, FR-511 |
| P-202 | 정적 페이지 | `/{slug}` | GET | 공개 | 없음 | SC-1 | FR-401, FR-402, FR-403, FR-511 |
| P-203 | 게시판 목록 | `/board/{slug}` | GET | 권한:post.read | 없음 | SC-1 | FR-501, FR-503, FR-507, FR-508 |
| P-204 | 글 보기 | `/board/{slug}/{id}` | GET | 권한:post.read | 없음 | SC-1 | FR-504, FR-505, FR-511, FR-512 |
| P-205 | 글 쓰기 | `/board/{slug}/write` | GET, POST | 권한:post.write | 있음 | SC-2 | FR-502, FR-503, FR-504, FR-506 |
| P-206 | 글 수정 | `/board/{slug}/{id}/edit` | GET, POST | 본인 | 있음 | SC-3 | FR-502, FR-503, FR-504, FR-506 |
| P-207 | 글 삭제 | `/board/{slug}/{id}/delete` | POST | 본인 | 있음 | SC-3 | FR-504 |
| P-208 | 댓글 작성 | `/board/{slug}/{id}/comments` | POST | 권한:comment.write | 있음 | SC-2 | FR-505 |
| P-209 | 댓글 수정 | `/comments/{id}/edit` | GET, POST | 본인 | 있음 | SC-3 | FR-505 |
| P-210 | 댓글 삭제 | `/comments/{id}/delete` | POST | 본인 | 있음 | SC-3 | FR-505 |
| P-211 | 첨부파일 다운로드 | `/attachments/{id}` | GET | 권한:post.read | 없음 | SC-7 | FR-506, NFR-206 |
| P-212 | 검색 결과 | `/search` | GET | 공개 | 없음 | SC-1 | FR-507, FR-508 |

### P-3xx 상품

| ID | 화면 | 경로 | 메서드 | 접근 | 상태변경 | 유형 | 관련 FR |
|---|---|---|---|---|---|---|---|
| P-301 | 상품 목록 | `/shop` | GET | 공개 | 없음 | SC-1 | FR-601, NFR-105 |
| P-302 | 카테고리별 목록 | `/shop/c/{slug}` | GET | 공개 | 없음 | SC-1 | FR-615 |
| P-303 | 상품 상세 | `/shop/p/{slug}` | GET | 공개 | 없음 | SC-1 | FR-601, FR-602 |
| P-304 | 옵션 조합 조회 (htmx) | `/shop/p/{slug}/variant` | GET | 공개 | 없음 | SC-1 | FR-602 |
| P-305 | 상품 검색 | `/shop/search` | GET | 공개 | 없음 | SC-1 | FR-614 |

### P-4xx 장바구니·주문·결제

| ID | 화면 | 경로 | 메서드 | 접근 | 상태변경 | 유형 | 관련 FR |
|---|---|---|---|---|---|---|---|
| P-401 | 장바구니 담기 | `/cart/items` | POST | 공개 | 있음 | SC-2 | FR-602, FR-603 |
| P-402 | 장바구니 보기 | `/cart` | GET | 공개 | 없음 | SC-1 | FR-603 |
| P-403 | 장바구니 수량 변경 | `/cart/items/{id}` | PATCH | 본인 | 있음 | SC-3 | FR-602, FR-603 |
| P-404 | 장바구니 항목 삭제 | `/cart/items/{id}` | DELETE | 본인 | 있음 | SC-3 | FR-603 |
| P-405 | 주문서 작성 | `/checkout` | GET | 공개 | 없음 | SC-6 | FR-604, FR-613, FR-626 |
| P-406 | 주문 생성 | `/checkout` | POST | 공개 | 있음 | SC-6 | FR-602, FR-604, FR-613, FR-626 |
| P-407 | 결제창 호출 | `/checkout/pay` | GET | 본인 | 있음 | SC-6 | FR-604, FR-605 |
| P-408 | 결제 승인 (successUrl) | `/checkout/success` | GET | 본인 | 있음 | SC-6 | FR-606, FR-607, FR-608, FR-609 |
| P-409 | 결제 실패 (failUrl) | `/checkout/fail` | GET | 본인 | 있음 | SC-6 | FR-609 |
| P-410 | 주문 완료 | `/checkout/complete` | GET | 본인 | 없음 | SC-3 | FR-604 |

### P-5xx 주문조회·취소환불

| ID | 화면 | 경로 | 메서드 | 접근 | 상태변경 | 유형 | 관련 FR |
|---|---|---|---|---|---|---|---|
| P-501 | 내 주문 목록 | `/orders` | GET | 로그인 | 없음 | SC-3 | FR-604, NFR-105 |
| P-502 | 주문 상세 | `/orders/{orderNo}` | GET | 본인 | 없음 | SC-3 | FR-604, FR-612 |
| P-503 | 비회원 주문 조회 폼 | `/orders/guest` | GET | 공개 | 없음 | SC-2 | FR-604 |
| P-504 | 비회원 주문 조회 실행 | `/orders/guest` | POST | 공개 | 있음 | SC-2 | FR-604, NFR-207 |
| P-505 | 배송 조회 | `/orders/{orderNo}/shipping` | GET | 본인 | 없음 | SC-3 | FR-604 |
| P-506 | 취소 요청 | `/orders/{orderNo}/cancel` | POST | 본인 | 있음 | SC-6 | FR-604, FR-611, FR-625 |
| P-507 | 부분 환불 요청 | `/orders/{orderNo}/refund` | POST | 본인 | 있음 | SC-6 | FR-611, FR-625 |
| P-508 | 취소·환불 상태 | `/orders/{orderNo}/refunds` | GET | 본인 | 없음 | SC-3 | FR-611 |
| P-509 | 주문서·영수증 | `/orders/{orderNo}/receipt` | GET | 본인 | 없음 | SC-3 | FR-612 |
| P-510 | 구매확정 | `/orders/{orderNo}/confirm` | POST | 본인 | 있음 | SC-3 | FR-604 |
| P-511 | 반품 요청 | `/orders/{orderNo}/return` | GET, POST | 본인 | 있음 | SC-6 | FR-617 |
| P-512 | 교환 요청 | `/orders/{orderNo}/exchange` | GET, POST | 본인 | 있음 | SC-6 | FR-618 |
| P-513 | 반품·교환 내역 | `/orders/{orderNo}/returns` | GET | 본인 | 없음 | SC-3 | FR-617, FR-618 |
| P-514 | 교환 차액 결제 | `/orders/{orderNo}/exchange/{returnNo}/pay` | GET, POST | 본인 | 있음 | SC-6 | FR-618, FR-607, FR-608 |

### P-9xx 시스템

| ID | 화면 | 경로 | 메서드 | 접근 | 상태변경 | 유형 | 관련 FR |
|---|---|---|---|---|---|---|---|
| P-901 | sitemap.xml | `/sitemap.xml` | GET | 공개 | 없음 | SC-1 | FR-510 |
| P-902 | robots.txt | `/robots.txt` | GET | 공개 | 없음 | SC-1 | FR-510 |
| P-903 | 404 오류 | `/*` | GET | 공개 | 없음 | SC-1 | FR-403, NFR-210 |
| P-904 | 500 오류 | `(오류 렌더링)` | GET | 공개 | 없음 | SC-1 | NFR-210 |
| P-905 | 결제 웹훅 수신 | `/webhooks/payment/{pg}` | POST | 공개 | 있음 | SC-8 | FR-610 |
| P-906 | 테마 정적 자산 | `/static/{path...}` | GET | 공개 | 없음 | SC-7 | FR-301, FR-309, NFR-206 |
| P-907 | 헬스체크 | `/healthz` | GET | 공개 | 없음 | SC-1 | NFR-101, NFR-210 |

---

## 관리자 화면

> **모든 `A-###` 화면의 접근은 `권한:{key}`다. 예외 없다.**
> 관리자 전용 로그인 화면을 따로 두지 않는다 — P-101을 재사용하고, 미인증 상태로 `/admin/*`에
> 접근하면 P-101로 보낸다. 로그인 폼이 둘이면 rate limit·세션 고정 방어·계정 열거 대응을
> 두 번 해야 하고, 한쪽만 고치는 사고가 난다. 로그아웃도 P-102를 쓴다.

### A-1xx 관리자 셸

| ID | 화면 | 경로 | 메서드 | 접근 | 상태변경 | 유형 | 관련 FR |
|---|---|---|---|---|---|---|---|
| A-101 | 대시보드 | `/admin/{$}` | GET | 권한:admin.access | 없음 | SC-4 | FR-702 |
| A-102 | 관리자 셸 (레이아웃·메뉴) | `/admin/*` | — | 권한:admin.access | 없음 | SC-4 | FR-701 |

### A-2xx 사이트 설정·테마

| ID | 화면 | 경로 | 메서드 | 접근 | 상태변경 | 유형 | 관련 FR |
|---|---|---|---|---|---|---|---|
| A-201 | 사이트 설정 | `/admin/settings` | GET, POST | 권한:settings.update | 있음 | SC-5 | FR-306, FR-511, FR-703, FR-710 |
| A-202 | 테마 목록·활성화 | `/admin/themes` | GET, POST | 권한:theme.activate | 있음 | SC-5 | FR-302, FR-303, FR-308 |
| A-203 | 테마 업로드 | `/admin/themes/upload` | GET, POST | 권한:theme.upload | 있음 | SC-7 | FR-307, NFR-206 |
| A-204 | 메뉴 관리 | `/admin/menus` | GET, POST | 권한:menu.manage | 있음 | SC-5 | FR-405 |
| A-205 | 메일 발송 설정 | `/admin/settings/mail` | GET, POST | 권한:settings.update | 있음 | SC-5 | FR-708 |
| A-206 | 소셜 로그인 설정 | `/admin/settings/social` | GET, POST | 권한:settings.update | 있음 | SC-5 | FR-709 |
| A-207 | 약관 관리 | `/admin/terms` | GET, POST | 권한:settings.update | 있음 | SC-5 | FR-619 |
| A-208 | 사업자 정보 설정 | `/admin/settings/business` | GET, POST | 권한:settings.update | 있음 | SC-5 | FR-711 |
| A-209 | 결제 설정 | `/admin/settings/payment` | GET, POST | 권한:settings.update | 있음 | SC-5 | FR-605, FR-607 |

### A-3xx 콘텐츠 관리

| ID | 화면 | 경로 | 메서드 | 접근 | 상태변경 | 유형 | 관련 FR |
|---|---|---|---|---|---|---|---|
| A-301 | 페이지 목록 | `/admin/pages` | GET | 권한:page.view | 없음 | SC-4 | FR-401, FR-403 |
| A-302 | 페이지 편집 | `/admin/pages/{id}` | GET, POST, DELETE | 권한:page.update | 있음 | SC-5 | FR-401, FR-402, FR-404 |
| A-303 | 페이지 발행 | `/admin/pages/{id}/publish` | POST | 권한:page.publish | 있음 | SC-5 | FR-403 |
| A-304 | 게시판 목록 | `/admin/boards` | GET | 권한:board.view | 없음 | SC-4 | FR-501, FR-705 |
| A-305 | 게시판 생성·설정 | `/admin/boards/{id}` | GET, POST, DELETE | 권한:board.manage | 있음 | SC-5 | FR-501, FR-505, FR-506, FR-512, FR-705 |
| A-306 | 커스텀 필드 스키마 편집기 | `/admin/boards/{id}/fields` | GET, POST | 권한:board.manage | 있음 | SC-5 | FR-502, FR-503, FR-509, FR-705 |
| A-307 | 글 관리 | `/admin/posts` | GET, POST, DELETE | 권한:post.moderate | 있음 | SC-5 | FR-504, FR-507, FR-512 |
| A-308 | 댓글 관리 | `/admin/comments` | GET, POST, DELETE | 권한:comment.moderate | 있음 | SC-5 | FR-505 |
| A-309 | 첨부파일 관리 | `/admin/attachments` | GET, DELETE | 권한:post.moderate | 있음 | SC-7 | FR-506, NFR-206 |

### A-4xx 사용자·역할

| ID | 화면 | 경로 | 메서드 | 접근 | 상태변경 | 유형 | 관련 FR |
|---|---|---|---|---|---|---|---|
| A-401 | 사용자 목록 | `/admin/users` | GET | 권한:user.view | 없음 | SC-4 | FR-704 |
| A-402 | 사용자 상세·편집 | `/admin/users/{id}` | GET, POST | 권한:user.update | 있음 | SC-5 | FR-207, FR-704 |
| A-403 | 역할 목록·정의 | `/admin/roles` | GET, POST, DELETE | 권한:role.manage | 있음 | SC-5 | FR-205, FR-704 |
| A-404 | 역할 권한 편집 | `/admin/roles/{id}/permissions` | GET, POST | 권한:role.manage | 있음 | SC-5 | FR-205 |
| A-405 | 사용자 역할 부여 | `/admin/users/{id}/roles` | GET, POST | 권한:role.assign | 있음 | SC-5 | FR-205, FR-704 |

### A-5xx 커머스 관리

| ID | 화면 | 경로 | 메서드 | 접근 | 상태변경 | 유형 | 관련 FR |
|---|---|---|---|---|---|---|---|
| A-501 | 상품 목록 | `/admin/products` | GET | 권한:product.view | 없음 | SC-4 | FR-601, FR-706 |
| A-502 | 상품 편집 | `/admin/products/{id}` | GET, POST, DELETE | 권한:product.manage | 있음 | SC-7 | FR-601, NFR-206 |
| A-503 | 옵션·재고 편집기 | `/admin/products/{id}/variants` | GET, POST | 권한:product.manage | 있음 | SC-5 | FR-602 |
| A-504 | 주문 목록 | `/admin/orders` | GET | 권한:order.view | 없음 | SC-4 | FR-508, FR-604, FR-706 |
| A-505 | 주문 상세 | `/admin/orders/{no}` | GET | 권한:order.view | 없음 | SC-4 | FR-604, FR-612 |
| A-506 | 주문 상태 변경 | `/admin/orders/{no}/transition` | POST | 권한:order.update | 있음 | SC-5 | FR-604 |
| A-507 | 취소·환불 처리 | `/admin/orders/{no}/refund` | GET, POST | 권한:order.refund | 있음 | SC-6 | FR-604, FR-611, FR-625 |
| A-508 | 결제 대사 | `/admin/payments/reconcile` | GET, POST | 권한:payment.view | 있음 | SC-6 | FR-608, FR-609, FR-610 |
| A-509 | 상품 카테고리 관리 | `/admin/categories` | GET, POST, DELETE | 권한:product.manage | 있음 | SC-5 | FR-615 |
| A-510 | 배송 정보·송장 입력 | `/admin/orders/{no}/shipping` | GET, POST | 권한:order.update | 있음 | SC-5 | FR-616 |
| A-511 | 반품·교환 처리 | `/admin/orders/{no}/returns` | GET, POST | 권한:order.return | 있음 | SC-6 | FR-617, FR-618 |
| A-512 | 커머스 정책 설정 | `/admin/commerce/policy` | GET, POST | 권한:settings.update | 있음 | SC-5 | FR-617, FR-618, FR-604 |
| A-513 | QR 라벨 발행 | `/admin/products/labels` | GET | 권한:product.manage | 없음 | SC-4 | FR-620 |
| A-514 | 스캔 입고 | `/admin/stock/receive` | GET, POST | 권한:product.manage | 있음 | SC-5 | FR-621 |
| A-515 | 재고 실사 | `/admin/stock/count` | GET, POST | 권한:product.manage | 있음 | SC-5 | FR-622 |
| A-516 | 출고 피킹 대조 | `/admin/orders/{no}/pick` | GET, POST | 권한:order.update | 있음 | SC-5 | FR-623 |
| A-517 | 스캔 재고 조회 | `/admin/stock/lookup` | GET | 권한:product.view | 없음 | SC-4 | FR-624 |

### A-6xx 운영

| ID | 화면 | 경로 | 메서드 | 접근 | 상태변경 | 유형 | 관련 FR |
|---|---|---|---|---|---|---|---|
| A-601 | 작업 로그 | `/admin/oplog` | GET | 권한:log.view | 없음 | SC-4 | FR-707 |
| A-602 | 시스템 정보 | `/admin/system` | GET | 권한:settings.view | 없음 | SC-4 | NFR-302, NFR-303, NFR-305 |
| A-603 | 웹훅 수신 이력 | `/admin/webhooks` | GET | 권한:payment.view | 없음 | SC-4 | FR-610 |

---

## 화면이 없는 필수 요구사항

**필수 `FR-###`는 원칙적으로 어느 화면인가가 실현한다.** 그렇지 않은 것은 여기 적고 이유를
남긴다 — 적히지 않은 채 화면 없이 남아 있으면 `make check`가 실패한다. 이 표가 "기능은
정했는데 만드는 화면을 안 만들었다"를 막는다.

| FR | 화면이 없는 이유 |
|---|---|
| FR-304 | 코어가 테마에 제공하는 **템플릿 함수맵**이다. 플랫폼 계약이지 화면이 아니다 ([D40](40-theme.md)) |
| FR-305 | "테마에 비즈니스 로직을 넣을 수 없다"는 **금지 규칙**이다. 실현하는 화면이 아니라 모든 화면이 지켜야 할 제약이다 |

`NFR-###`는 이 검사 대상이 아니다. 비기능 요구사항은 특정 화면이 아니라 시스템 전체에
걸리는 것이 정상이다.

## 통합하며 내린 판단

초안을 병렬로 만들면서 갈라진 부분을 여기서 통일했다. 근거를 남긴다.

| 판단 | 이유 |
|---|---|
| **접근을 역할이 아니라 권한으로 표기** | 초안들이 `역할:operator`·`역할:게시판읽기`·`역할:superadmin`으로 제각각이었다. 역할로 잠그면 설치처가 역할을 추가할 때마다 코드를 고쳐야 한다. 역할은 데이터, 권한은 코드 ([D15](15-access-control.md) P1·P2) |
| **관리자 로그인 화면 삭제, P-101 재사용** | 로그인 폼이 둘이면 rate limit·세션 재발급·계정 열거 대응이 둘이 되고 한쪽만 고쳐진다. 덕분에 "모든 `A-###`는 `권한:`" 이 예외 없는 불변식이 됐다 |
| **웹훅(P-905) 추가** | 어느 초안에도 없었다. 결제 웹훅(FR-610)은 인증 없이 외부에서 POST가 들어오는 유일한 경로다. 인벤토리에 없으면 아무도 SC-8 규칙을 적용하지 않는다 |
| **`shopmanager`를 내장 역할로 두지 않음** | 초안 하나가 제안했으나, 커머스를 안 쓰는 설치처에 죽은 역할이 남는다. 설치처가 만들 수 있다 ([D15](15-access-control.md) P2) |
| **`attachment.*` 권한 미생성** | 첨부 접근은 부모 글의 `post.read`를 따른다. 권한을 따로 두면 게시판 설정과 권한이 어긋나는 조합이 생긴다 |

## 아직 정하지 않은 것

미결은 [D18 미결 대장](18-open-decisions.md)에 모아 둔다. 문서마다 표를 두면 결정을 내려도 일부만 지워져 낡은 항목이 남는다.
