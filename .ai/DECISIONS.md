# DECISIONS

확정된 결정과 **배제된 선택지**의 영구 기록.

> 이 파일의 목적은 세션이 바뀌어도 이미 배제된 선택지가 "더 나은 대안"으로 되살아나지 않게 하는 것이다.
> 아래 항목을 다시 제안하지 말 것. 뒤집으려면 이 파일을 먼저 고칠 것.

---

## DEC-0. 착수 결정 (2026-07-29 확정)

| 항목 | 결정 | 사유 |
|---|---|---|
| 데이터베이스 | **PostgreSQL 고정** | JSONB를 커스텀 필드에 사용. 마이그레이션 분기 없음 |
| 라이선스 | **Apache-2.0** | 특허 조항. 배포형 제품에 유리 |
| 플러그인 생태계 | **1차 미지원** | 코어에 훅 포인트만 정의. 로더는 만들지 않음 |
| Go 모듈 경로 | **`github.com/emirue/ondolith`** | vanity import path 사용 금지 (DEC-3.8) |

MySQL 동시 지원은 배제됐다. JSONB 없이 커스텀 필드를 재설계해야 하고 goose
마이그레이션과 세션 스토어가 방언별로 이중화된다.

---

## DEC-1. 확정 스택

| 영역 | 선택 | 버전 (2026-07-29 확인) |
|---|---|---|
| 언어 | Go | 1.25.5 |
| 라우터 | `net/http.ServeMux` (표준 라이브러리) | stdlib |
| 테마 렌더링 | `html/template` | stdlib |
| CSRF | `net/http.CrossOriginProtection` | stdlib — **DEC-2.1 참조** |
| 세션 | `alexedwards/scs/v2` + `scs/pgxstore` | v2.9.0 / v0.0.0-20251002162104 |
| 마이그레이션 | `pressly/goose/v3` | v3.27.3 |
| DB 드라이버 | `jackc/pgx/v5` (+ `pgx/v5/stdlib` 브리지) | v5.10.0 |
| 인터랙션 | htmx | (CDN/embed, Phase 1에서 확정) |
| 소셜 로그인 | `markbates/goth` | v1.82.0 (Phase 1) |
| 정적 자산 | `embed.FS` | stdlib |

**라우터를 stdlib로 정한 이유:** Go 1.22+ `ServeMux`가 메서드·와일드카드 패턴
라우팅을 지원한다. `chi`가 더 주는 것은 `Route`/`Use` 그룹 설탕 정도이고, 그건
20줄짜리 헬퍼로 대체된다. 단일 바이너리·최소 의존성이라는 목표에 stdlib가 맞다.
gin/echo/fiber는 애초에 배제 (미들웨어 생태계를 끌고 오면 단일 바이너리 이점이 희석).

**pgx 단일 커넥션 풀 사용:** `pgxpool.Pool` 하나를 앱·세션 스토어가 공유하고,
goose에는 `stdlib.OpenDBFromPool(pool)`로 `*sql.DB`를 만들어 넘긴다. 접속 설정이
한 군데다.

---

## DEC-2. 요청서 이후 변경된 결정

### DEC-2.1 CSRF: `gorilla/csrf` → `net/http.CrossOriginProtection` (stdlib)

요청서 2장은 `gorilla/csrf`를 지정했으나, 착수 전 검증에서 뒤집혔다.

- `gorilla/csrf`는 **CVE-2025-47909 (GO-2025-3884)** 대상이며 최신 v1.7.3이
  `last_affected` — 즉 **수정 버전이 존재하지 않는다**.
- 해당 어드바이저리의 공식 권고가 "Go 1.25에 도입된 `net/http.CrossOriginProtection`
  으로 이전하라"이다. 우리 툴체인은 Go 1.25.5다.

부수 효과로 설계가 단순해진다. double-submit 토큰 방식이 아니라 `Sec-Fetch-Site` /
`Origin` 헤더 검사이므로 **테마 템플릿의 모든 `<form>`에 토큰 히든 필드를 심을
필요가 없다.** 테마 작성자가 토큰을 빠뜨려 보호가 뚫리는 사고 자체가 사라진다.

알려진 트레이드오프: `Sec-Fetch-Site`와 `Origin`이 **둘 다 없는** 요청은 통과된다
(Go 팀이 명시한 설계 — 2023년 이후 모든 브라우저가 `Sec-Fetch-Site`를 보낸다).
비브라우저 클라이언트에는 CSRF 개념이 적용되지 않으므로 수용 가능하다.

---

## DEC-3. 명시적 금지 사항

**아래는 이미 검토 후 배제된 것이다. "더 나은 대안"으로 제안하지 말 것.**
(요청서 3장 원문 + DEC-3.7, DEC-3.8 추가)

### DEC-3.1 테마 레이어에 `templ` 사용 금지
`templ generate`는 `.templ` 파일에서 Go 코드를 생성하고, 그 코드가 컴파일된다.
테마를 바꾸려면 재컴파일이 필요해지므로 **런타임 테마 교체라는 핵심 요구사항과
정면 충돌**한다. 코어 관리자 UI에는 사용 가능하지만, 사용자 테마 렌더링 경로에는
반드시 `html/template`을 쓸 것.

### DEC-3.2 Next.js / React SSR 계열 금지
같은 이유(사전 컴파일) + 저사양 서버에서의 상시 메모리 점유.

### DEC-3.3 PocketBase 채택 금지
SQLite 전용이며 PostgreSQL을 지원하지 않는다. 또한 v0.23+ 부터 첫 superuser 생성이
콘솔에 출력되는 토큰 링크 방식이라, "브라우저에서 설치 완결"이라는 요구사항과
어긋난다.

### DEC-3.4 QOR5 채택 금지
QOR5는 템플릿 언어 대신 정적 타입 Go로 HTML을 작성하는 방식(go template조차 사용하지
않음)이라 UI가 전부 컴파일된다. 테마 요구사항과 충돌한다.

### DEC-3.5 GoAdmin을 의존성으로 편입 금지
- 본체 저장소의 마지막 갱신이 2025년 6월로 확인됨 (1년 이상 정체)
- 자체 인증 미들웨어·관리자 유저 테이블·RBAC를 통째로 끌고 와, Ondolith 자체 권한
  체계와 **권한 시스템이 이중화**된다

**단, 설계 참고는 권장한다.** Apache-2.0이므로 RBAC 스키마, 메뉴 트리 구조,
operation log 모델 등은 참고하거나 발췌해도 된다 (저작권 고지 유지).

### DEC-3.6 Go `plugin` 패키지 사용 금지
공식 문서상 애플리케이션과 플러그인이 정확히 같은 툴체인 버전·빌드 태그·플래그로
컴파일되어야 하고, 공통 의존성도 동일 소스에서 빌드되어야 하며, 로드 후 언로드가
불가능하다. Windows도 미지원. 배포형 제품에 부적합하다.

### DEC-3.7 카드 정보 직접 저장 금지
카드번호/유효기간/CVC를 DB에 저장하지 말 것 (PCI DSS). 정기결제가 필요하면 빌링키
방식만 사용한다.

### DEC-3.8 vanity import path 금지
`ondolith.dev/core` 같은 경로를 쓰지 말 것 — 도메인 만료 시 전 세계 사용자의
`go get`이 깨진다. 도메인은 문서 사이트 용도로만 쓴다.

### DEC-3.9 게시판별 동적 `CREATE TABLE` 금지
그누보드가 게시판마다 테이블을 만드는 방식은 마이그레이션 관리를 망가뜨린다.
`boards` / `posts`(+ JSONB `custom_fields`) / `board_fields` 구조로 간다.

### DEC-3.10 관리자 CRUD 자동 생성기 사용 금지
관리자 화면 자체가 제품의 핵심 UX다. 게시판 설정, 테마 관리, 상품 옵션 편집기는
자동 생성으로 나오지 않는다. 직접 구현한다.

---

## DEC-4. 미검증 — 해당 Phase 착수 전 확인 필요

| 항목 | 필요 시점 | 상태 |
|---|---|---|
| 토스페이먼츠 `go-react` 샘플 유지 상태 | Phase 3 | 미확인 |
| 토스페이먼츠 API 스펙 재확인 (승인 엔드포인트/10분 만료) | Phase 3 | 요청서 기준, 재확인 필요 |
| htmx 최신 버전 및 배포 방식 | Phase 1 | 미확인 |
| goth 프로바이더 설정 API | Phase 1 | 미확인 |

`github.com/emirue/ondolith` 사용으로 GitHub 조직명 가용성 확인은 불필요해졌다.
