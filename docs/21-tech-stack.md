# D21. 기술 스택

**무엇으로 만드는가의 단일 출처다.** 배제된 선택지의 근거는 [.ai/DECISIONS.md](../.ai/DECISIONS.md),
구조는 [D20](20-architecture.md), 작성 규약은 [D22](22-dev-standards.md).

> 이 문서의 버전은 **조회로 확인한 값**이다. 기억으로 적지 않는다 ([M1](../.ai/MISTAKES.md)).
> `go.mod`와 어긋나면 `make check`가 실패한다 ([D90](90-conventions.md) 26번).

---

## 1. 툴체인

| 항목 | 값 |
|---|---|
| 언어 | Go |
| `go.mod` 최소 버전 | `1.26.6` |
| 확인 시점 | 2026-08-14 |

### 툴체인 버전은 보안 경계다

**표준 라이브러리 취약점은 툴체인을 올려야만 고쳐진다.** 의존성 업데이트로는 안 된다.

2026-08-14 `govulncheck` 결과: **우리 코드가 호출하는 경로에서 표준 라이브러리 취약점 7건**
(`GO-2026-6218`, `-6091`, `-6090`, `-6089`, `-6088`, `-5972`, `-5026`). 전부 `go1.26.6`에서
수정됐고, 올린 뒤 **0건**이 됐다 (NFR-209). 그중 `GO-2026-6091`은 `html/template` 이다 —
테마 렌더링 경로 전체가 그 패키지 위에 있다 ([DEC-3.1](../.ai/DECISIONS.md)).

**이것을 사람이 아니라 CI 가 찾았다.** `1.26.5`는 2026-08-03 기준 최신이었고 그때는 0건이었다 —
툴체인은 가만히 있어도 낡는다. `make vuln`이 `.github/workflows/ci.yml`에서 매 푸시마다
돌기 때문에 뒤처진 날 바로 빨간불이 됐다. 앞선 판(`1.25.7` → `1.26.5`)은 15건을 보고
사람이 올린 것이었다.

| 규칙 | 내용 |
|---|---|
| 릴리즈 전 | **`make vuln`을 반드시 돌린다.** 네트워크가 필요해 `make check`에는 넣지 않았다 |
| 취약점 발견 시 | 툴체인부터 올린다. 애플리케이션 코드로 우회하지 않는다 |
| `go.mod`의 `go` 지시자 | 실제로 검증한 버전으로 유지한다. 올릴 때 `make check` + `make test-integration`을 다시 돌린다 |

---

## 2. 직접 의존성

**5개뿐이다.** 하나를 더 늘리려면 4절의 기준을 통과해야 한다.

| 모듈 | 버전 | 용도 | 왜 이것인가 |
|---|---|---|---|
| `github.com/jackc/pgx/v5` | `v5.10.0` | PostgreSQL 드라이버·커넥션 풀 | Go의 사실상 표준. `database/sql`을 거치지 않는 네이티브 프로토콜 + `stdlib` 브리지를 함께 제공해 goose에도 같은 풀을 쓸 수 있다 |
| `github.com/pressly/goose/v3` | `v3.27.3` | 마이그레이션 | **`fs.FS`를 직접 받는다** → `embed.FS`로 바이너리에 넣고 별도 CLI 없이 실행할 수 있다 (FR-103) |
| `github.com/alexedwards/scs/v2` | `v2.9.0` | 서버측 세션 | OWASP 세션 패턴. 쿠키에 식별자만 두고 상태는 스토어에 둔다 (NFR-204) |
| `github.com/alexedwards/scs/pgxstore` | `v0.0.0-20251002162104-209de6e426de` | scs의 PostgreSQL 스토어 | 같은 `pgxpool.Pool`을 공유한다. 접속 설정이 한 군데다 |
| `golang.org/x/crypto` | `v0.54.0` | bcrypt | 비밀번호 해시 (NFR-208). 표준 라이브러리에 없다 |
| `rsc.io/qr` | `v0.2.0` | QR 인코딩 (FR-620) | **비트맵까지만 준다** — 사각형을 찍는 것은 우리 코드라 뷰박스·여백·SVG 출력을 우리가 정한다 (D50 은 서버에서 SVG 를 요구한다). API 가 `Encode`·`Black` 둘뿐이라 표면이 작고, 이미지 인코더·폰트 같은 딸린 의존성이 없다. 2026-08-06 조회: OSV 취약점 없음 |
| `github.com/markbates/goth` | `v1.82.0` | 소셜 로그인 OAuth2 (FR-208) | 프로바이더별 인가 흐름은 자작 금지 대상이다 (NFR-201). **`gothic` 하위 패키지는 쓰지 않는다** — gorilla/sessions 를 쓰는데 우리 세션은 scs 라 (NFR-204) 두 스토어가 한 요청에 있으면 어느 쪽이 진짜 로그인 상태인지가 코드마다 달라진다. `goth.Provider`·`goth.Session` 만 쓴다. 2026-08-07 조회: OSV 취약점 없음 |

**Phase 1에 추가 예정** — 착수 시 버전과 취약점을 다시 조회한다

| 모듈 | 용도 | 근거 |
|---|---|---|
| `github.com/markbates/goth` | 소셜 로그인 (FR-208) | 프로바이더별 OAuth 흐름. 자작 금지 대상 (NFR-201) |

`scs/pgxstore`가 유사 버전(pseudo-version)인 이유: 저장소가 서브모듈에 태그를 달지 않는다.
`scs/v2` 본체는 태그가 있다.

---

## 3. 의존성 없이 표준 라이브러리로 해결한 것

**이 목록이 이 프로젝트의 성격을 말한다.** 흔히 라이브러리를 쓰는 자리에 표준 라이브러리를 썼다.

| 영역 | 표준 라이브러리 | 흔한 대안 | 왜 안 썼나 |
|---|---|---|---|
| 라우팅 | `net/http.ServeMux` | chi, gorilla/mux, gin, echo | Go 1.22+가 메서드·와일드카드 패턴을 지원한다. chi가 더 주는 것은 그룹 설탕 정도이고 20줄 헬퍼로 대체된다. 프레임워크는 미들웨어 생태계를 끌고 와 단일 바이너리 이점을 희석한다 ([DEC-1](../.ai/DECISIONS.md)) |
| CSRF | `net/http.CrossOriginProtection` | gorilla/csrf | **gorilla/csrf는 최신 v1.7.3에도 수정 버전이 없는 CVE-2025-47909 대상이다.** 공식 권고가 표준 라이브러리로의 이전이다 ([DEC-2.1](../.ai/DECISIONS.md)) |
| 템플릿 | `html/template` | templ, quicktemplate | 코드 생성 방식은 **런타임 테마 교체와 정면 충돌**한다 ([DEC-3.1](../.ai/DECISIONS.md)) |
| 자산 내장 | `embed` | packr, statik | 표준 기능이다 |
| 로깅 | `log/slog` | zap, zerolog | 구조화 로깅이 표준에 들어왔다. 저사양 서버에서 성능 차이가 문제 되는 규모가 아니다 |
| 설정 | `encoding/json` | viper | 설정이 파일 하나에 4개 키다 ([D20](20-architecture.md)) |
| 백그라운드 작업 | 고루틴 | 워커 프로세스, 작업 큐 | 별도 프로세스를 요구하지 않는다 (NFR-103) |

---

## 4. 의존성 추가 기준

**추가하지 않는 것이 기본값이다.** 아래를 전부 통과해야 추가한다.

| # | 기준 |
|---|---|
| 1 | **표준 라이브러리로 안 되는가.** 되면 표준 라이브러리를 쓴다 |
| 2 | **직접 짜면 50줄을 넘는가.** 안 넘으면 직접 짠다 |
| 3 | **보안 경계인가.** 인증·세션·CSRF·암호·업로드 검증은 **반대로 자작 금지**다 (NFR-201). 1·2번보다 우선한다 |
| 4 | **취약점을 조회했는가** — 아래 절차 (NFR-209) |
| 5 | **유지되는가.** 최근 릴리즈, 미해결 보안 이슈, 대안 존재 여부 |
| 6 | **[.ai/DECISIONS.md](../.ai/DECISIONS.md)에 배제 기록이 있는가.** 있으면 추가할 수 없다 |

### 추가 전 조회 절차 (NFR-209)

```bash
go list -m -versions <module>
curl -s https://api.osv.dev/v1/query -d '{"package":{"name":"<module>","ecosystem":"Go"}}'
```

**최신 버전에 수정본이 없는 취약점이 있으면 그 라이브러리는 탈락이다.** `gorilla/csrf`가
그렇게 탈락했다.

정기 점검: `make vuln` (릴리즈 전 필수, 그 외 월 1회 권장)

---

## 5. 빌드·배포

| 항목 | 값 | 근거 |
|---|---|---|
| 대상 | `linux/amd64`, `linux/arm64` | Lightsail이 둘 다 판다 (NFR-306) |
| `CGO_ENABLED` | `0` | 정적 바이너리. 대상 서버의 glibc 버전을 신경 쓰지 않는다 |
| `-trimpath` | 사용 | 빌드 경로가 바이너리에 남지 않는다 |
| `-ldflags` | `-s -w -X main.version=vX.Y.Z` | 심볼 제거로 크기 축소, 버전 스탬프 (NFR-305) |
| 산출물 | 단일 바이너리 | 외부 런타임 의존 없음 (NFR-102) |

`make release`가 이 조합을 수행한다. 절차는 [D70](70-operations.md).

## 6. 개발 환경

| 필요한 것 | 용도 |
|---|---|
| Go (1절의 버전 이상) | 빌드·테스트 |
| htmx 2.0.9 | 프런트 인터랙션. **CDN 아닌 내장** (`internal/theme/builtin/static/js/`). 버전·sha256 은 같은 디렉터리 `htmx.VERSION`. 근거 [DEC-2.2](../.ai/DECISIONS.md) |
| PostgreSQL 18 | `make test-integration`이 **`ONDOLITH_TEST_DSN`이 없으면 Docker로 직접 띄운다** (`scripts/testdb.sh`, `postgres:18-alpine`). 컨테이너는 다음 실행을 위해 남으며 `make test-db-down`으로 지운다. DSN을 명시하면 그쪽이 우선한다 |
| `jq` | `gofmt` 훅이 후크 페이로드를 읽는다 |
| `perl` | `checkdocs.sh`의 패턴 추출 |
| Docker (선택) | 통합 테스트용 PostgreSQL |

`make help`가 사용 가능한 명령을 보여준다.
