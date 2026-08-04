# D20. 아키텍처

## 지배 원칙

**설치 전과 설치 후는 완전히 다른 라우트 트리를 갖는다.**

하나의 라우터에 `if installed` 분기를 넣지 않는다. 기동 시점에 어느 트리를 등록할지
결정한다. 분기를 라우터 안에 넣으면 모든 핸들러가 미설치 상태를 신경 써야 하고,
설치 전에 노출되면 안 되는 경로가 실수 하나로 열린다.

```
부팅
 └→ config.Load(path)
     ├─ ErrNotInstalled ──→ 설치 트리 등록
     │                       GET  /install     폼
     │                       POST /install     처리
     │                       /*                → 303 /install
     │                       (일반 라우트는 아예 등록되지 않는다)
     │                            │
     │                            │ 설치 성공
     │                            ↓
     │                       운영 트리로 원자적 교체 (재시작 없음)
     │
     ├─ 파싱 실패 ──────────→ 기동 중단 (FR-110)
     │                        설정이 깨졌다고 설치 모드로 되돌아가면
     │                        지나가는 사람이 사이트를 재점유한다
     │
     └─ 정상 ──────────────→ 운영 트리 등록
                              대기 마이그레이션 자동 적용 (NFR-302)
```

## 핸들러 교체

`http.Server`는 `root` 하나만 알고 있고, 설치가 끝나면 그 안의 포인터가 바뀐다.
그래서 재시작이 필요 없다 (FR-106).

**핸들러와 그 정리 함수는 한 값으로 묶어 통째로 교체한다.**

```go
type tree struct {
	handler http.Handler
	cleanup func()   // 커넥션 풀 등. 설치 트리는 nil
	once    sync.Once
}

type root struct{ t atomic.Pointer[tree] }

func (r *root) swap(h http.Handler, cleanup func()) {
	r.t.Swap(&tree{handler: h, cleanup: cleanup}).close() // 이전 트리 해제
}
func (r *root) close()                { r.t.Load().close() }
func (r *root) ServeHTTP(w, req)      { r.t.Load().handler.ServeHTTP(w, req) }
```

**둘을 따로 두면 안 되는 이유:** 교체는 설치 폼을 처리하는 **HTTP 핸들러 고루틴**에서
일어나고, 해제는 종료 시 **메인 고루틴**에서 일어난다. `srv.Shutdown`은 데드라인이
지나면 핸들러가 아직 도는 중에도 반환하므로, 설치 완료와 SIGTERM이 겹치는 창이 실재한다.
포인터만 원자적으로 보호하고 정리 함수를 옆의 평범한 변수에 두면 그 창에서 데이터
레이스가 난다 — 실제로 그렇게 짰다가 고쳤다 ([M5](../.ai/MISTAKES.md)).

`sync.Once`는 교체와 종료가 같은 트리에 동시에 도달해도 정리가 정확히 한 번만
실행되게 한다. 커넥션 풀 이중 해제를 막는다.

## 패키지 구조

```
cmd/ondolith/          main. 플래그, 부팅 분기, 핸들러 교체, graceful shutdown
internal/
  config/              설정 파일 읽기·쓰기. 파일 존재 = 설치 완료 플래그
  install/             설치 트리 — 라우트 + 폼 검증 + 프로비저닝 + 템플릿(embed)
  app/                 운영 트리 — 풀·세션·라우트 조립
  migrations/          goose 마이그레이션 (embed.FS) + Run()
  auth/                권한 판정 순수 함수 (CanOn, 사다리 차단, rate limit). DB 접근 없음
  content/             입력 검증·페이지 상태 전이·메뉴 트리. DB 접근 없음
```

Phase 1 이후 추가될 자리:

```
  theme/               테마 로더 (embed 폴백 + 디스크 오버라이드), 함수맵
  admin/               관리자 트리
  commerce/            상품·주문·PG 어댑터
  hooks/               훅 포인트 (NFR-401, 로더 없음)
```

**규칙:** `internal/` 밖으로 무엇도 내보내지 않는다. 이 저장소는 라이브러리가 아니라 제품이다.

> 위 「패키지 구조」 목록과 실제 `internal/` 은 `make check` 가 매번 대조한다. 패키지를
> 추가하면 같은 커밋에서 이 목록에 넣어야 빌드가 통과한다 — 새 코드를 어디에 둘지 묻는
> 사람이 읽는 곳이 여기이기 때문이다. 「Phase 1 이후 추가될 자리」는 대조 대상이 아니다.

## 요청 흐름 (운영 모드)

```
요청
 → CrossOriginProtection        상태 변경 요청의 교차 출처 차단 (NFR-205)
 → scs.LoadAndSave              세션 로드 / 응답 시 저장
 → ServeMux                     Go 1.22+ 패턴 라우팅
 → 핸들러                        데이터 조회
 → 테마 렌더링                   html/template — 준비된 데이터만 받는다
```

미들웨어는 이 세 겹이 전부다. 더 늘리려면 [D10](10-requirements.md)의 요구사항 ID를 근거로 대야 한다.

## 모듈 게이팅 (FR-710)

설치/운영 트리를 나눈 것과 **같은 원칙을 커머스에도 적용한다.**

```
운영 트리 조립
 ├─ 핵심 라우트            항상 등록
 └─ site_mode == "shop" ?  ─예→ 커머스 라우트 등록 (P-3xx·P-4xx·P-5xx·P-905·A-5xx)
                           └아니오→ 등록하지 않음
```

**핸들러 안에서 `if 커머스켜짐`을 검사하지 않는다.** 조립 시점에 등록 여부를 정한다 —
분기를 핸들러에 넣으면 새 커머스 라우트를 추가할 때마다 검사를 빠뜨릴 수 있고,
빠뜨리면 커머스를 끈 사이트에 결제 경로가 열린다.

| 항목 | 처리 |
|---|---|
| 라우트 | `cms`면 등록하지 않는다 → 404 |
| 관리자 메뉴 | 등록된 라우트만 메뉴에 나온다. 메뉴는 라우트에서 파생되지 별도 목록이 아니다 |
| 마이그레이션 | **커머스 테이블도 항상 만든다.** 조건부 스키마는 마이그레이션 관리를 망가뜨린다 ([DEC-3.9](../.ai/DECISIONS.md)와 같은 논리) — 설치처마다 스키마가 달라지면 검증이 불가능하다 |

### 조립 시점에 정하는데 A-201이 바꾼다면

FR-710은 "나중에 바꿀 수 있다"고 하고 이 절은 "조립 시점에 정한다"고 한다. 둘 다 맞다 —
**A-201이 저장에 성공하면 운영 트리를 다시 조립해 통째로 원자 교체한다.** 재시작은 없다.

설치 트리 → 운영 트리 교체와 **같은 장치**다 (위 `root.swap`). 새 트리를 만들고 포인터를
바꾸면, 그 순간 진행 중인 요청은 옛 트리로 끝나고 다음 요청부터 새 트리로 간다.

| 항목 | 처리 |
|---|---|
| 재시작 | 필요 없다 (FR-303 테마 교체와 같은 성질) |
| 실패 시 | 조립이 실패하면 **교체하지 않는다.** 옛 트리가 계속 서비스한다 — 반쯤 조립된 트리를 세우는 것보다 낫다 |
| 진행 중 요청 | 옛 트리에서 끝난다. `shop → cms` 전환 순간 결제 진행 중이던 요청은 완료된다. 미완결 주문이 있으면 애초에 전환이 거부되므로(D13) 이 창은 좁다 |
| 정리 | 옛 트리가 소유한 자원은 `tree.close()`가 `sync.Once`로 한 번만 정리한다 ([M5](../.ai/MISTAKES.md)) |

**핸들러 안에 `if 커머스켜짐`을 넣지 않는 원칙은 그대로다.** 바뀌는 것은 "언제 조립하는가"
뿐이고, 조립된 트리 안에서 커머스 라우트는 여전히 있거나 없거나 둘 중 하나다.
| 권한 | 커머스 권한은 항상 존재한다 ([D15](15-access-control.md) P1). 부여하지 않을 뿐이다 |
| 되돌리기 | `shop` → `cms` 전환 시 주문 조회 화면은 남긴다 ([D11](11-screens.md) 모듈 구성) |

빈 테이블 12개의 비용은 저사양 서버에서도 무시할 수준이고, 그 대가로 **모든 설치처가 같은
스키마를 갖는다.** 업그레이드 때 분기가 없다는 뜻이다 ([D70](70-operations.md)).

## 커넥션

`pgxpool.Pool` **하나**를 앱과 세션 스토어가 공유한다. goose만 `*sql.DB`를 요구하므로
`stdlib.OpenDBFromPool(pool)`로 브리지를 만들어 넘긴다. 접속 설정이 한 군데뿐이어야 한다.

```go
pool, _ := pgxpool.New(ctx, cfg.DatabaseURL)
db := stdlib.OpenDBFromPool(pool)     // goose 전용, 마이그레이션 후 Close
sessions.Store = pgxstore.New(pool)   // scs
```

## CSRF: 표준 라이브러리

`net/http.CrossOriginProtection` (Go 1.25)을 쓴다. `gorilla/csrf`는 배제됐다 —
근거는 [DEC-2.1](../.ai/DECISIONS.md).

토큰 방식이 아니라 `Sec-Fetch-Site` / `Origin` 헤더 검사이므로 **테마 템플릿의 모든
`<form>`에 히든 토큰을 심을 필요가 없다.** 테마 작성자가 토큰을 빠뜨려 보호가 뚫리는
사고 자체가 사라진다 — 테마를 외부인이 쓰는 제품에서 이건 큰 차이다.

알려진 트레이드오프: `Sec-Fetch-Site`와 `Origin`이 **둘 다 없는** 요청은 통과한다.
2023년 이후 모든 브라우저가 `Sec-Fetch-Site`를 보내므로 실질 대상은 비브라우저
클라이언트이고, 거기엔 CSRF 개념이 적용되지 않는다.

## 라우터: 표준 라이브러리

Go 1.22+ `ServeMux`가 메서드·와일드카드 패턴을 지원한다. `chi`가 더 주는 것은
`Route`/`Use` 그룹 설탕 정도이고 작은 헬퍼로 대체된다. gin/echo/fiber는 배제 —
미들웨어 생태계를 끌고 오면 단일 바이너리의 이점이 희석된다. ([DEC-1](../.ai/DECISIONS.md))

```go
mux.HandleFunc("GET /{$}", home)              // "/" 정확히 일치
mux.HandleFunc("GET /board/{slug}", list)
mux.HandleFunc("GET /board/{slug}/{id}", view)
```

## 설정

단일 JSON 파일. 기본 경로 `./ondolith.json`, `-config`로 변경.

| 키 | 내용 |
|---|---|
| `database_url` | PostgreSQL DSN. **비밀번호 포함 → 파일 권한 0600** |
| `site_name` | 사이트 이름 |
| `installed_at` | 설치 시각 (UTC) |
| `secure_cookies` | 세션 쿠키 `Secure` 플래그. 설치 시 감지, 운영자가 편집 가능 |

쓰기는 임시 파일 + `rename`으로 원자적이다. 부분 기록된 설정 파일이 남으면 다음 부팅이
FR-110에 걸려 기동하지 못한다.

## 아직 없는 것

Phase 0 시점에서 의도적으로 비어 있다. 채우기 전에 [D10](10-requirements.md)에서 해당
FR을 확인할 것.

- 테마 로더 (FR-301~308) — 현재 운영 트리는 내장 템플릿 하나만 렌더링한다
- 인증·RBAC (FR-2xx) — `users.is_admin` 불리언 하나뿐. Phase 1에서 교체
- 관리자 트리 (FR-7xx)
- 훅 포인트 (NFR-401)
