# D86. 보안·성능 점검 목록

> **이 문서는 「무엇을 점검하는가」의 목록이다.** 항목마다 어디를 보는지, 어떻게
> 확인하는지, 그리고 **마지막으로 확인한 날의 상태**를 적는다. 상태는 코드·테스트·
> 실측에서 읽은 것만 적는다 — 「그럴 것이다」는 이 문서에 없다.
> 확인되지 않은 것은 △ 로 남기고, 결함으로 확정된 것은 [D85](85-gaps.md)에 `GAP-##`/
> `BUG-##` 로 옮긴다. 이 문서는 대장이 아니라 **점검표**다.

관련: [D60 보안](60-security.md) · [D15 접근 제어](15-access-control.md) · [D50 커머스](50-commerce.md) ·
[D70 운영](70-operations.md) · [D85 대장](85-gaps.md)

**상태 기호** — ✓ 확인됨 (무엇으로 확인했는지 함께) · △ 열림 (아직 안 했거나 못 잰 것) ·
— 하지 않기로 함 (이유 함께)

**마지막 전수 점검: 2026-09-14** (`370e9d9` 이후 이 문서의 △ 를 닫는 작업까지). 방법: 다섯 갈래(인증·세션·RBAC / 파일·
템플릿·XSS / SQL·결제·웹훅 / DB 성능 / HTTP·런타임)로 코드 전체를 읽고, `govulncheck`·
`staticcheck`·`gosec` 을 돌리고, 결함으로 확인된 것은 고친 뒤 실제 PostgreSQL 로 재현·
검증했다. 결과는 [CHANGELOG](../CHANGELOG.md) v0.2.0 「Security」「Performance」에 있다.

---

## 1. 인증·세션

| # | 점검 항목 | 어디 | 확인 방법 | 상태 |
|---|---|---|---|---|
| 1.1 | 세션 쿠키 `HttpOnly`·`SameSite=Lax`·`Secure`(설치 시 감지) | `internal/app/app.go` 세션 설정 | 코드 읽기 | ✓ 셋 다. 수명 12시간 |
| 1.2 | 로그인·가입·소셜·비밀번호 변경 뒤 세션 토큰 **재발급**(고정 공격) | `handler_login.go`·`handler_account.go`·`handler_social.go` | `RenewToken` 호출 | ✓ 네 경로 모두 `RenewToken` 뒤 `Put` |
| 1.3 | 로그아웃이 서버 세션을 **파기**(키 비우기 아님) | `handler_login.go` | `sm.Destroy` | ✓ |
| 1.4 | `sessions_valid_from` 컷오프를 **매 요청** 검사, 비활성·삭제 계정의 세션 파기 | `middleware_auth.go` | 코드 읽기 | ✓ 셋 다 `withActor` 에서. `auth_at` 없으면 파기(닫힌 실패) |
| 1.5 | 비밀번호 해시 bcrypt, 존재하지 않는 계정에도 **같은 비용**의 비교(계정 열거 방지, FR-201) | `internal/auth/login.go` | 코드 읽기 | ✓ `DefaultCost`(10), 더미 비교 |
| 1.6 | 인증·재설정 토큰: `crypto/rand` 32바이트, **SHA-256 저장**, 1회용·만료 | `internal/auth/token.go` | 코드 읽기 | ✓ `UPDATE … WHERE used_at IS NULL AND expires_at > now() RETURNING` |
| 1.7 | 재설정 요청 응답이 계정 존재·SMTP 실패와 무관하게 **같다** | `handler_reset.go` | 코드 읽기 | ✓ |
| 1.8 | `next` 리다이렉트: 스킴·`//`·`\`·`javascript:` 거부 | `internal/auth/login.go` `SafeNext` | 단위 테스트 | ✓ |
| 1.9 | OAuth `state` 32바이트·세션 보관·**상수 시간 비교**·1회용, 콜백 프로바이더 허용목록 | `handler_social.go` | 코드 읽기 | ✓ `subtle.ConstantTimeCompare` |
| 1.10 | 소셜 계정은 **기존 계정에만 연결**(이메일 일치로 자동 연결 없음) | `internal/auth/social.go` | 코드 읽기 | ✓ `(provider, uid)` 로만 찾는다 |
| 1.11 | 속도 제한: 로그인 IP 10/분·계정 5/분, 재설정 3/시간, 가입 5/시간, 재인증 5/분 | `internal/auth/ratelimit.go` `DefaultLimits` | 단위 테스트가 D15 4.3-2 와 대조 | ✓ |
| 1.12 | **프록시 뒤에서 IP 별 제한이 사이트 전체 제한이 된다** | `middleware_gate.go` `clientIP` — `RemoteAddr` 만 | [D72](72-deploy-lightsail.md) 「프록시 뒤의 속도 제한」 | — 코드로는 안 고친다(`X-Forwarded-For` 신뢰는 CloudFront 구성에서 오히려 틀린다). 프록시의 `limit_req` 가 맡는다 |
| 1.13 | 재인증 창 15분, 위험 작업(테마 업로드·환불·계정 조작·결제 설정)에 요구 | `internal/app/reauth.go`·각 핸들러 | 코드 읽기 | ✓ |
| 1.14 | FR-214: 켜져 있으면 미인증 계정은 글·댓글·주문 불가 | `handler_board.go`·`handler_comment.go`·`handler_checkout.go` | `TestUnverifiedAccountCannotWriteWhenRequired` | ✓ 2026-09-13 에 고침 |

## 2. 권한(RBAC)

| # | 점검 항목 | 어디 | 확인 방법 | 상태 |
|---|---|---|---|---|
| 2.1 | 모든 관리자 핸들러가 **자기 권한을 직접 확인**(`d.require`), 라우트 표의 권한과 같은 키 | `internal/admin/handlers*.go` ↔ `internal/app/tree.go` | 기동 자체 점검 + 코드 읽기 | ✓ |
| 2.2 | 게시판 범위 권한이 **범위째 저장**된다 | `internal/auth/store.go` `GrantPermission` | `TestGrantPermissionKeepsBoardScope` | ✓ 2026-09-13 에 고침(전역 저장이었다) |
| 2.3 | 권한 상승 사다리 R1~R6, superuser 유일·편집 불가, 마지막 superuser 보호 | `internal/auth/escalation.go`·`roles_one_superuser_idx`·`withLastSuperuserGuard` | 단위·통합 테스트 | ✓ |
| 2.4 | A-404·A-405 폼이 **실제 라우트에 닿는다** | `tree.go` `/admin/roles/permissions`·`/admin/users/roles` | `TestRoleFormsReachTheirHandlers` | ✓ 2026-09-13 에 고침(`guardID` 에 막혀 아무도 관리자가 될 수 없었다) |
| 2.5 | 대량 할당: 폼은 고정 구조체, 예약 키(`is_admin`·`is_active`·`email`…) 거부 | `content/customfield.go`·`handler_account.go` | 코드 읽기 | ✓ |
| 2.6 | 소유권은 **SQL 술어**로: 주문·환불·반품·댓글·글·첨부 | `commerce/order.go`·`returns.go`·`handler_board.go`·`handler_comment.go` | 통합 테스트(`TestOrderDetailNeedsAGrantOrOwnership` 등) | ✓ |
| 2.7 | `{id}` 경로는 uuid 만(`guardID`), 형식 불량은 404 | `internal/app/routes.go` | 통합 테스트 | ✓ |
| 2.8 | 관리자 트리 IP 제한 60/분 | `middleware_gate.go` | 코드 읽기 | ✓ (1.12 의 한계 동일) |
| 2.9 | 글·댓글 작성에 속도 제한 없음 | [D15](15-access-control.md) 4.3-2 표에 없다 | — | — 표가 정한 것이 아니다. 인증 계정(FR-214)과 게시판 권한이 문이고, 스팸은 중재(A-307)의 몫. 필요해지면 D15 표에 행을 먼저 더한다 |

## 3. CSRF·출력 인코딩·헤더

| # | 점검 항목 | 어디 | 확인 방법 | 상태 |
|---|---|---|---|---|
| 3.1 | `net/http.CrossOriginProtection` 이 설치·운영 **두 트리 전체**를 감싼다 | `app.go`·`install.go` | 코드 읽기 | ✓ 우회 등록 없음 |
| 3.2 | 상태를 바꾸는 GET 은 비밀에 묶인다 | `tree.go` — `/checkout/success`(세션 `pending_order`), `/auth/{provider}/callback`(state), `/verify/{token}`(토큰) | 기동 자체 점검이 SC-5/SC-6 GET 에 사유를 요구 | ✓ 셋뿐 |
| 3.3 | 테마 렌더링은 `html/template` 만, `text/template` 없음 | 전체 | `grep '"text/template"'` | ✓ 0건 |
| 3.4 | `template.HTML` 생성 지점이 **셋**이고 각각 출처가 신뢰됨 | `admin_render.go`(내장 로고)·`handlers_scan.go`(정수 좌표만 찍는 QR SVG)·`theme/funcs.go`(`nl2br`, 이스케이프 뒤) | `grep 'template.HTML('` | ✓ |
| 3.5 | FuncMap 에 `raw`·`safeHTML`·`js`·`html` 없음 | `theme/funcs.go` | 테스트 `ForbiddenFuncNames` | ✓ |
| 3.6 | 본문 렌더링 파이프라인(마크다운·리치텍스트) | 없음 — 글·페이지 본문은 `nl2br` 뿐 | 코드 읽기 | ✓ 살균기가 필요 없다 |
| 3.7 | 보안 헤더: `X-Frame-Options: DENY`·`nosniff`·`Referrer-Policy`·CSP | `internal/httpsec/headers.go` | 단위 테스트 | ✓ 정적 자산·첨부에도 (트리 밖으로 옮기며 유지) |
| 3.8 | CSP 에 `script-src`/`default-src` 없음 (인라인 스크립트 허용) | `headers.go` | — | — 의도적. XSS 방어는 3.3~3.5 에 있다. 테마가 인라인을 쓴다 |
| 3.9 | `Host`·`X-Forwarded-Host` 신뢰 | `handler_seo.go`(사이트맵 origin)·`app.go`(OAuth `redirect_uri`) | `grep X-Forwarded-Host` | ✓ `X-Forwarded-Host` 0건. 메일 링크는 요청이 아니라 `site_url`(4.6) |
| 3.10 | 오류 페이지가 내부를 말하지 않는다 | `handler_public.go` `serverError` | 코드 읽기 | ✓ 원문은 **개발 빌드**에서만 (2026-09-13 에 고침 — DB 설정 하나로 켜졌었다) |
| 3.11 | HSTS | 앱은 TLS 를 모른다 — [D72](72-deploy-lightsail.md) 3절 nginx 블록의 `Strict-Transport-Security` | 프록시 응답 헤더 확인 | ✓ 2026-09-14 에 문서에 넣음. **실기로 확인한 적은 없다** |

## 4. 입력·파일·설정

| # | 점검 항목 | 어디 | 확인 방법 | 상태 |
|---|---|---|---|---|
| 4.1 | SQL 은 전부 바인딩. 문자열 결합은 **허용목록 값**(정렬 컬럼)·닫힌 열거(토큰 테이블)·상수뿐 | `content/listquery.go`·`commerce/store.go`·`auth/token.go`·`content/post.go` | `grep -E '(Query\|Exec)\(ctx, [^\`]*\+'` 로 결합 지점을 세고 각각 읽는다 | ✓ 결합 4곳 모두 위 세 부류 |
| 4.2 | tsquery 는 연산자를 벗기고 바인딩 | `content/post.go` `toPrefixQuery`·`commerce/store.go` | 코드 읽기 | ✓ |
| 4.3 | JSONB 커스텀 필드 키가 SQL 에 들어가지 않고, 모르는 키 거부 | `content/customfield.go` | 코드 읽기 | ✓ |
| 4.4 | 테마 zip: `os.Root` 로만 쓰기, `entryName` 이 `..`·절대·역슬래시·NUL·깊이 거부, 심볼릭 링크 거부, 상한(총 20 MiB·엔트리 20 MiB·2000 개·깊이 10·압축비 100) | `internal/theme/upload.go` | 단위 테스트 | ✓ `gosec` 의 경로 traversal 경고는 오탐으로 확인 |
| 4.5 | 첨부: 확장자 허용목록(`.jpg .jpeg .png .gif .webp .pdf .zip .txt .csv`), **매직바이트 대조**, `YYYY/MM/<uuid>` 저장, `os.Root`, 내려줄 때 `Content-Disposition: attachment`·`nosniff`·부모 글 권한 재확인 | `content/upload.go`·`content/attachment.go`·`handler_attachment.go` | 코드 읽기 | ✓ `.svg`·`.html` 없음. 운영자는 목록을 **줄일 수만** 있다 |
| 4.6 | 설정 파일 0600·원자적 쓰기, `site_url` 은 설치 요청에서, `secure_cookies` 설치 시 감지 | `internal/config/config.go`·`install.go` | `TestSaveUsesOwnerOnlyPermissions`·`TestRequestOriginFollowsTheInstallRequest` | ✓ `site_url` 은 2026-09-13 추가 |
| 4.7 | 설치 창: 설정이 써진 순간 닫힘, 채워진 DB 거부 | `install.go` | `TestInstallStaysClosedWhenSwitchFails` | ✓ 2026-09-13 에 고침 |
| 4.8 | 본문 상한: 글 64 MiB(`MaxBytesReader`)·테마 24 MiB·웹훅 1 MiB·토스 응답 1 MiB·첨부 `LimitReader(max+1)` | 각 핸들러 | `grep MaxBytesReader` | ✓ `ParseMultipartForm` 은 모두 `MaxBytesReader` 뒤 |
| 4.9 | 정적 파일: `static/` 아래만, 심볼릭 링크 탈출·디렉터리 목록 404 | `theme/static.go` | 단위 테스트 | ✓ |
| 4.10 | SMTP 목적지가 `169.254.0.0/16`(메타데이터)이면 거부 | `internal/app/mail.go` `blockMetadataAddr` | 단위 테스트 | ✓ 해석된 주소로 판정 |
| 4.11 | 글 삭제가 첨부 **파일**까지 지운다(디스크 고아 없음) | `content/attachment.go` `DeletePost` — P-207·A-307 둘 다 이 경로 | 코드 읽기 | ✓ 행은 CASCADE, 파일은 `os.Root.Remove` |

## 5. 결제·커머스

| # | 점검 항목 | 어디 | 확인 방법 | 상태 |
|---|---|---|---|---|
| 5.1 | 금액은 서버가 스냅샷에서 계산, 폼에 가격·할인·수량 상한 밖 값 없음 | `commerce/order.go` `CreateOrder`·`amount.go` | 통합 테스트 | ✓ 수량 1~999, 합계 상한 |
| 5.2 | 승인 전 금액 대조(FR-607), 승인 후 응답 금액 대조 | `commerce/payment.go` ①②⑥ | `TestAmountMismatchNeverReachesTheGateway` | ✓ |
| 5.3 | **승인 응답의 상태로 판정** — 200 ≠ 승인. 가상계좌는 `입금대기`, 결제완료는 웹훅이 조회 API 로 `DONE` 확인 뒤 | `payment.go` ⑥·`webhook.go` | `TestVirtualAccountConfirmIsNotPaid`·`TestDepositWebhookCompletesOnlyOnGatewayDone` | ✓ 2026-09-13 에 고침(입금 없이 결제완료였다) |
| 5.4 | 이중 승인은 **DB 부분 유니크**가 막는다(FR-608) | `payments_order_approved_idx`·`payments_exchange_idx` | `TestSecondConfirmIsRefusedByTheDatabase` | ✓ |
| 5.5 | 주문 상태 전이는 표(`state.go`)를 거치고 **비교-교환** | `payment.go` `moveOrder`·`returns.go` | 통합 테스트 | ✓ 교환 차액도 2026-09-13 부터 |
| 5.6 | 환불 누적 ≤ 승인액은 **DB CHECK** | `payments_refund_within_approved` | `TestConcurrentRefundsCannotOvershoot` | ✓ |
| 5.7 | 재고: `FOR UPDATE` 정렬 잠금 + `CHECK (stock >= 0)` | `commerce/stock.go`·`00011` | `TestStockVersionMismatchIsRefused` 등 | ✓ 초과 판매 경로 없음 |
| 5.8 | 웹훅: `(pg, event_id)` 유니크로 멱등, 본문 1 MiB, secret **상수 시간** 비교, 본문을 진실로 쓰지 않음 | `webhook.go`·`handler_webhook.go` | `TestWebhookSecretMismatchIsRefused` | ✓ |
| 5.9 | 교환 대상 조합은 노출 중인 것만 | `returns.go` `OpenReturn` | `TestExchangeRefusesHiddenVariant` | ✓ 2026-09-13 |
| 5.10 | 시크릿·카드 정보가 로그·화면·원문 보관에 없다 | `Toss.String`(가림)·`MaskCardFields`·`oplog.go`(비밀 필드 없음)·A-602 | `grep` + `make verify-upgrade` ⑤ 실기동 로그 | ✓ |
| 5.11 | 작업 로그 append-only(D15 7절): DELETE 거부, UPDATE 는 `SET NULL` 한 경우만 | `00010`·`00021` | `TestOperationLogCannotBeRewrittenUnderCoverOfActorNull` | ✓ 2026-09-13 에 구멍을 막음 |
| 5.12 | 취소 API 호출: 접수(A-507)·취소(P-506)·반품 정산(A-511)이 `ExecuteRefund` 로 PG 를 부르고 `완료` 로 확정 | `commerce/refund.go` `ExecuteRefund` | `TestExecuteRefundCallsThePGOnceAndCompletes`·`…KeepsUnknownResultsAndRetriesDeclines` | ✓ 2026-09-14 에 넣음(이전엔 호출처 0 — 돈이 나가지 않았다). 실제 토스 왕복은 [GAP-03](85-gaps.md) |
| 5.13 | `결제대기` 만료: `AuthWindow` 를 넘긴 주문을 `결제실패` 로, 재고 복원. 결과 불명 결제가 있는 주문은 제외 | `commerce/order.go` `ExpirePendingOrders`, `app.go` 1분 고루틴 | `TestExpirePendingOrdersRestoresStock` | ✓ 2026-09-14 |
| 5.14 | `payments_pg_key_idx` 가 부분 인덱스가 아님 | `00013` | 코드 읽기 | — 그대로 둔다. 토스 `paymentKey` 는 결제 시도마다 새 값이라 실패 행이 키를 점유해도 정상 재결제는 막히지 않고, 다른 주문에서 같은 키가 오는 것(`ErrPaymentKeyReused`)을 잡는 쪽이 더 값지다 |
| 5.15 | 웹훅 서명 실패 응답 400 | `handler_webhook.go`·[D19](19-screen-io.md) P-905 | 토스 웹훅 가이드(200 만 성공, 최대 7회 재전송) | ✓ 2026-09-14 확정 |
| 5.16 | 구매자 부분 환불 요청(P-507)의 승인·거부 | A-507 | — | △ 화면이 없다 — [GAP-08](85-gaps.md) |

## 6. 운영 환경

| # | 점검 항목 | 어디 | 확인 방법 | 상태 |
|---|---|---|---|---|
| 6.1 | systemd: `User=ondolith`·`ProtectSystem=strict`·`ReadWritePaths` | [D72](72-deploy-lightsail.md) 2절 | Lightsail 실기(2026-09-02) | ✓ 재부팅 후 자동 기동까지 |
| 6.2 | 설치 페이지 노출 창을 **인스턴스 방화벽**으로 막는다(`ufw` 는 inactive) | [D71](71-install-guide.md) 3절 | Lightsail 실기 | ✓ 문서 고침 |
| 6.3 | 백업 3종(DB·설정·업로드)이 실제로 떠지고 복원된다 | [D72](72-deploy-lightsail.md) 5절·`make verify-upgrade` ⑥ | 실기 | ✓ |
| 6.4 | 의존성·툴체인 취약점 | `make vuln` (`govulncheck`) | 릴리즈마다, CI `vuln` 잡 | ✓ 2026-09-13: 호출 경로 0건. 모듈에 남은 것은 [GAP-04](85-gaps.md) |
| 6.5 | 정적 분석 | `staticcheck`·`gosec` | 손으로: `go run honnef.co/go/tools/cmd/staticcheck@latest ./...` · `go run github.com/securego/gosec/v2/cmd/gosec@latest ./...` | ✓ 2026-09-13: staticcheck 는 스타일뿐, gosec HIGH 2건은 오탐(4.4) |
| 6.6 | 로그에 DSN·비밀번호·토큰 없음 | 실기동 로그 | `make verify-upgrade` ⑤ | ✓ |
| 6.7 | 세션 테이블 정리 | `pgxstore.New`(기본 5분 주기), 종료 시 `StopCleanup` | 코드 읽기 | ✓ |

## 7. 성능 — 데이터베이스

| # | 점검 항목 | 어디 | 확인 방법 | 상태 |
|---|---|---|---|---|
| 7.1 | 요청당 고정 질의 수 | 세션 1 + 액터 1~2 + 설정 1(상점 2) + 메뉴 1 | 코드 읽기 | ✓ 익명 페이지 ≈ 4~5, 로그인 ≈ 5~7. 설정을 매 요청 읽는 것은 FR-303 의 결정 |
| 7.2 | `/static/` 이 세션·권한 조회를 타지 않는다 | `app.go` 트리 밖 분기 | `TestStaticAssetsSkipTheSessionAndAreCacheable` | ✓ 2026-09-13 |
| 7.3 | 목록 질의가 한 번(N+1 없음): 글·댓글·상품·주문·장바구니·메뉴·역할 | `content/post.go` 등 | 코드 읽기 | ✓ 예외는 7.9 |
| 7.4 | 인덱스: 자주 쓰는 WHERE/ORDER BY 마다 | `internal/migrations/*.sql` (54개) | 질의와 대조 | ✓ 2026-09-13 에 셋 추가(최근 글·배송·대사). 남은 것은 7.10 |
| 7.5 | 검색이 GIN 인덱스를 탄다 | `post.go` `ListPosts`(조건부 절)·`SearchPosts`·`SearchProducts` | `EXPLAIN` | ✓ 2026-09-13 에 OR 절 제거. **EXPLAIN 으로 실측한 적은 없다** |
| 7.6 | 풀: `MinConns=1`, 상한 기본(1 vCPU 에서 4) | `app.go` | 코드 읽기 | ✓ |
| 7.7 | 트랜잭션이 네트워크 호출을 잡고 있지 않다 | `payment.go`(승인 전 커밋)·`cart.go`(읽기가 앞) | 코드 읽기 | ✓ |
| 7.8 | 요청 ctx 가 DB 까지 간다; 종료 시 설정 조회가 끊기지 않는다 | `app.go` `settingCtx` | 코드 읽기 | ✓ `setting()` 만 부팅 ctx 의 취소를 뗀 것 |
| 7.9 | 반품 목록(`Returns`)이 한 질의 | `returns.go` | 기존 반품 테스트 | ✓ 2026-09-14 |
| 7.10 | `views`·`title` 정렬 인덱스 | `00022` | 코드 읽기 | ✓ 2026-09-14 (기본 방향만) |
| 7.11 | OFFSET 페이징 상한 100 페이지; 키셋 커서(`After`)는 파싱만 되고 미사용 | `listquery.go` | 코드 읽기 | — 상한 100 × 100 행이면 최악 OFFSET 10,000 을 인덱스가 흡수한다. 커서는 그 상한이 부족해질 때 |
| 7.12 | 목록·최근 글·중재 목록은 본문을 싣지 않는다 | `post.go` `postListColumns` | 기존 목록 테스트 | ✓ 2026-09-14 |
| 7.13 | 목록마다 `count(*)` | `CountPosts`(게시판)·`OpLog.Count`(A-601) | 코드 읽기 | — 게시판 총수는 화면 요소다: 내장 테마 페이저가 「전체 N건」을 그린다(`partials/pagination.html`, `TestPaginationLinksReflectWhereYouAre`). 없애 봤다가 되돌렸다. 인덱스만 세는 질의라 둔다 |
| 7.14 | 댓글 삭제: 먼저 지우고 23503 이면 묘비 | `post.go` `DeleteComment` | 기존 댓글 테스트 | ✓ 2026-09-14 |
| 7.15 | `statement_timeout` | 없음 | — | — 부팅 마이그레이션이 같은 풀을 쓴다. 두면 큰 표의 마이그레이션이 끊긴다 |
| 7.16 | `operation_logs` 가 영원히 자란다 | `00010` — 삭제·수정 금지가 설계 | — | — D15 7절의 성질이다. 보존 기간·아카이브는 정한 바 없다 ([D18](18-open-decisions.md) 후보). 목록은 `LIMIT 100`·`created_at` 인덱스라 크기에 무관 |

## 8. 성능 — HTTP·런타임

| # | 점검 항목 | 어디 | 확인 방법 | 상태 |
|---|---|---|---|---|
| 8.1 | 서버 시한: 헤더 10초·읽기 30초·쓰기 60초·유휴 120초, 종료 15초 | `cmd/ondolith/main.go` | 코드 읽기 | ✓ `MaxHeaderBytes` 는 기본 1 MiB |
| 8.2 | 템플릿은 한 번 파싱·캐시, 테마 교체는 포인터 교환 | `theme/loader.go`·`app.go` | 코드 읽기 | ✓ 개발 모드만 매 요청 재파싱(FR-306) |
| 8.3 | 정적 자산 캐시: 해시 주소 `immutable` 1년, 그 외 1시간 | `theme/static.go` | 8.2 의 테스트 | ✓ 2026-09-13 |
| 8.4 | 아웃바운드 시한: 토스 30초, SMTP 접속 10초·교환 30초 | `commerce/payment.go` `GatewayTimeout`·`app/mail.go` | 코드 읽기 | ✓ 2026-09-13 |
| 8.5 | 고루틴 기한: 웹훅 처리 1분, 메일 전송은 8.4 로 상한 | `handler_webhook.go`·`auth/mail.go` | 코드 읽기 | ✓ 전역 동시 수 제한은 없음(IP 별 제한이 앞에 있다) |
| 8.6 | 레이트리미터 지도가 자란다 | `auth/ratelimit.go` | `TestAllowSweepsIdleBucketsOnceLarge` | ✓ 2026-09-13 |
| 8.7 | 사이트맵: 질의 하나 + `Cache-Control` 1시간 | `handler_seo.go`·`content.SitemapPosts` | 코드 읽기 | ✓ 2026-09-13 |
| 8.8 | bcrypt 비용 10 = 1 vCPU 에서 시도당 수십~백 ms | `auth/login.go` | — | — 의도적. IP·계정 제한이 앞에 있다 |
| 8.9 | A-508 대사: 조회 하나 5초, 요청 전체 40초 예산, 넘긴 행은 「조회하지 않았다」 | `commerce/webhook.go` `Reconcile` | 코드 읽기 | ✓ 2026-09-14 |
| 8.10 | 1 vCPU/512MB 실측 | `make measure` | 실기 | ✓ 2026-09-02 Lightsail: 앱 RSS 15.6 MB, PG 125 MB, 가용 165 MB |
| 8.11 | 응답 압축(gzip/br) 없음 | `net/http` 에 내장 없음 | — | — 프록시(nginx `gzip on`, CloudFront)가 한다. 앱은 정적 자산에 `immutable` 캐시(8.3)로 재요청 자체를 줄인다 |

## 9. 점검을 다시 할 때

1. `make check` — 여기 적힌 테스트 전부가 돈다 (DB 테스트는 `make test-integration`)
2. `make vuln` + 6.5 의 두 도구
3. 이 문서의 △ 를 하나씩 닫거나 [D85](85-gaps.md)로 옮긴다
4. 새 결제 수단·새 업로드 종류·새 관리자 화면이 생기면 5·4·2절에 **행을 먼저 추가**한다 —
   항목 없는 기능은 점검되지 않는다
