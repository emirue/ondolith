# D30. 데이터 모델

## 지배 원칙

### 1. 게시판마다 테이블을 만들지 않는다

그누보드는 게시판을 만들 때마다 `CREATE TABLE`을 실행한다. 이 방식은 마이그레이션 관리를
망가뜨린다 — 스키마가 운영 데이터에 따라 달라지므로, 어떤 설치처에 어떤 테이블이 있는지
알 수 없고 마이그레이션을 검증할 수 없다. ([DEC-3.9](../.ai/DECISIONS.md))

대신 **정의 테이블 + 공통 테이블 + JSONB 커스텀 필드**로 간다.

```
boards        게시판 정의 (이름, 슬러그, 권한, 첨부 허용, 댓글 여부, 스킨)
board_fields  게시판별 커스텀 필드 스키마 (이름, 타입, 필수, 선택지, 순서)
posts         board_id로 구분. 공통 컬럼 + custom_fields JSONB
```

관리자가 게시판 설정에서 필드 구성을 저장하면 → `board_fields`에 기록되고 →
템플릿이 그 스키마를 읽어 폼과 목록을 동적으로 렌더링한다 (FR-502, FR-503).
이것이 "설정으로 게시판 뚝딱" 경험의 핵심이다.

### 2. PostgreSQL 고정

MySQL 동시 지원은 배제됐다 ([DEC-0](../.ai/DECISIONS.md)). JSONB 없이는 위 설계가 성립하지 않는다.

### 3. 규칙

| 항목 | 규칙 |
|---|---|
| PK | `uuid` + `gen_random_uuid()` (내장, `pgcrypto` 불필요). **최소 버전은 PostgreSQL 18** ([D21](21-tech-stack.md) 1절) — `UNIQUE NULLS NOT DISTINCT`(15+)를 쓸 수 있으므로 NULL 허용 컬럼의 유일성에 부분 인덱스 두 개를 만들지 않는다 |
| 명명 | snake_case, 테이블은 복수형, FK 컬럼은 `_id` 접미사 |
| FK | 항상 `REFERENCES`를 명시. 삭제 동작(`ON DELETE`)을 반드시 정한다 |
| 감사 컬럼 | `created_at`, `updated_at`. 소프트 삭제가 필요한 테이블만 `deleted_at` |
| 시각 | 전부 `timestamptz`. 애플리케이션은 UTC로 쓴다 |
| 이메일 | 애플리케이션에서 소문자화 후 저장. `citext` 확장을 쓰지 않는다 |
| 인덱스 | 모든 FK 컬럼에. 1만 행을 넘길 테이블의 필터 컬럼에 |
| 자기 참조 트리 | `parent_id`로 트리를 만드는 테이블(`menus`, `categories`)의 **순환은 FK가 막지 못한다.** 애플리케이션 불변식이며 테스트 대상이다 (NFR-405). 갱신 트랜잭션은 `pg_advisory_xact_lock`으로 **직렬화**한다 — 동시 요청 A→B와 B→A는 각자 검사를 통과한 뒤 순환을 만들고, 행 잠금만으로는 순서가 정해지지 않는다. 깊이 안전 상한 10 ([D13](13-screens-admin.md) A-509) |
| 문자열 길이 | 모든 `text` 컬럼에 상한을 정해 **3-2 표**에 적는다. 상한 없는 `text`는 [D19](19-screen-io.md) C4 위반이다. 검증은 핸들러가 하고 **값의 단일 출처는 이 문서**다 |

### 3-1. `ON DELETE` 지정

위 규칙이 "반드시 정한다"고 요구하는 값을 여기 모은다. **스키마 블록에는 이름만 적고
삭제 동작은 이 표가 단일 출처다** — 블록마다 적으면 갈라진다.

| 참조 | 동작 | 이유 |
|---|---|---|
| `posts.board_id` → `boards` | CASCADE | 게시판을 지우면 글도 간다 (A-305 확인 단계 대상) |
| `posts.author_id` → `users` | SET NULL | 글은 남고 작성자만 사라진다 |
| `comments.post_id` → `posts` | CASCADE | 글이 사라지면 댓글은 갈 곳이 없다 |
| `comments.parent_id` → `comments` | **NO ACTION** | 아래 참조 |
| `comments.author_id` → `users` | SET NULL | 〃 |
| `attachments.post_id` → `posts` | CASCADE | 고아 행을 만들지 않는다. 고아 **파일**은 허용된다 (D13 A-309) |
| `board_fields.board_id` → `boards` | CASCADE | 스키마는 게시판에 종속 |
| `user_roles.user_id` → `users` | CASCADE | 계정이 가면 부여도 간다 |
| `user_roles.role_id` → `roles` | **RESTRICT** | 부여된 사용자가 있는 역할은 삭제 거부 (D15 1.3, D19 A-403의 409) |
| `role_permissions.role_id` → `roles` | CASCADE | 역할이 가면 그 역할의 권한 행도 간다 |
| `role_permissions.permission_id` → `permissions` | RESTRICT | 권한 목록은 코드가 정한다 (D15 P1) — 행이 사라질 일이 없어야 한다 |
| `role_permissions.board_id` → `boards` | CASCADE | 스코프 대상이 사라지면 스코프도 무의미 |
| `password_reset_tokens.user_id` / `email_verification_tokens.user_id` / `social_accounts.user_id` → `users` | CASCADE | 계정에 딸린 것들 |
| `menus.parent_id` → `menus` | CASCADE | 부모 메뉴를 지우면 하위도 간다 |
| **`orders.user_id` → `users`** | **RESTRICT** | **주문 이력이 있는 계정은 누구도 지울 수 없다** (FR-212). 아래 참조 |
| `order_items.order_id` / `payments.order_id` / `shipments.order_id` / `returns.order_id` / `order_agreements.order_id` → `orders` | RESTRICT | **주문 행은 지우지 않는다.** DB가 막는다 |
| `order_items.product_id` → `products` · `order_items.variant_id` → `product_variants` | RESTRICT | 주문된 상품·조합은 물리 삭제 불가. 아래 참조 |
| `order_agreements.terms_id` → `terms` | RESTRICT | 동의 이력이 가리키는 약관이 사라지면 이력이 거짓이 된다 (FR-619) |
| `refunds.order_id` / `refunds.payment_id` | RESTRICT | 돈 기록 |
| `return_items.return_id` → `returns` | CASCADE | 반품 건에 종속 |
| `return_items.order_item_id` → `order_items` | RESTRICT | 〃 돈 기록 |
| `returns.new_variant_id` → `product_variants` | RESTRICT | 교환이 예약한 조합이 사라지면 예약이 잠긴 채 남는다 (D14 「교환 재고」) |
| `product_options.product_id` / `product_variants.product_id` → `products` | CASCADE | 주문된 적 없는 상품만 실제로 지워진다 — 위 RESTRICT가 먼저 걸린다 |
| `carts.user_id` → `users` | CASCADE | 비회원 장바구니는 `user_id`가 NULL |
| `cart_items.cart_id` → `carts` · `cart_items.variant_id` → `product_variants` | CASCADE | **장바구니는 이력이 아니다.** 금액도 동의도 담지 않으므로 조합이 사라지면 항목도 사라진다. RESTRICT로 두면 익명 방문자가 담기만 해도 관리자의 조합 삭제를 막는다 |
| `product_categories.product_id` → `products` | CASCADE | 분류는 이력이 아니다 |
| `product_categories.category_id` → `categories` | RESTRICT | 소속 상품이 있는 카테고리는 삭제 거부 |
| `categories.parent_id` → `categories` | RESTRICT | 하위가 있는 카테고리도 삭제 거부 |
| `payments.return_id` → `returns` | RESTRICT | 돈 기록. 차액 결제가 가리키는 교환 건 |
| `refunds.return_id` → `returns` | RESTRICT | 〃 반품에서 나온 환불의 근거 |
| `refund_items.refund_id` → `refunds` | CASCADE | 환불 건에 종속 |
| `refund_items.order_item_id` → `order_items` | RESTRICT | 돈 기록 |
| `shipments.return_id` → `returns` | RESTRICT | 교환 재발송 송장이 가리키는 건 |
| `webhook_events.order_id` → `orders` | RESTRICT | 주문 행은 지우지 않는다 |
| `product_variants.option_values` (JSONB) | — | **FK가 아니다.** 그룹명 문자열이 키라 `product_options` 삭제와 무관하다 — 찾다가 없다고 놀라지 않도록 적어 둔다 |
| `operation_logs.actor_user_id` → `users` | SET NULL | 로그는 남고 `actor_email` 스냅샷이 누구였는지 말한다 |

**`comments.parent_id`에 RESTRICT를 쓰지 않는 이유.** RESTRICT는 검사를 미룰 수 없다.
글을 지울 때 `post_id` CASCADE가 부모 댓글과 자식 댓글을 **같은 문장에서** 지우는데,
RESTRICT면 그 순간에도 실패한다. NO ACTION은 문장 끝까지 검사를 미룰 수 있어 통과한다.
이 동작은 마이그레이션 통합 테스트로 확인한다 ([D22](22-dev-standards.md)).
**실측 확인(2026-08-03)**: 자식이 있는 댓글의 `DELETE`는 거부되고, 부모 글의 `DELETE`는
부모·자식 댓글을 한 문장에서 함께 지워 통과한다. 그래서 P-210·A-308의 삭제는 툼스톤이 된다.

**`operation_logs.actor_user_id`의 SET NULL을 PostgreSQL은 내부 `UPDATE`로 실행한다.**
그 표에 append-only 트리거를 걸 때 UPDATE를 통째로 막으면 **사용자 삭제(A-402·P-110)가
실패한다.** 트리거는 `actor_user_id → NULL` 전이만 통과시켜야 한다 — 이것도 실측으로 잡았다.

**주문·상품을 RESTRICT로 묶은 이유.** 물리 삭제 경로를 아예 없앤다.

- 판매 중단은 `products.노출 여부 = false`다. 이미 있는 컬럼을 쓴다 — `deleted_at`을
  더하면 "안 보이는 상태"가 둘이 되고, 애플리케이션이 매 조회에 `WHERE deleted_at IS NULL`을
  빠뜨리지 않아야 성립한다. **한 번 빠뜨리면 지운 상품이 되살아난다.** RESTRICT는 DB가 강제한다
- 조합 은퇴도 같다 — `product_variants`에도 `노출 여부`를 둔다. 재고 0은 "품절"이지 "은퇴"가 아니다
- `orders.user_id` RESTRICT는 FR-212를 **본인 탈퇴(P-110)와 관리자 삭제(A-402) 양쪽에**
  동시에 건다. FR-212의 사유(주문의 주체가 사라지면 정산·분쟁 대응이 불가능하다)는
  누가 요청했는지와 무관한데, 애플리케이션 검사를 두 화면에 두 벌 적으면 한쪽만 고쳐진다

### 3-2. 문자열 길이 상한

3절 규칙이 "아래 표에 적는다"고 요구하는 값이다. **핸들러가 검증하고, 값의 단일 출처는
이 표다** — 화면마다 적으면 같은 컬럼에 두 상한이 생긴다 ([D19](19-screen-io.md) C4).

**전 Phase 확정.** 테이블을 더하면 같은 커밋에서 이 표에도 행을 추가한다.

| 컬럼 | 상한 | 근거 |
|---|---|---|
| `users.email` | 254 | RFC 5321의 경로 상한. 더 긴 주소는 전송이 안 된다 |
| `users.display_name` | 100 | [D19](19-screen-io.md) P-001의 `site_name`과 같은 상한을 쓴다 — 새 상수를 늘리지 않는다 |
| `users.password_hash` | 72 (입력) / 60 (저장) | **bcrypt는 72바이트를 넘는 입력을 조용히 잘라낸다.** 입력 상한을 두지 않으면 긴 비밀번호의 뒷부분이 무시되는데 사용자는 모른다. 저장값은 bcrypt 출력 고정 길이 |


**Phase 1**

| 컬럼 | 상한 | 근거 |
|---|---|---|
| `roles.key` | 32 | 한 세그먼트 식별자. 내장 최장이 `anonymous` 9자 |
| `roles.name` · `pages.template` | 50 / 100 | 목록의 한 열 / 테마 템플릿 상대 경로 |
| `roles.description` · `permissions.description` | 200 | D15 2.2 `뜻` 열의 최장이 30자 미만 |
| `permissions.key` | 50 | D15 2.2 최장이 `user.reset_password` 19자 |
| `pages.slug` | 100 | `/{slug}`가 전체 경로다 |
| `pages.title` | 200 | `<title>`·OG 제목이 된다 (FR-511) |
| `pages.body` | 100,000 | 본문 계열. 한 요청이 버퍼링해도 되는 크기 |
| `settings.key` | 80 | 3 세그먼트 + 프로바이더 이름 최악값 |
| `settings.value` | 4,000 | **짧은 스칼라만 담는다.** 긴 본문은 전용 표의 몫. 최장 후보가 OG 이미지 URL(2,048) |
| `menus.title` · `menus.url` | 50 / 500 | 내비게이션 한 항목 / 외부 URL 포함 |
| `social_accounts.provider` | 30 | 코드 허용목록의 키 |
| `social_accounts.provider_uid` | 255 | **프로바이더가 정하는 값이라 우리가 못 정한다** — 관측 최장의 열 배 이상으로 잡는다 |

**Phase 2**

| 컬럼 | 상한 | 근거 |
|---|---|---|
| `boards.slug` · `boards.skin` | 64 | URL 한 세그먼트 / 템플릿 파일명이 된다 |
| `boards.name` · `board_fields.label` | 100 | `display_name`과 같은 상한 — 새 상수를 늘리지 않는다 |
| `board_fields.key` | 32 | 폼 필드명이자 **모든 글의 JSONB 키**라 행 크기에 직접 들어간다 |
| `board_fields.options` | 4,096 바이트 | 항목당 100자 × 최대 50개. **항목 수·길이는 핸들러가, 총량은 DB가** 막는다 |
| `posts.title` | 200 | 메타·OG 제목 + 목록 한 열 |
| `posts.body` | 50,000 | 한글 5만 자 ≈ 146 KiB. 더 큰 문서는 첨부의 몫이다 |
| `posts.custom_fields` | 16,384 바이트 | 필드 8종 어느 것도 장문을 담지 않는다. JSONB는 제약이 약하므로 총량만이라도 DB가 막는다 |
| `comments.body` | 2,000 | 댓글에는 첨부도 커스텀 필드도 없고 P-204가 한 화면에 전부 렌더링한다 |
| `attachments.stored_path` | 128 | `YYYY/MM/` + uuid 36자 + 여유 |
| `attachments.original_name` | 255 | 대부분 파일시스템의 파일명 상한 |
| `attachments.mime_type` | 128 | 허용목록 밖 값이 들어올 수 없다 |
| `operation_logs.actor_email` | 254 | `users.email`과 같은 상한 |
| `operation_logs.action` · `target_type` · `target_id` | 64 / 32 / 64 | 정규식과 함께. `target_id`는 uuid 36자 + 슬러그·설정 키를 함께 담는다 |
| `operation_logs.summary` | 500 | 한 줄 요약. 원문은 로그 파일이 갖는다 |

**Phase 3**

| 컬럼 | 상한 | 근거 |
|---|---|---|
| `products.name` · `order_items.product_name` | 200 | **같아야 한다.** 스냅샷으로 복사되므로 다르면 잘린다 |
| `products.slug` · `categories.slug` · `categories.name` | 100 | URL 세그먼트 / 이름 계열 |
| `products.description` · `terms.body` | 20,000 | 본문 계열. 상품 1만 개 × 20KB ≈ 200MB — 이보다 크면 디스크 계획이 불가능하다 |
| `product_options.name` | 50 | 옵션 라벨 한 줄 |
| `product_variants.sku` | 64 | 외부 창고·정산 코드 |
| `orders.order_no` · `returns.return_no` | 32 | 서버 생성값이지만 **사람이 입력한다**(P-504) |
| `orders.receiver_name` | 100 | 이름 계열 |
| `orders.receiver_phone` · `orderer_phone` | 20 | 국가번호·하이픈 포함 |
| `orders.postcode` | 10 | 한국 5자리 + 여유 |
| `orders.address1` · `address2` · `delivery_memo` | 200 | 도로명 전체 / 상세 / 요청사항 |
| `orders.orderer_email` | 254 | `users.email`과 같은 상한 |
| `order_items.option_label` | 200 | 조합 표기 스냅샷 |
| `payments.pg` · `shipments.carrier` · `webhook_events.pg` | 32 | 어댑터·택배사 슬러그 |
| `payments.payment_key` · `webhook_events.event_id` | 200 | **PG가 정하는 값**이라 사양이 PG마다 다르다 |
| `refunds.reason` · `returns.reason` · `reject_reason` · `webhook_events.error` | 500 | 사유 계열 |
| `refunds.request_key` | 100 | 서버 발급 멱등 키 |
| `shipments.tracking_no` | 64 | 택배사 송장 최대 표기 |
| `terms.kind` · `terms.version` | 50 / 20 | D19 A-207이 이미 "50자"로 명시 |
| `carts.guest_key` | 64 | 난수 base64 |

> **상한 없는 `text`는 규칙 위반이다.** PostgreSQL의 `text`는 무제한이라 상한을 안 정하면
> 폼 하나가 디스크를 채운다 — 1 vCPU / 512MB 인스턴스(NFR-101)에서는 그것으로 충분하다.

### 3-3. 테이블 목록

**이 표가 테이블 이름의 단일 출처다.** 아래 스키마 절과 [D16 데이터 커버리지](16-data-coverage.md)가
이 목록을 기준으로 대조되고, 어긋나면 `make check`가 실패한다.

| Phase | 테이블 |
|---|---|
| Phase 0 | `users` · `sessions` |
| Phase 1 | `roles` · `permissions` · `role_permissions` · `user_roles` · `pages` · `settings` · `menus` · `password_reset_tokens` · `email_verification_tokens` · `social_accounts` |
| Phase 2 | `boards` · `board_fields` · `posts` · `comments` · `attachments` · `operation_logs` |
| Phase 3 | `products` · `product_options` · `product_variants` · `categories` · `product_categories` · `carts` · `cart_items` · `orders` · `order_items` · `payments` · `refunds` · `refund_items` · `shipments` · `returns` · `return_items` · `webhook_events` · `terms` · `order_agreements` |

전 36개. 테이블을 더하면 **같은 커밋에서** 이 표와 D16에 함께 추가한다.

## 마이그레이션 규칙

- 도구: `pressly/goose/v3`. 파일은 `internal/migrations/*.sql`, `embed.FS`로 바이너리에 내장
- 실행: 설치 시 1회 + **매 부팅 시 대기분 자동 적용** (NFR-302). 별도 CLI 단계 없음
- **모든 마이그레이션에 `-- +goose Down`을 쓴다** (NFR-303). 되돌릴 수 없으면 그 사실을
  주석과 [CHANGELOG.md](../CHANGELOG.md)에 명시한다 (NFR-308)
- 파일명은 `NNNNN_설명.sql`. 번호는 재사용하지 않는다
- **배포된 마이그레이션은 수정하지 않는다.** 고칠 게 있으면 새 마이그레이션을 추가한다
- 파괴적 변경(컬럼 삭제·타입 변경)은 두 릴리즈에 나눠 적용한다:
  릴리즈 N에서 새 컬럼 추가 + 양쪽 쓰기 → 릴리즈 N+1에서 옛 컬럼 삭제.
  이렇게 해야 다운그레이드 경로가 남는다

## 현재 스키마

`internal/migrations/00001_init.sql` (Phase 0) · `00002`~`00005` (Phase 1)

> **아래 「계획된 스키마」의 Phase 1 절은 2026-08-04에 실제 SQL이 됐다.** 그 절의 컬럼
> 정의는 이제 계획이 아니라 마이그레이션과 대조되는 명세다 — 어긋나면 `make check`가
> 실패한다. Phase 2·3은 아직 문서뿐이라 대조 대상이 아니다.

### users

| 컬럼 | 타입 | 비고 |
|---|---|---|
| `id` | uuid PK | |
| `email` | text UNIQUE NOT NULL | 소문자로 저장 |
| `password_hash` | text NOT NULL | bcrypt (NFR-208) |
| `display_name` | text NOT NULL DEFAULT '' | |
| `is_admin` | boolean NOT NULL DEFAULT false | **임시.** Phase 1에서 RBAC로 교체 |
| `created_at` / `updated_at` | timestamptz | |

### sessions

`scs/pgxstore`가 요구하는 스키마 그대로. 우리가 정한 것이 아니므로 바꾸지 않는다.

| 컬럼 | 타입 |
|---|---|
| `token` | text PK |
| `data` | bytea NOT NULL |
| `expiry` | timestamptz NOT NULL (+ 인덱스) |

## 계획된 스키마

**컬럼·타입·제약까지 확정돼 있다.** 해당 Phase 착수 시 이 절을 그대로 마이그레이션으로 옮긴다.

**Phase 1은 옮겨졌다** (`00002_rbac.sql`·`00003_rbac_seed.sql`·`00004_content.sql`·`00005_auth_tokens.sql`).
아래 Phase 1 절과 그 SQL은 `checkdocs.sh`가 컬럼 단위로 대조한다.

읽는 규칙 셋:

| 규칙 | 내용 |
|---|---|
| `ON DELETE` | **여기 적지 않는다.** 3-1 표가 단일 출처다 |
| 길이 상한 | **여기 적지 않는다.** 3-2 표가 단일 출처다 |
| 식별자·값 | 컬럼명은 **영문 snake_case**, 값 리터럴은 **한국어**다. 주문 상태의 단일 출처가 [D14](14-screen-flows.md) 5절(한국어)이고, 거기서 갈라지면 같은 개념에 두 어휘가 생긴다 |

`text` 컬럼에 길이 `CHECK`를 걸지 않는다 — 검증은 핸들러가 한다(3절). 상한을 DB에 넣으면
상한 하나를 고칠 때마다 「파괴적 변경은 두 릴리즈」 절차를 타게 된다. `CHECK`는 **값 집합·부호
같은 도메인 제약에만** 쓴다. 값 집합이 `CHECK IN (...)`으로 고정된 컬럼은 3-2에 행을 두지
않는다 — 값 집합이 곧 상한이다.

### Phase 1 — 인증·권한·페이지·설정 (FR-2xx, FR-4xx, FR-7xx)

**`users` 추가분**

| 컬럼 | 타입 | 제약 | 비고 |
|---|---|---|---|
| `is_active` | boolean | NOT NULL DEFAULT true | 삭제보다 비활성이 기본 ([D15](15-access-control.md) 5.3) |
| `sessions_valid_from` | timestamptz | NOT NULL DEFAULT now() | 이 시각보다 오래된 세션을 다음 요청에서 거부 (D15 5.4) |
| `email_verified_at` | timestamptz | NULL | NULL이면 미인증 (FR-214) |

`sessions_valid_from`을 NULL 허용으로 두면 "컷오프 없음"이 NULL이 되어 비교가 **fail-open**이
된다. NOT NULL + 기본값이면 판정이 언제나 단순 비교 하나다.

**`email_verified_at`을 `email_verification_tokens`로 대신할 수 없다.** 토큰 표는 "인증 링크를
보냈다"를 기록하지 "이 계정이 인증됐다"를 기록하지 않는다 — 토큰은 1회용이고 만료되며 정리
대상이다. 인증 상태를 토큰 존재로 판정하면 **토큰을 지우는 순간 전원이 미인증으로 되돌아간다.**
이메일 인증을 끈 사이트에서는 이 컬럼을 보지 않고 값도 그대로 둔다 — 껐다 켜도 이미 인증한
사람이 다시 인증하지 않게 하기 위해서다.

**`roles`**

| 컬럼 | 타입 | 제약 |
|---|---|---|
| `id` | uuid | PK DEFAULT `gen_random_uuid()` |
| `key` | text | NOT NULL UNIQUE, `CHECK (key ~ '^[a-z][a-z0-9_]*$')` |
| `name` | text | NOT NULL |
| `description` | text | NOT NULL DEFAULT '' |
| `is_builtin` | boolean | NOT NULL DEFAULT false |
| `is_superuser` | boolean | NOT NULL DEFAULT false |
| `is_assignable` | boolean | NOT NULL DEFAULT true |
| `created_at` / `updated_at` | timestamptz | NOT NULL DEFAULT now() |

```sql
CREATE UNIQUE INDEX roles_one_superuser_idx ON roles ((is_superuser)) WHERE is_superuser;
```

`key`에 점을 쓰지 않는다 — 점은 권한 키의 `<리소스>.<동작>` 구분자다(D15 2.1). 역할 키에
허용하면 A-403·A-404가 한 화면에서 두 종류 키를 보여줄 때 구분이 불가능해진다.
`is_assignable`이 `anonymous`·`member`를 막는다: 둘은 암묵 부여라 `user_roles` 행이 생기면
안 되는데, 그걸 DB에서 지키는 수단이 이 컬럼뿐이다.

**`permissions`**

| 컬럼 | 타입 | 제약 |
|---|---|---|
| `id` | uuid | PK |
| `key` | text | NOT NULL UNIQUE, `CHECK (key ~ '^[a-z][a-z0-9]*\.[a-z][a-z0-9_]*$')` |
| `description` | text | NOT NULL |
| `is_scoped` | boolean | NOT NULL DEFAULT false |
| `created_at` / `updated_at` | timestamptz | NOT NULL DEFAULT now() |

**Phase 1 마이그레이션은 D15 2.2의 `Phase = 1`인 19개만 시딩한다.** 37개를 전부 심으면
D15 4.4의 "어떤 라우트도 쓰지 않는 권한 → 경고" 검사가 매 부팅 18건을 뱉어 **검사 자체가
무시된다.** `is_scoped` 컬럼은 Phase 1에 둔다 — 대상 권한 6개는 전부 Phase 2지만, 부여
핸들러와 시드가 Phase마다 다른 모양이 되면 안 된다(D15 2.4).

**`role_permissions`**

| 컬럼 | 타입 | 제약 |
|---|---|---|
| `id` | uuid | PK |
| `role_id` | uuid | NOT NULL REFERENCES `roles(id)` |
| `permission_id` | uuid | NOT NULL REFERENCES `permissions(id)` |
| `board_id` | uuid | NULL REFERENCES `boards(id)` ON DELETE CASCADE |
| `created_at` / `updated_at` | timestamptz | NOT NULL DEFAULT now() |

```sql
-- Phase 1 (boards 가 아직 없다)
ALTER TABLE role_permissions ADD CONSTRAINT role_permissions_uniq
  UNIQUE (role_id, permission_id);

-- Phase 2: board_id 추가와 같은 마이그레이션에서 위 제약을 교체한다
ALTER TABLE role_permissions DROP CONSTRAINT role_permissions_uniq;
ALTER TABLE role_permissions ADD CONSTRAINT role_permissions_uniq
  UNIQUE NULLS NOT DISTINCT (role_id, permission_id, board_id);
CREATE INDEX role_permissions_permission_id_idx ON role_permissions (permission_id);
-- 부분 인덱스다. 전역 부여(board_id IS NULL)가 행의 대부분이고 스코프 판정은
-- board_id 가 있는 행만 찾으므로, NULL 행을 색인에 넣으면 크기만 커지고 답은
-- 같다.
CREATE INDEX role_permissions_board_id_idx ON role_permissions (board_id)
  WHERE board_id IS NOT NULL;
```

> **`NULLS NOT DISTINCT`가 필요한 이유.** 기본 동작에서 NULL은 서로 같지 않으므로 평범한
> `UNIQUE (role_id, permission_id, board_id)`는 **전역 부여(`board_id IS NULL`)를 무한히 중복
> 삽입시킨다.** PostgreSQL 15+가 이 절을 제공하고 우리 최소 버전은 18이다(3절) — 그 이전
> 버전이었다면 `IS NULL`/`IS NOT NULL`로 갈린 부분 유니크 인덱스 **두 개**가 유일한 수단이었다.
>
> `board_id`를 Phase 1에 둘 수 없다: `boards`가 Phase 2 테이블이고, 3절이 FK에 `REFERENCES`
> 명시를 요구한다. 존재하지 않는 테이블을 가리킬 수 없다.

**`user_roles`**

| 컬럼 | 타입 | 제약 |
|---|---|---|
| `id` | uuid | PK |
| `user_id` | uuid | NOT NULL REFERENCES `users(id)` |
| `role_id` | uuid | NOT NULL REFERENCES `roles(id)` |
| `created_at` / `updated_at` | timestamptz | NOT NULL DEFAULT now() |

```sql
ALTER TABLE user_roles ADD CONSTRAINT user_roles_uniq UNIQUE (user_id, role_id);
CREATE INDEX user_roles_role_id_idx ON user_roles (role_id);
```

**`granted_by` 컬럼을 두지 않는다** — 역할 부여·회수는 D15 7절이 작업 로그 필수 대상으로
못박았다. 컬럼을 더 두면 "누가 줬나"의 출처가 둘이 되어 갈라진다. `role_id` 인덱스는 성능이
아니라 A-403의 "부여된 사용자가 있는 역할 삭제 거부"가 곧 그 기준 카운트이기 때문이다.

**`pages`**

| 컬럼 | 타입 | 제약 |
|---|---|---|
| `id` | uuid | PK |
| `slug` | text | NOT NULL UNIQUE |
| `title` | text | NOT NULL |
| `body` | text | NOT NULL |
| `status` | text | NOT NULL DEFAULT 'draft', `CHECK (status IN ('draft','published'))` |
| `template` | text | NOT NULL DEFAULT '' |
| `created_at` / `updated_at` | timestamptz | NOT NULL DEFAULT now() |

`UNIQUE (slug)`가 A-302의 동시성 수단이다 — 사전 조회 후 INSERT는 동시 요청 둘을 함께
통과시킨다. **`author_id`·`published_at`·`deleted_at`을 두지 않는다**: 소비하는 화면이 없고,
"누가 바꿨나"의 출처는 D15 7절이 지정한 `operation_logs`다.

**`settings`**

| 컬럼 | 타입 | 제약 |
|---|---|---|
| `key` | text | **PRIMARY KEY** |
| `value` | text | NOT NULL |
| `created_at` / `updated_at` | timestamptz | NOT NULL DEFAULT now() |

키 형식: `^[a-z][a-z0-9]*(\.[a-z][a-z0-9_]*){1,2}$` (2~3 세그먼트. 3번째는 소셜 프로바이더 등
인스턴스 식별자가 필요할 때만).

| 키 | 값 | 화면 | Phase |
|---|---|---|---|
| `site.name` · `site.meta_description` · `site.og_image` · `site.dev_mode` · `site.type` | text / text / text / bool / `cms`\|`shop` | A-201 | 1 |
| `auth.email_verification_required` | bool | A-201 | 1 |
| `mail.smtp_host` · `smtp_port` · `smtp_user` · `smtp_password` · `tls_mode` · `from_address` · `from_name` | text / int / text / **자격증명** / `none`\|`starttls`\|`tls` / text / text | A-205 | 1 |
| `social.{provider}.client_id` · `.client_secret` · `.enabled` | text / **자격증명** / bool | A-206 | 1 |
| `business.*` | text | A-208 | 3 |
| `commerce.return_window_days` · `auto_confirm_days` · `return_shipping_payer` · `return_shipping_fee` | int(7) / int(8) / `차감`\|`별도청구` / int(0) | A-512 | 3 |

**PK가 `uuid`가 아니라 `key`다 — 3절 규칙의 의도적 예외다.** 이 행은 어떤 FK의 대상도 아니고
모든 접근이 `WHERE key = $1`이다. uuid를 얹으면 식별자가 둘이 되고 upsert의 `ON CONFLICT`
대상과 PK가 갈라진다.

**`type`·`is_secret` 컬럼을 두지 않는다.** 어느 키가 어떤 타입이고 어느 것이 자격증명인지는
**코드가 안다** — 범용 설정 편집 화면이 없고 A-201/205/206/208/512가 각자 자기 키만 쓴다.
컬럼으로 두면 코드 상수와 DB 데이터라는 두 출처가 생기고, `is_secret`이 UI로 뒤집히면
D15 7절의 "자격증명 값은 로그에 남기지 않는다"가 데이터에 의존하게 된다.

**`menus`**

| 컬럼 | 타입 | 제약 |
|---|---|---|
| `id` | uuid | PK |
| `title` | text | NOT NULL |
| `url` | text | NOT NULL, `CHECK (url ~ '^(/([^/\\].*)?|https?://[^\s]+)$')` |
| `parent_id` | uuid | NULL REFERENCES `menus(id)` |
| `sort_order` | integer | NOT NULL DEFAULT 0 |
| `created_at` / `updated_at` | timestamptz | NOT NULL DEFAULT now() |

```sql
CREATE INDEX menus_parent_sort_idx ON menus (parent_id, sort_order);
```

**링크 대상은 한 컬럼이다.** `link_type` 판별 컬럼을 두지 않는다 — 내부/외부는 `/`로 시작하는지로
한 줄에 복원되고, 저장해 두면 값과 어긋날 수 있는 두 번째 출처가 된다.

`CHECK`는 핸들러 검증의 복제가 아니라 **fail-closed 백스톱**이다. `javascript:`·`data:`뿐 아니라
**프로토콜 상대 URL(`//evil.com`)** 까지 막는다 — `//`는 `/`로 시작하므로 "내부 경로는 `/`로
시작"만 검사하는 구현에서 가장 흔하게 새는 구멍이고, P-101의 `next` 검증이 같은 이유로
`//`·`\`를 거부한다.

**`password_reset_tokens` · `email_verification_tokens`**

| 컬럼 | 타입 | 제약 |
|---|---|---|
| `id` | uuid | PK |
| `user_id` | uuid | NOT NULL REFERENCES `users(id)` |
| `token_hash` | **bytea** | NOT NULL UNIQUE — SHA-256(32바이트 `crypto/rand` 원문) |
| `expires_at` | timestamptz | NOT NULL — 재설정 **30분**, 인증 **24시간** |
| `used_at` | timestamptz | NULL. NULL = 미사용 |
| `created_at` / `updated_at` | timestamptz | NOT NULL DEFAULT now() |

```sql
CREATE INDEX password_reset_tokens_user_id_idx     ON password_reset_tokens (user_id);
CREATE INDEX email_verification_tokens_user_id_idx ON email_verification_tokens (user_id);
```

> **bcrypt를 쓰면 안 된다.** "해시를 저장한다"만 보고 비밀번호와 같은 함수를 쓰는 것이 자연스러운
> 실수인데, bcrypt는 행마다 솔트가 달라 **해시로 조회할 수 없다** — P-105가 전 토큰을 스캔하며
> bcrypt 비교를 돌게 된다. 토큰은 256비트 난수라 사전 공격 대상이 아니므로 **솔트 없는
> SHA-256이 옳고**, 그래야 `UNIQUE (token_hash)`로 조회 한 번에 끝난다.

두 표를 `kind` 컬럼으로 합치지 않는다 — 만료·재사용 정책이 다르고(30분 vs 24시간), 합치면
만료 상수가 행 데이터에 따라 갈라지는 분기가 조회 경로마다 생긴다.

**`social_accounts`**

| 컬럼 | 타입 | 제약 |
|---|---|---|
| `id` | uuid | PK |
| `user_id` | uuid | NOT NULL REFERENCES `users(id)` |
| `provider` | text | NOT NULL |
| `provider_uid` | text | NOT NULL |
| `created_at` / `updated_at` | timestamptz | NOT NULL DEFAULT now() |

```sql
ALTER TABLE social_accounts ADD CONSTRAINT social_accounts_provider_uid_uniq UNIQUE (provider, provider_uid);
ALTER TABLE social_accounts ADD CONSTRAINT social_accounts_user_provider_uniq UNIQUE (user_id, provider);
```

유니크가 **둘**이다. 앞은 소셜 계정 하나가 우리 계정 둘에 붙는 것을 막고, 뒤는 **P-111의 삭제
술어가 전제하는 것**이다 — `DELETE ... WHERE user_id=$세션 AND provider=$1`이 정확히 한 행을
지운다는 보장이 이 제약뿐이고, 없으면 같은 프로바이더 계정을 둘 붙인 뒤 해제 한 번에 둘 다
사라진다.

### Phase 2 — 게시판 (FR-5xx)

> 이 절의 DDL은 **실제 PostgreSQL 컨테이너에 적용해 검증했다** (2026-08-03).
> 아래 「측정한 것」 항목은 전부 실행 결과다.

**`boards`**

| 컬럼 | 타입 | 제약 |
|---|---|---|
| `id` | uuid | PK |
| `slug` | text | NOT NULL UNIQUE, `CHECK (slug ~ '^[a-z0-9][a-z0-9-]*$')` |
| `name` | text | NOT NULL |
| `skin` | text | NOT NULL DEFAULT '' |
| `allow_attachments` | boolean | NOT NULL DEFAULT false |
| `allow_comments` | boolean | NOT NULL DEFAULT true |
| `allow_secret` | boolean | NOT NULL DEFAULT false |
| `per_page` | integer | NOT NULL DEFAULT 20, `CHECK (per_page BETWEEN 1 AND 100)` |
| `created_at` / `updated_at` | timestamptz | NOT NULL DEFAULT now() |

권한 컬럼을 두지 않는다 — 스코프는 `role_permissions.board_id`가 표현한다(D15 2.4).
A-305의 프리셋은 같은 트랜잭션에서 `role_permissions` 행을 넣을 뿐 `boards`에 흔적을 남기지 않는다.

**`board_fields`**

| 컬럼 | 타입 | 제약 |
|---|---|---|
| `id` | uuid | PK |
| `board_id` | uuid | NOT NULL REFERENCES `boards(id)` |
| `key` | text | NOT NULL, `CHECK (key ~ '^[a-z][a-z0-9_]*$')` |
| `label` | text | NOT NULL |
| `field_type` | text | NOT NULL, `CHECK (field_type IN ('text','textarea','number','select','checkbox','multiselect','date','url'))` |
| `is_required` · `show_in_list` | boolean | NOT NULL DEFAULT false |
| `options` | jsonb | NOT NULL DEFAULT '[]' |
| `sort_order` | integer | NOT NULL DEFAULT 0 |
| `created_at` / `updated_at` | timestamptz | NOT NULL DEFAULT now() |

```sql
ALTER TABLE board_fields ADD CONSTRAINT board_fields_key_uniq UNIQUE (board_id, key);
ALTER TABLE board_fields ADD CONSTRAINT board_fields_options_shape
  CHECK (jsonb_typeof(options) = 'array' AND octet_length(options::text) <= 4096);
ALTER TABLE board_fields ADD CONSTRAINT board_fields_options_when
  CHECK ((field_type IN ('select','multiselect')) = (options <> '[]'::jsonb));
```

**타입을 PG `enum`이 아니라 `text` + `CHECK`로 둔다.** `ALTER TYPE ... ADD VALUE`는 되돌릴 수
없어 NFR-303(모든 마이그레이션에 `Down`)과 부딪힌다. `CHECK`는 DROP/ADD로 완전히 되돌아간다.

**예약 키(`id` `title` `body` `author_id` `board_id`)는 DB로 막지 않는다** — 목록이 컬럼 추가마다
늘고, 핸들러가 422로 거부한다(A-306).

`UNIQUE (board_id, key)`의 선두 컬럼이 `board_id`라 FK 인덱스 규칙을 이 인덱스가 만족한다.

**`posts`**

| 컬럼 | 타입 | 제약 |
|---|---|---|
| `id` | uuid | PK |
| `board_id` | uuid | NOT NULL REFERENCES `boards(id)` |
| `author_id` | uuid | **NULL 허용** REFERENCES `users(id)` — 3-1이 SET NULL이다 |
| `title` · `body` | text | NOT NULL |
| `custom_fields` | jsonb | NOT NULL DEFAULT '{}' |
| `status` | text | NOT NULL DEFAULT 'published', `CHECK (status IN ('published','hidden'))` |
| `is_pinned` · `is_secret` | boolean | NOT NULL DEFAULT false |
| `view_count` | integer | NOT NULL DEFAULT 0, `CHECK (view_count >= 0)` |
| `search_vector` | tsvector | `GENERATED ALWAYS AS (to_tsvector('simple'::regconfig, title \|\| ' ' \|\| body)) STORED` |
| `created_at` / `updated_at` | timestamptz | NOT NULL DEFAULT now() |

```sql
CHECK (jsonb_typeof(custom_fields) = 'object' AND octet_length(custom_fields::text) <= 16384)

CREATE INDEX posts_board_list_idx ON posts (board_id, is_pinned DESC, created_at DESC, id DESC);
CREATE INDEX posts_author_id_idx  ON posts (author_id);
CREATE INDEX posts_search_idx     ON posts USING GIN (search_vector);  -- 00008
```

**측정한 것 — 인덱스 끝의 `id`는 FR-508 때문이다.** 20,000행에서 `LIMIT 20 OFFSET 19000`은
이 인덱스가 있어도 **Seq Scan + 19,020행 정렬**로 떨어졌다. 같은 조건의 키셋 질의
`(is_pinned, created_at, id) < (...)`는 **Index Only Scan, 실제 접근 20행**이었다. 즉 FR-508은
인덱스가 아니라 **키셋 페이지네이션**이 충족하고, `id`가 그 tiebreaker다. 세 정렬 컬럼의 방향이
전부 DESC로 같아야 행 비교가 인덱스 조건으로 내려간다.

**`custom_fields`에 GIN 인덱스를 만들지 않는다.** FR-509("커스텀 필드 기준 정렬")에 **GIN은
아무 도움이 되지 않는다** — GIN은 포함 연산자만 지원하고 순서를 제공하지 않는다. 정렬을
인덱스로 받으려면 필드마다 표현식 B-tree가 필요한데 그건 게시판마다 인덱스를 만드는 것이고
[DEC-3.9](../.ai/DECISIONS.md)가 금지한 동적 DDL과 같은 문제다. `board_id` 필터 + `LIMIT` 뒤
메모리 정렬로 간다(FR-509는 `선택`). **정렬 키는 파라미터 바인딩된다** — `ORDER BY
custom_fields->>$1`이 문자열 연결 없이 동작하는 것을 확인했다(NFR-202 유지). 그래도
`board_fields`에 있는 key인지 검사한다.

**측정한 것 — GIN 인덱스는 90,000행부터 선택된다 (2026-08-05, PG 18).** 본문이 서로 다른
90,000행에서 `게시판:*`이 `Bitmap Index Scan on posts_search_idx`로 계획됐고, 70,000행에서는
여전히 Seq Scan 이었다. 두 가지가 이 숫자를 만든다 — ① **접두 tsquery 의 기본 선택도는 2%
고정**이라 추정 행수가 테이블과 함께 커진다. "행이 많아지면 언젠가 인덱스를 탄다"가 저절로
성립하지 않는다는 뜻이다. ② **모든 행이 같은 단어를 담으면 통계가 무너져 어떤 크기에서도
Seq Scan 이다** — `제목 1`·`본문 1` 같은 연번 텍스트로 15만 행을 넣었을 때 인덱스를 타지
않았다. 실제 게시판은 글마다 본문이 다르므로 실측도 다르게 만들어야 한다.

이 수치는 튜닝 대상이 아니라 **작은 사이트에서 Seq Scan 이 나오는 것이 정상**이라는 뜻이다.
검색이 느리다는 신고가 오면 먼저 행 수를 본다.

**측정한 것 — 전문검색은 `simple` config + 접두 질의여야 한다.** 스톡 PostgreSQL에 한국어 사전이
없고 `english`는 한국어를 망가뜨린다. 본문 "게시판을 새로 열었습니다"에 대해
`to_tsquery('simple','게시판')`은 **0건**, `'게시판:*'`은 **1건**이었다 — 조사 문제를 접두 질의가
덮는다. 생성 컬럼이 성립하려면 **2인자 `to_tsvector(regconfig, text)`** 여야 한다(1인자는
IMMUTABLE이 아니다).

`deleted_at`을 두지 않는다 — 물리 삭제다. "안 보이는 상태"를 둘(`status='hidden'`, `deleted_at`)로
만들면 매 조회에서 한쪽을 빠뜨리는 순간 지운 글이 되살아난다.

**`comments`**

| 컬럼 | 타입 | 제약 |
|---|---|---|
| `id` | uuid | PK |
| `post_id` | uuid | NOT NULL REFERENCES `posts(id)` |
| `parent_id` | uuid | NULL REFERENCES `comments(id)` |
| `author_id` | uuid | NULL REFERENCES `users(id)` |
| `body` | text | NOT NULL. 툼스톤일 때 `''` |
| `deleted_at` | timestamptz | NULL — **툼스톤 표시** |
| `created_at` / `updated_at` | timestamptz | NOT NULL DEFAULT now() |

```sql
CHECK (parent_id IS NULL OR parent_id <> id)
CHECK (deleted_at IS NULL OR body = '')

CREATE INDEX comments_post_id_idx   ON comments (post_id, created_at);
CREATE INDEX comments_parent_id_idx ON comments (parent_id) WHERE parent_id IS NOT NULL;
CREATE INDEX comments_author_id_idx ON comments (author_id);
```

**측정한 것 — 툼스톤은 설계 선택이 아니라 FK가 강제하는 결과다.** 3-1의 `NO ACTION`을 실제로
걸고 확인했다: 자식이 있는 댓글을 `DELETE`하면 **거부된다**(FK 위반). 같은 스키마에서 부모 글을
지우면 부모·자식 댓글이 한 문장에서 함께 사라져 **통과한다**. 따라서 P-210·A-308의 삭제는
**자식이 없으면 물리 삭제, 있으면 `deleted_at` + `body=''`** 두 갈래다.

본문을 DB에서 비우는 이유는 표시를 코드의 `if` 하나에 걸지 않기 위해서다 — 테마는 제3자가
작성하고 작성자가 `if`를 빠뜨리는 것을 전제해야 한다. `author_id`는 유지한다(A-308의 분쟁 근거).

**순환 방지 락이 필요 없다.** 3절의 자기 참조 트리 규칙은 `menus`·`categories`를 지목한다.
`comments.parent_id`는 **삽입 시점에 확정되고 이후 어떤 화면도 받지 않으므로**(P-209) 순환이
구조적으로 만들어지지 않는다.

**`attachments`**

| 컬럼 | 타입 | 제약 |
|---|---|---|
| `id` | uuid | PK |
| `post_id` | uuid | NOT NULL REFERENCES `posts(id)` |
| `stored_path` | text | NOT NULL **UNIQUE**, `CHECK (stored_path ~ '^[0-9]{4}/[0-9]{2}/[0-9a-f-]{36}$')` |
| `original_name` | text | NOT NULL — **표시 전용** |
| `mime_type` | text | NOT NULL — 서버가 매직바이트로 산출 |
| `byte_size` | bigint | NOT NULL, `CHECK (byte_size > 0)` |
| `created_at` | timestamptz | NOT NULL DEFAULT now() |

**저장 경로는 `YYYY/MM/<uuid>` 상대 경로이고 확장자를 붙이지 않는다.** 절대 경로를 저장하면
운영자가 업로드 디렉터리를 옮기는 순간 전 행이 죽는다. 확장자를 떼는 것은 [D60](60-security.md)
3항의 "파일명 재생성"을 끝까지 미는 것이다 — 웹루트 밖이 어쩌다 서빙되더라도 **실행 가능한
이름이 디스크에 없다.** 표시용 확장자는 `original_name`, Content-Type은 `mime_type`이 갖는다.
`CHECK` 정규식이 `../` 형태를 DB에서 거부하는 것을 확인했다.

`stored_path` UNIQUE: 두 행이 같은 파일을 가리키면 한쪽을 지울 때 다른 쪽 실물이 조용히
사라진다. A-309가 "디스크 삭제 실패를 롤백하지 않는다"고 정한 이상 반대 방향은 DB가 막아야 한다.

`updated_at`을 두지 않는다(3절 예외) — 생성과 삭제만 있고 변경 화면이 없다.

**`operation_logs`**

| 컬럼 | 타입 | 제약 |
|---|---|---|
| `id` | uuid | PK |
| `actor_user_id` | uuid | NULL REFERENCES `users(id)` |
| `actor_email` | text | NOT NULL DEFAULT '' — 스냅샷. `''` = 시스템 주체 |
| `action` | text | NOT NULL, `CHECK (action ~ '^[a-z][a-z0-9]*\.[a-z][a-z0-9_]*$')` |
| `target_type` | text | NOT NULL, `CHECK (target_type ~ '^[a-z][a-z0-9_]*$')` |
| `target_id` | **text** | NULL |
| `summary` | text | NOT NULL DEFAULT '' |
| `ip` | **inet** | NULL |
| `created_at` | timestamptz | NOT NULL DEFAULT now() |

```sql
CREATE INDEX operation_logs_created_at_idx ON operation_logs (created_at DESC);
CREATE INDEX operation_logs_actor_idx      ON operation_logs (actor_user_id);
```

**`action`·`target_type`을 DB 열거형으로 고정하지 않는다.** 값 집합의 단일 출처는 D15 7절
표이고 그 표는 Phase 3까지 늘어난다 — `CHECK` 열거면 **동작 하나 추가마다 마이그레이션**이
필요하고, 그 마이그레이션이 "수정·삭제하지 않는" 이 표를 잠근다. 값 집합을 좁히는 마이그레이션은
과거 행을 위반 상태로 만들어 NFR-303(모든 마이그레이션에 `Down`)과 정면으로 부딪힌다.
대신 **형태를 `CHECK`로 고정**했고, `action`은 D15 2.1의 권한 키와 **같은 2세그먼트 정규식**을
쓴다(새 명명 규칙을 만들지 않는다).

**보존 기간을 두지 않는다 — 지우지 않는다.** 정리 잡은 NFR-103(별도 워커·크론 없음)과
부딪히고, 앞의 append-only 트리거가 `DELETE` 를 이미 거부한다. 정리를 하려면 트리거를
우회하는 경로를 만들어야 하는데, 그 경로가 있으면 append-only 는 더 이상 보장이 아니다
(D13 A-601 은 이 표를 "수정·삭제하지 않는다"고 못박았다). 표가 커져 문제가 되면 그때
필요한 것은 삭제가 아니라 파티션이고, 그것은 별도 요구사항으로 연다.

**`target_id`가 uuid가 아닌 이유**: 대상이 항상 uuid 행이 아니다 — A-201 설정(키), A-202 테마
활성화(테마 이름), A-205 메일 설정은 uuid PK를 갖지 않는다. uuid 컬럼이면 D15 7절이 "반드시
기록"으로 지정한 그 항목들이 **대상 없이** 남는다.

**append-only는 트리거로 강제한다.**

```sql
CREATE TRIGGER operation_logs_no_delete BEFORE DELETE ON operation_logs
    FOR EACH ROW EXECUTE FUNCTION operation_logs_append_only();
CREATE TRIGGER operation_logs_no_update BEFORE UPDATE ON operation_logs
    FOR EACH ROW WHEN (NEW.actor_user_id IS NOT NULL OR OLD.actor_user_id IS NULL)
    EXECUTE FUNCTION operation_logs_append_only();
```

> **측정한 것 — 단순한 `BEFORE UPDATE OR DELETE` 트리거는 사용자 삭제를 통째로 막았다.**
> PostgreSQL은 `ON DELETE SET NULL`을 내부 `UPDATE ONLY operation_logs SET actor_user_id = NULL`로
> 실행한다. UPDATE 전체를 막으면 3-1이 정한 SET NULL이 실패하고 `DELETE FROM users`가 에러를 낸다.
> 위 `WHEN` 절 형태로 재검증했다: 앱의 UPDATE·DELETE는 거부되고, 사용자 삭제는 성공하며,
> `actor_email` 스냅샷이 남고, 그 뒤에도 행은 여전히 수정 불가다.
>
> 한계: 앱이 테이블 소유자로 접속하므로 `DROP TRIGGER`가 가능하다. 이 트리거는 **공격자가
> 아니라 우리 코드의 사고**를 막는다.

### Phase 3 — 커머스 (FR-6xx)

**`products` · `product_options` · `product_variants`**

**`products`**

| 컬럼 | 타입 | 제약 |
|---|---|---|
| `id` | uuid | PK |
| `slug` | text | NOT NULL UNIQUE, `CHECK (slug ~ '^[a-z0-9][a-z0-9-]*$')` |
| `name` | text | NOT NULL |
| `description` | text | NOT NULL DEFAULT '' |
| `base_price` | integer | NOT NULL `CHECK (>= 0)` — 정수 minor unit |
| `is_visible` | boolean | NOT NULL **DEFAULT false** |
| `search_tsv` | tsvector | GENERATED STORED, `to_tsvector('simple', name \|\| ' ' \|\| description)` |
| `created_at` / `updated_at` | timestamptz | NOT NULL DEFAULT now() |

**`product_options`**

| 컬럼 | 타입 | 제약 |
|---|---|---|
| `id` | uuid | PK |
| `product_id` | uuid | NOT NULL REFERENCES `products(id)` ON DELETE CASCADE |
| `name` | text | NOT NULL |
| `values` | jsonb | NOT NULL, `CHECK (jsonb_typeof='array' AND length BETWEEN 1 AND 50)` |
| `sort_order` | integer | NOT NULL DEFAULT 0 |
| `created_at` / `updated_at` | timestamptz | NOT NULL DEFAULT now() |

`UNIQUE (product_id, name)`.

**`product_variants`**

| 컬럼 | 타입 | 제약 |
|---|---|---|
| `id` | uuid | PK |
| `product_id` | uuid | NOT NULL REFERENCES `products(id)` ON DELETE CASCADE |
| `option_values` | jsonb | NOT NULL, `CHECK (jsonb_typeof='object' AND ≤4096바이트)` |
| `sku` | text | NULL. 있을 때만 UNIQUE |
| `price_delta` | integer | NOT NULL DEFAULT 0 — **음수 허용** |
| `stock` | integer | NOT NULL DEFAULT 0 `CHECK (>= 0)` |
| `is_visible` | boolean | NOT NULL DEFAULT true |
| `created_at` / `updated_at` | timestamptz | NOT NULL DEFAULT now() |

`UNIQUE (product_id, option_values)`.

```sql
CREATE UNIQUE INDEX ON products (slug);
CREATE INDEX ON products (is_visible, created_at DESC);
CREATE INDEX ON products USING GIN (search_tsv);
CREATE UNIQUE INDEX ON product_options (product_id, name);
CREATE UNIQUE INDEX ON product_variants (product_id, option_values);
CREATE UNIQUE INDEX ON product_variants (sku) WHERE sku IS NOT NULL;
CREATE INDEX ON product_variants (product_id) WHERE is_visible AND stock > 0;
```

`products.is_visible` 기본값이 **false**인 것은 옵션·재고를 넣기 전에 팔리는 것을 막는
fail-closed다(A-503이 뒤에 온다). 소프트 삭제 컬럼은 두지 않는다 — 3-1의
`order_items.product_id RESTRICT`가 물리 삭제를 DB에서 막는다.

**`option_values`의 키는 `product_option_id`가 아니라 그룹명 문자열이다.** JSONB에는 FK를 걸 수
없어 ID를 키로 쓰면 옵션 그룹 삭제가 고아 참조를 만들고, 은퇴한 조합은 주문 이력이 계속
가리킨다. 대가는 그룹명 변경 시 같은 트랜잭션에서 조합 키를 함께 갱신해야 하는 것이다.

재고는 **delta 갱신(`stock = stock + $1`)만** 하고 절대값 UPDATE 경로를 만들지 않는다 —
delta 두 건은 순서와 무관하게 둘 다 맞다.

**주문된 조합이 있는 상품은 옵션을 재편할 수 없다. 은퇴(`is_visible=false`)만 가능하다** —
"조합은 옵션 값의 곱으로 서버가 만든다"(A-503)와 `order_items.variant_id RESTRICT`가 만나는 지점이다.

**`categories` · `product_categories`**

**`categories`**

| 컬럼 | 타입 | 제약 |
|---|---|---|
| `id` | uuid | PK |
| `parent_id` | uuid | NULL REFERENCES `categories(id)` ON DELETE RESTRICT |
| `name` | text | NOT NULL |
| `slug` | text | NOT NULL **UNIQUE 전역** |
| `sort_order` | integer | NOT NULL DEFAULT 0 |
| `created_at` / `updated_at` | timestamptz | NOT NULL DEFAULT now() |

`CHECK (parent_id IS NULL OR parent_id <> id)`.

**`product_categories`**

| 컬럼 | 타입 | 제약 |
|---|---|---|
| `product_id` | uuid | NOT NULL REFERENCES `products(id)` ON DELETE CASCADE |
| `category_id` | uuid | NOT NULL REFERENCES `categories(id)` ON DELETE RESTRICT |
| `created_at` | timestamptz | NOT NULL DEFAULT now() |

**PK (product_id, category_id)**.

**깊이 컬럼을 두지 않는다.** 안전 상한 10은 "설계 제약이 아니라 폭주 방지턱"이고(A-509),
`depth`를 물리화하면 서브트리 이동마다 갱신 코드가 붙는다. 순환·깊이는 3절대로 재귀 CTE 검사 +
`pg_advisory_xact_lock` 직렬화로 간다.

`product_categories`는 갱신하지 않는 순수 연결 표라 `updated_at`을 두지 않는다(3절 예외).

**`carts` · `cart_items`**

**`carts`**

| 컬럼 | 타입 | 제약 |
|---|---|---|
| `id` | uuid | PK |
| `user_id` | uuid | NULL REFERENCES `users(id)` ON DELETE CASCADE |
| `guest_key` | text | NULL. 16~128자 |
| `created_at` / `updated_at` | timestamptz | NOT NULL DEFAULT now() |

`CHECK ((user_id IS NULL) <> (guest_key IS NULL))` — 주인은 정확히 하나다.

**`cart_items`**

| 컬럼 | 타입 | 제약 |
|---|---|---|
| `id` | uuid | PK |
| `cart_id` | uuid | NOT NULL REFERENCES `carts(id)` ON DELETE CASCADE |
| `variant_id` | uuid | NOT NULL REFERENCES `product_variants(id)` ON DELETE CASCADE |
| `quantity` | integer | NOT NULL `CHECK (>= 1)` |
| `created_at` / `updated_at` | timestamptz | NOT NULL DEFAULT now() |

`UNIQUE (cart_id, variant_id)` — 같은 조합은 한 행이고 수량이 는다.

```sql
CREATE UNIQUE INDEX ON carts (user_id)    WHERE user_id IS NOT NULL;
CREATE UNIQUE INDEX ON carts (guest_key)  WHERE guest_key IS NOT NULL;
CREATE UNIQUE INDEX ON cart_items (cart_id, variant_id);
```

**세션 토큰 자체를 복사하지 않는다** — `guest_key`는 별도 난수다. `sessions.token`을 두 표에
두면 유출면이 하나 는다(토큰 표가 해시를 저장하는 것과 같은 논리).

**가격 컬럼이 없다** — P-401은 담을 때의 가격조차 저장하지 않고 P-402가 현재가로 재계산한다.
`UNIQUE (cart_id, variant_id)`가 같은 조합 재담기를 합산으로 강제한다 — 별도 행을 허용하면
**두 행이 각각 재고 검사를 통과**한다.

**`orders`**

| 컬럼 | 타입 | 제약 |
|---|---|---|
| `id` | uuid | PK |
| `order_no` | text | NOT NULL UNIQUE — `crypto/rand` 기반, **순번이 아니다** |
| `user_id` | uuid | NULL REFERENCES `users(id)` ON DELETE SET NULL |
| `status` | text | NOT NULL DEFAULT '결제대기', `CHECK` 아래 |
| `total_amount` | integer | NOT NULL `CHECK (>= 0)` — P-408 금액 대조의 단일 출처. **할인을 뺀 값**이다 |
| `discount_amount` | integer | NOT NULL DEFAULT 0 `CHECK (>= 0)` — 주문 단위 할인 (FR-626) |
| `receiver_name` | text | NOT NULL |
| `receiver_phone` | text | NOT NULL |
| `postcode` | text | NOT NULL |
| `address1` | text | NOT NULL |
| `address2` | text | NOT NULL DEFAULT '' |
| `delivery_memo` | text | NOT NULL DEFAULT '' |
| `orderer_email` | text | NOT NULL (회원도 세션 이메일을 복사) |
| `orderer_phone` | text | **NOT NULL** — 비회원 조회 대조 키. 회원도 받는다 (아래 참조) |
| `delivered_at` | timestamptz | NULL — **`배송완료` 전이 시각** |
| `confirmed_at` | timestamptz | NULL |
| `created_at` / `updated_at` | timestamptz | NOT NULL DEFAULT now() |

```sql
CHECK (status IN (
  '결제대기','입금대기','결제완료','결제실패',
  '배송준비','배송중','배송완료','구매확정',
  '취소','환불',
  '반품접수','반품수거',
  '교환접수','교환수거','차액결제대기','교환발송'))     -- 16개. D14 5절이 단일 출처
CHECK (orderer_email <> '')

CREATE UNIQUE INDEX ON orders (order_no);
CREATE INDEX ON orders (user_id, created_at DESC);
CREATE INDEX ON orders (status, created_at DESC);
CREATE INDEX ON orders (delivered_at) WHERE status = '배송완료';
```

**`orderer_phone`을 회원에게도 받는 이유.** 처음에는 "비회원만 필수"로 두고
`CHECK (user_id IS NOT NULL OR orderer_phone IS NOT NULL)`을 걸었는데, `user_id`가
`ON DELETE SET NULL`이라 **회원 계정을 지우는 순간 둘 다 NULL이 되어 CHECK가 깨지고 사용자
삭제 자체가 막혔다** (2026-08-05, 통합 테스트가 잡았다). `operation_logs` 트리거가 같은 이유로
사용자 삭제를 막았던 것과 같은 형태다.

고치는 방향은 CHECK를 푸는 것이 아니라 연락처를 항상 받는 것이다 — 배송이 있는 주문에
전화번호가 없는 경우는 없고, 그 값이 계정과 무관하게 남아야 주문이 계속 열린다.
`orderer_email`을 회원에게도 복사하는 것과 같은 판단이다.

**`delivered_at`이 필요한 이유.** A-512의 반품 기간(7일)·자동 확정(8일)이 전부 "`배송완료` 전이
시각 기준"인데 그 시각을 담을 곳이 없었다. 없으면 `operation_logs`를 운영 판정에 쓰게 되는데,
그 표는 A-601이 "수정·삭제하지 않는다"고 못박은 **감사 흔적이지 운영 데이터가 아니다.**

**배송비 컬럼을 두지 않는다** — 배송비 정책은 무료배송 기준액 하나뿐이고 부분 취소로 환불하지 않는다 ([D50](50-commerce.md)). 주문 시점의 배송비는 `orders` 가 이미 갖는다.

**`order_items`**

| 컬럼 | 타입 | 제약 |
|---|---|---|
| `id` | uuid | PK |
| `order_id` | uuid | NOT NULL REFERENCES `orders(id)` ON DELETE RESTRICT |
| `product_id` | uuid | NOT NULL REFERENCES `products(id)` ON DELETE RESTRICT |
| `variant_id` | uuid | NOT NULL REFERENCES `product_variants(id)` ON DELETE RESTRICT |
| `product_name` | text | NOT NULL — **스냅샷** |
| `option_label` | text | NOT NULL DEFAULT '' — **스냅샷** ("색상: 검정 / 사이즈: L") |
| `unit_price` | integer | NOT NULL `CHECK (>= 0)` — **스냅샷** = `base_price + price_delta` |
| `quantity` | integer | NOT NULL `CHECK (>= 1)` |
| `line_amount` | integer | GENERATED ALWAYS AS (`unit_price * quantity`) STORED |
| `discount_amount` | integer | NOT NULL DEFAULT 0 `CHECK (>= 0 AND <= unit_price * quantity)` — **배분된 할인 스냅샷** |
| `settled_quantity` | integer | NOT NULL DEFAULT 0, `CHECK (BETWEEN 0 AND quantity)` |
| `created_at` / `updated_at` | timestamptz | NOT NULL DEFAULT now() |

FR-612("스냅샷만으로 주문서 재발행")를 만족하려면 **상품명 + 옵션 표기 + 단가 + 수량**이 전부
복사돼야 한다 — `option_label`이 없으면 조합이 은퇴·변경된 뒤 주문서가 옵션을 재현하지 못한다.
`line_amount` 생성 컬럼이 품목 금액과 단가·수량의 어긋남을 구조적으로 없앤다.
`settled_quantity`는 `refunded_amount`와 같은 패턴이다 — 잔여 수량을 매번 합산으로 구하면
동시 요청 두 건이 함께 통과한다.

**`discount_amount`가 품목에 있는 이유** (FR-626). 주문 할인을 환불할 때마다 비례로 다시
계산하면 반올림이 매번 달라져 마지막 품목에서 합이 안 맞는다. 주문 생성 시 한 번 배분해
스냅샷으로 두면 부분 취소가 **뺄셈**이 되고, 전 품목을 나눠 환불한 합계가 상품 합계와
1원도 다르지 않다. 배분 규칙(품목 금액 비례 + 최대 나머지법)은 [D50](50-commerce.md)
「Phase 3 정책값」에 있다.

**표시는 스냅샷 컬럼만 쓴다.** `product_id`·`variant_id`는 관리자 화면 이동 링크용이며,
조인해 현재 상품명·가격을 보여주면 FR-612가 깨진다.

**`payments`**

| 컬럼 | 타입 | 제약 |
|---|---|---|
| `id` | uuid | PK |
| `order_id` | uuid | NOT NULL REFERENCES `orders(id)` ON DELETE RESTRICT |
| `return_id` | uuid | NULL REFERENCES `returns(id)` ON DELETE RESTRICT |
| `kind` | text | NOT NULL `CHECK (kind IN ('주문결제','교환차액'))` |
| `status` | text | NOT NULL DEFAULT '대기' `CHECK (status IN ('대기','승인','실패'))` |
| `pg` | text | NOT NULL |
| `payment_key` | text | NOT NULL |
| `approved_amount` | integer | NOT NULL `CHECK (>= 0)` |
| `refunded_amount` | integer | NOT NULL DEFAULT 0, `CHECK (>= 0 AND <= approved_amount)` |
| `raw_response` | jsonb | NULL — 카드 필드 마스킹 후 |
| `secret` | text | NULL, 길이 ≤ 200. **웹훅 대조용** — 토스는 서명 헤더를 주지 않고, 공식 문서가 제시하는 검증 수단은 승인 응답의 이 값과 웹훅 본문의 값을 대조하는 것 하나뿐이다 ([D50](50-commerce.md) 「웹훅」). 저장하지 않으면 대조할 상대가 없다 |
| `approved_at` | timestamptz | NULL |
| `created_at` / `updated_at` | timestamptz | NOT NULL DEFAULT now() |

`returns` 는 다음 마이그레이션(W3-06)에서 생긴다. `return_id` 의 FK 는 거기서
`ALTER TABLE` 로 건다 — 최종 스키마는 이 표와 같다.

```sql
CHECK ((kind = '교환차액') = (return_id IS NOT NULL))

CREATE UNIQUE INDEX ON payments (order_id)
  WHERE kind = '주문결제' AND status <> '실패';        -- 주문당 승인 1건 (FR-608)
CREATE UNIQUE INDEX ON payments (order_id, return_id)
  WHERE kind = '교환차액';                             -- 교환 건당 차액 1건 (FR-618)
CREATE UNIQUE INDEX ON payments (pg, payment_key);
CREATE INDEX ON payments (status, created_at) WHERE status = '대기';  -- A-508 대사
```

> **부분 유니크에 `AND status <> '실패'`가 붙는 이유.** 그것 없이는 승인 API가 실패한 뒤 행이
> 영구히 남아 **재결제가 불가능해진다** — P-409는 "주문은 `결제대기`에 머문다, 재시도 경로를
> 남기기 위해서다"라고 못박았다. 동시 두 건은 둘 다 `대기`로 들어가려다 하나만 성공하므로
> FR-608은 그대로 성립한다. 타임아웃(결과 불명)은 `대기`로 남겨 D50의 "재승인 시도 금지 →
> 조회 API"와 A-508 대사 대상이 자동으로 일치한다.

**카드번호·유효기간·CVC는 컬럼도 없고 `raw_response`에도 넣지 않는다** ([DEC-3.7](../.ai/DECISIONS.md),
PCI DSS). 정기결제가 필요해지면 빌링키 컬럼을 그때 더한다.

**`refunds` · `refund_items`**

**`refunds`**

| 컬럼 | 타입 | 제약 |
|---|---|---|
| `id` | uuid | PK |
| `order_id` | uuid | NOT NULL REFERENCES `orders(id)` ON DELETE RESTRICT |
| `payment_id` | uuid | NOT NULL REFERENCES `payments(id)` ON DELETE RESTRICT |
| `return_id` | uuid | NULL REFERENCES `returns(id)` ON DELETE RESTRICT |
| `status` | text | NOT NULL DEFAULT '요청' `CHECK IN ('요청','승인','거부','완료')` |
| `requester` | text | NOT NULL `CHECK IN ('구매자','관리자')` |
| `amount` | integer | NOT NULL `CHECK (> 0)` |
| `reason` | text | NOT NULL DEFAULT '' |
| `request_key` | text | NOT NULL **UNIQUE** |
| `created_at` / `updated_at` | timestamptz | NOT NULL DEFAULT now() |

**`refund_items`**

| 컬럼 | 타입 | 제약 |
|---|---|---|
| `id` | uuid | PK |
| `refund_id` | uuid | NOT NULL REFERENCES `refunds(id)` ON DELETE CASCADE |
| `order_item_id` | uuid | NOT NULL REFERENCES `order_items(id)` ON DELETE RESTRICT |
| `quantity` | integer | NOT NULL `CHECK (>= 1)` |
| `created_at` | timestamptz | NOT NULL DEFAULT now(). **`updated_at` 없음** |

`UNIQUE (refund_id, order_item_id)`.

요청 키를 A-507 전용이 아니라 **모든 경로에 NOT NULL로** 둔다 — P-506·P-507의 중복 제출도 같은
사고(이중 환불)를 내는데 화면마다 멱등 수단이 다르면 한쪽만 고쳐진다.

**`payments.refunded_amount`는 요청 시점에 올라가는 선점액이다.** `거부`로 전이할 때 같은
트랜잭션에서 되돌린다 — 이 정의가 없으면 컬럼 이름이 "이미 나간 돈"으로 읽혀 이중 계상된다.

**`return_id`가 있는 `refunds`는 `refund_items`를 만들지 않는다** — 품목·수량이 이미
`return_items`에 있고, 양쪽에 넣으면 `settled_quantity`가 이중 계상된다(애플리케이션 불변식, NFR-405).

**`returns` · `return_items`**

**`returns`**

| 컬럼 | 타입 | 제약 |
|---|---|---|
| `id` | uuid | PK |
| `return_no` | text | NOT NULL UNIQUE — 순번이 아니다 |
| `order_id` | uuid | NOT NULL REFERENCES `orders(id)` ON DELETE RESTRICT |
| `kind` | text | NOT NULL `CHECK (kind IN ('반품','교환'))` |
| `status` | text | NOT NULL, `CHECK` 아래 |
| `reason` | text | NOT NULL DEFAULT '' |
| `reject_reason` | text | NOT NULL DEFAULT '' |
| `fault` | text | NULL `CHECK (fault IN ('구매자','판매자'))` — **수거 확인 시 확정** |
| `shipping_fee_policy` | text | NULL `CHECK IN ('차감','별도청구')` — **A-512 스냅샷** |
| `shipping_fee_amount` | integer | NULL `CHECK (>= 0)` — **A-512 스냅샷** |
| `new_variant_id` | uuid | NULL REFERENCES `product_variants(id)` ON DELETE RESTRICT |
| `price_difference` | integer | NULL — 부호 있음 |
| `created_at` / `updated_at` | timestamptz | NOT NULL DEFAULT now() |

```sql
CHECK ((kind='반품' AND status IN ('반품접수','반품수거','환불','거부'))
    OR (kind='교환' AND status IN ('교환접수','교환수거','차액결제대기','교환발송','거부')))
CHECK (kind='교환' OR (new_variant_id IS NULL AND price_difference IS NULL))
CHECK (kind<>'교환' OR new_variant_id IS NOT NULL)
CHECK (status <> '차액결제대기' OR price_difference > 0)
CHECK (kind <> '반품' OR status NOT IN ('반품수거','환불')
       OR (fault IS NOT NULL AND shipping_fee_policy IS NOT NULL
           AND shipping_fee_amount IS NOT NULL))          -- 수거 확인 = 스냅샷 복사
CHECK (fault <> '판매자' OR shipping_fee_amount = 0)
```

**`return_items`**

| 컬럼 | 타입 | 제약 |
|---|---|---|
| `id` | uuid | PK |
| `return_id` | uuid | NOT NULL REFERENCES `returns(id)` ON DELETE CASCADE |
| `order_item_id` | uuid | NOT NULL REFERENCES `order_items(id)` ON DELETE RESTRICT |
| `quantity` | integer | NOT NULL `CHECK (>= 1)` |
| `is_open` | boolean | NOT NULL DEFAULT true |
| `created_at` / `updated_at` | timestamptz | NOT NULL DEFAULT now() |

```sql
UNIQUE (return_id, order_item_id)
CREATE UNIQUE INDEX ON return_items (order_item_id) WHERE is_open;   -- 품목당 처리 중 1건
```

**`is_open`은 비정규화이고 이유가 하나뿐이다** — PostgreSQL 부분 인덱스의 술어는 **같은
테이블의 컬럼만** 참조할 수 있어 `returns.status`를 볼 수 없다. "같은 품목에 처리 중인 건이
둘 이상 생기지 않는다"를 DB로 강제하려면 상태를 이 표에 내려야 한다. `returns`가 종결로
전이하는 트랜잭션에서 `is_open = false`로 함께 내린다.

상태 값은 **D14의 주문 상태 라벨을 그대로** 쓴다 — 한 주문에 서로 다른 품목의 반품·교환이
동시에 진행될 수 있어 `orders.status` 하나로는 건별 단계를 표현하지 못하는데, 여기서 새 이름을
만들면 같은 개념에 두 어휘가 생긴다.

**`shipments`**

| 컬럼 | 타입 | 제약 |
|---|---|---|
| `id` | uuid | PK |
| `order_id` | uuid | NOT NULL REFERENCES `orders(id)` ON DELETE RESTRICT — **UNIQUE를 걸지 않는다** |
| `return_id` | uuid | NULL REFERENCES `returns(id)` ON DELETE RESTRICT |
| `kind` | text | NOT NULL `CHECK IN ('최초발송','교환재발송')` |
| `carrier` | text | NOT NULL |
| `tracking_no` | text | NOT NULL |
| `shipped_at` | timestamptz | NOT NULL |
| `created_at` / `updated_at` | timestamptz | NOT NULL DEFAULT now() |

```sql
CHECK ((kind = '교환재발송') = (return_id IS NOT NULL))
CREATE UNIQUE INDEX ON shipments (order_id)  WHERE kind = '최초발송';
CREATE UNIQUE INDEX ON shipments (return_id) WHERE return_id IS NOT NULL;
```

`order_id`에 UNIQUE를 걸면 D14의 `교환발송 → 배송완료` 복귀 흐름이 성립하지 않는다.
**부분 유니크 두 개**가 실제로 지키려던 불변식("최초 발송 1건", "교환 건당 재발송 1건")이다.

**`webhook_events`**

| 컬럼 | 타입 | 제약 |
|---|---|---|
| `id` | uuid | PK |
| `pg` | text | NOT NULL |
| `event_id` | text | NOT NULL |
| `order_id` | uuid | NULL REFERENCES `orders(id)` ON DELETE RESTRICT |
| `status` | text | NOT NULL DEFAULT '수신' `CHECK IN ('수신','처리완료','실패')` |
| `payload` | jsonb | NOT NULL — 마스킹 후 |
| `error` | text | NULL |
| `created_at` / `updated_at` | timestamptz | NOT NULL DEFAULT now() |

```sql
CREATE UNIQUE INDEX ON webhook_events (pg, event_id);
CREATE INDEX ON webhook_events (status, created_at DESC) WHERE status <> '처리완료';
```

UNIQUE가 `(pg, event_id)` **복합**인 이유: 어댑터가 여럿이라는 것이 FR-605의 전제이고,
두 PG가 같은 ID 문자열을 발급하면 단일 컬럼 UNIQUE는 **정상 이벤트를 중복으로 버린다.**
`수신`은 "미처리"를 뜻한다 — 고루틴 처리 중 프로세스가 죽으면 반드시 남는 상태이고 ([D50](50-commerce.md) 「Phase 3 정책값」이 자동 재처리를 두지 않기로 했다),
부분 인덱스가 A-603 상단에 그 행들을 올린다.

**`terms` · `order_agreements`**

**`terms`**

| 컬럼 | 타입 | 제약 |
|---|---|---|
| `id` | uuid | PK |
| `kind` | text | NOT NULL (**허용목록 없음**) |
| `version` | text | NOT NULL |
| `body` | text | NOT NULL — **평문** |
| `effective_at` | timestamptz | NOT NULL |
| `is_required` | boolean | NOT NULL DEFAULT true |
| `created_at` | timestamptz | NOT NULL DEFAULT now(). **`updated_at` 없음** |

**`order_agreements`**

| 컬럼 | 타입 | 제약 |
|---|---|---|
| `order_id` | uuid | NOT NULL REFERENCES `orders(id)` ON DELETE RESTRICT |
| `terms_id` | uuid | NOT NULL REFERENCES `terms(id)` ON DELETE RESTRICT |
| `agreed_at` | timestamptz | NOT NULL DEFAULT now() |

**PK (order_id, terms_id)**.

```sql
ALTER TABLE terms ADD CONSTRAINT terms_kind_version_uniq UNIQUE (kind, version);
ALTER TABLE terms ADD CONSTRAINT terms_no_backdate CHECK (effective_at >= created_at);
CREATE INDEX ON terms (kind, effective_at DESC);
CREATE INDEX ON order_agreements (terms_id);
```

`terms`에 `updated_at`이 없다 — 배포된 버전은 수정하지 않고 개정은 새 행이다.
`UNIQUE (kind, version)`이 없으면 "어느 본문에 동의했는지"를 특정할 수 없다.

**약관 본문을 복사하지 않는다** — `terms` 행이 불변이고 `terms_id RESTRICT`가 삭제를 막으므로
참조만으로 FR-619의 "나중에 재현된다"가 성립한다. 복사하면 주문 수만큼 본문이 복제된다.

### 돈·재고 불변식을 DB가 강제하는 목록

**애플리케이션 검사만으로는 동시 요청을 막지 못한다.** 아래는 전부 DB가 막는다.

| 불변식 | 수단 | 없으면 |
|---|---|---|
| 환불 누적 ≤ 승인금액 | `payments` CHECK | 결제액보다 많은 돈이 나간다 (FR-611) |
| 주문당 승인 1건 | 부분 UNIQUE | 동시 콜백 두 건이 이중 승인 (FR-608) |
| 교환 건당 차액 승인 1건 | 부분 UNIQUE | 차액이 두 번 결제된다 |
| 교환차액 행은 반드시 교환 건을 가리킨다 | CHECK | `return_id`가 NULL이면 위 유니크를 통째로 우회한다 |
| 같은 승인이 두 행으로 기록되지 않음 | `UNIQUE (pg, payment_key)` | A-508이 무엇이 진짜인지 판정 못한다 |
| 재고 음수 금지 | CHECK `stock >= 0` | 백오더가 조용히 생긴다 |
| 소진 수량 ≤ 주문 수량 | CHECK | 3개 주문에 5개를 환불한다 |
| 품목당 처리 중 반품·교환 1건 | `return_items` 부분 UNIQUE | 같은 물건을 두 번 환불받는다 |
| 웹훅 재전송 멱등 | `UNIQUE (pg, event_id)` | 같은 입금이 두 번 반영된다 (FR-610) |
| 회원당 장바구니 1개 | 부분 UNIQUE | 담은 물건이 사라져 보인다 |
| 장바구니 조합당 1행 | `UNIQUE (cart_id, variant_id)` | 두 행이 각각 재고 검사를 통과한다 |
| 품목 금액 = 단가 × 수량 | GENERATED STORED | 합계와 품목이 어긋난 주문서가 재발행된다 |
| 수거 확인 시 배송비 스냅샷 존재 | CHECK | A-512 변경만으로 과거 환불액이 달라진다 |
| 판매자 귀책이면 배송비 0 | CHECK | 하자 상품의 반품비를 구매자가 문다 |
| 주문·품목·결제·환불 행이 지워지지 않음 | FK RESTRICT (3-1) | 정산·분쟁 근거가 사라진다 |
| 약관 (종류, 버전) 유일 | UNIQUE | 어느 본문에 동의했는지 특정 불가 (FR-619) |
| 소급 시행 금지 | `terms` CHECK | 끝난 주문에 나중 약관이 소급된다 |
| 환불 요청 멱등 | `UNIQUE (request_key)` | 새로고침 한 번이 이중 환불이 된다 |

**DB가 막지 못해 애플리케이션 불변식으로 남는 것** (전부 NFR-405 테스트 대상):
`orders.total_amount = SUM(line_amount)` · `base_price + price_delta >= 0` ·
교환 새 조합이 **같은 상품**인지(FR-618) · `refunds.amount = SUM(스냅샷 계산액) − 배송비` ·
`return_id`가 있는 `refunds`에 `refund_items`를 만들지 않기 · `raw_response`·`payload`의 카드
필드 마스킹 · 카테고리 순환·깊이 10 · 메뉴 순환.

## 참조

- 게시판 커스텀 필드가 화면에 나타나는 경로: [D40](40-theme.md)
- 결제 테이블이 지켜야 할 검증: [D50](50-commerce.md), [D60](60-security.md)
