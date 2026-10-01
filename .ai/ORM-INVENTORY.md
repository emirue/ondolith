# ORM 전환 조사 — SQL 전수 목록 (2026-10-02)

읽기 전용 조사 결과. 수치는 코드를 읽어 센 값이다(추정 없음). 기준 커밋 8443be2.

## 1. 규모

| 패키지 | SQL 호출 | tx-begin | tx 안 문장 | 풀 직접 | 저장소 메서드 |
|---|---|---|---|---|---|
| commerce (10 파일) | 180 | 22 | 93 | 65 | 77 |
| content (6 파일) | 65 | 2 | 3 | 60 | 53 |
| auth (4 파일) | 45 | 5 | 12 | 28 | 34 |
| install (1 파일) | 1 | 0 | 0 | 1 | — |
| **합계** | **291** | **29** | **108** | **154** | **164** |

- pgx 를 import 하는 비테스트 파일: content·auth·commerce 전부 + app.go + install.go. secretbox 는 DB 를 만지지 않는다.
- goose 마이그레이션 21개(00001~00022, 00006 결번), Go 마이그레이션 0개. 부팅·설치 둘 다 `stdlib.OpenDBFromPool(pool)` → `migrations.Run`.
- 풀은 하나: `pgxpool`(MinConns=1). scs `pgxstore.New(pool)` 가 같은 풀을 쓴다.
- DB 를 직접 만지는 테스트 파일 46개(테스트 SQL 호출 512건). 패키지별 자체 헬퍼가 `DROP SCHEMA public CASCADE` 후 goose 로 다시 올린다 — 공유 헬퍼 없음, `-p 1` 강제.

## 2. PostgreSQL 전용 기능 (호출 건수)

| 기능 | commerce | content+auth | 비고 |
|---|---|---|---|
| `FOR UPDATE` / `FOR UPDATE OF 별칭` | 29 | 2 | 주문·재고·결제·환불 전부 잠금 기반 |
| `pg_advisory_xact_lock` | 1 | 0 | 카테고리 재배치 |
| `RETURNING` (INSERT / UPDATE…RETURNING) | 13 | 10 | 토큰 소각은 `UPDATE … RETURNING` 단일문 |
| `ON CONFLICT` DO UPDATE / DO NOTHING | 5 | 5 | 장바구니는 부분 유니크 추론(`WHERE user_id IS NOT NULL`), 변형은 JSONB 컬럼 충돌 |
| 부분 유니크 인덱스에 기대는 23505 | 12 | 5 | 멱등·중복 방지의 유일 근거 (웹훅, 환불 요청 키, 주문당 결제 1건 …) |
| `pgconn.PgError` 코드 분기 | 23505 ×12 · 23514 ×5 · 23503 ×3 · 23001 ×2 · ConstraintName ×3 | 23505 ×5 · 23503 ×2 · 23001 ×1 · ConstraintName ×1 | CHECK 위반(23514)이 환불 한도·재고 음수의 방어선 |
| `pgx.ErrNoRows` | 38 | 19 | 대부분 ErrNotFound, 일부는 정상 흐름 |
| CAS `UPDATE … WHERE status = $n` + RowsAffected | 14 | 2 | 상태머신 동시성 |
| `= ANY($1)` / `<> ALL($1)` / `text[]` | 5 | 12 | |
| `LEFT JOIN LATERAL` | 2 | 0 | 상품 목록·검색 |
| `tsvector @@ to_tsquery('simple')` (+ `ts_rank`) | 1 | 5 | 검색어는 Go 에서 룬 화이트리스트로 조립 |
| JSONB 컬럼 (map ↔ jsonb) | 6 | 10 | 연산자(`@>`, `->>`) 사용 0건 |
| `DISTINCT ON` | 3 | 0 | |
| CTE `WITH` / 윈도 함수 / `array_agg … FILTER` | 0 / 0 / 0 | 1 / 1 / 5 | 권한 로딩 |
| `UPDATE … FROM` 다중 테이블 | 1 | 0 | |
| `inet` + `host()` | 0 | 2 | 작업 로그 |
| 불린 식 ORDER BY, `CASE WHEN`, `NULLIF/GREATEST/COALESCE` 캐스트 술어 | 다수 | 다수 | `$n::uuid IS NULL OR …` 패턴이 소유권 술어의 기본형 |

## 3. 동적으로 조립되는 질의 (전체 6곳 — 사용자 입력이 SQL 텍스트에 닿는 경로는 0)

| 위치 | 방식 |
|---|---|
| commerce/store.go ListProducts | `ORDER BY` + 허용목록 `productSortColumns` (키 5개) |
| commerce/cart.go UpdateCartItem | const 소유권 조각 3문장에 연결 |
| content/post.go (23건) | `postColumns`/`postListColumns` 상수 결합 + `ListQuery.OrderBy()` 허용목록 + 검색 유무로 질의 2벌 |
| content/store.go scanPage | 질의문을 인자로 받는 공유 스캐너 |
| content/post.go toPrefixQuery | tsquery 를 Go 에서 조립 (룬 화이트리스트) |
| auth/token.go | 토큰 테이블명을 `TokenKind` 2값에서 선택 (3문장) |

## 4. 트랜잭션 소유 함수 (29)

commerce 22: OpenReturn, ConfirmPickup, SettleReturn, RejectReturn, CompleteExchange, ConfirmExchangeDiff, CreateOrder, TransitionOrder, expireOne, RequestRefund, RejectRefund, CancelOrder, SettleFullRefund, ExecuteRefund, Reparent, EditVariants, SetOptions, AddToCart, MergeCarts, ConfirmPayment(tx·tx2 연속 — 사이에 PG 호출), processWebhook.
content 2: PutSettings(봉인 훅 포함), CreateBoard. auth 5: withLastSuperuserGuard(→SetActive/DeleteUser), UnlinkSocial, IssueResetToken, ResetPassword, IssueToken.
`pgx.Tx` 를 인자로 받는 헬퍼 9개(AdjustStock, moveOrder, cartID, restockOrder, restockDeltas, returnFeeSetting, returnGross, requiredTermIDs, withLastSuperuserGuard) — 호출 19회.

## 5. 후보 라이브러리 (공식 릴리즈·문서에서 확인, 2026-10-02)

| 후보 | 최신 | 실행 경로 | 잠금 / RETURNING / ON CONFLICT | 비고 |
|---|---|---|---|---|
| sqlc | v1.31.1 (2026-04-22) | `sql_package: pgx/v5` — 생성 코드가 pgx 타입(pgx.Rows, pgtype)을 쓴다. `*pgxpool.Pool`·`pgx.Tx` 를 그대로 넘긴다 | 전부 가능 — SQL 을 그대로 쓰므로 | ORM 이 아니라 SQL→타입 코드 생성기. 질의가 컴파일 시점에 고정되므로 동적 ORDER BY 는 CASE 식이나 질의 분기로 바꿔야 한다 (공식 howto 두 페이지에 동적 정렬 언급 없음). `= ANY($1)` 는 슬라이스 인자로 자동 생성 |
| Bun | v1.2.18 (2026-02-28) | `database/sql` 위. pgx 는 `stdlib.OpenDBFromPool(pool)` 로 감싸서 — 풀 직접 재사용 불가 | `For("UPDATE")`, `Returning`, `On("CONFLICT …")` 빌더 | 가벼운 쿼리 빌더+ORM. 문서가 pgx 사용 시 `QueryExecModeSimpleProtocol` 을 권한다 |
| ent | v0.14.6 (2026-03-23) | `database/sql` 위, 스키마 코드 생성 | 기능 플래그 `sql/lock`(ForUpdate), `sql/upsert`(OnConflict), `sql/modifier`, `sql/execquery` | 스키마를 Go 로 다시 정의해야 한다(goose SQL 과 이중 관리). 그래프 지향 |
| GORM | v1.31.2 (2026-06-25), postgres 드라이버 v1.6.3 | `database/sql` 위 | `clause.Locking{Strength:"UPDATE"}`, `clause.OnConflict`, RETURNING 은 별도 clause | 가장 널리 쓰임. 리플렉션 기반, 부분 유니크·CHECK 코드 분기·CAS 는 결국 `Raw`/`Exec` |

공통 사실: ent·Bun·GORM 은 `database/sql` 을 거친다 → `pgconn.PgError` 는 `errors.As` 로 여전히 꺼낼 수 있지만, `pgx.ErrNoRows` 는 `sql.ErrNoRows` 로 바뀌고 스캔 타입 체계가 달라진다. pgxstore(세션)·goose 는 어느 경우든 그대로 둘 수 있다.
