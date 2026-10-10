# Ondolith — Claude Code 진입점

Go 단일 바이너리 CMS + 커머스. 브라우저에서 설치가 끝나고, 테마를 런타임에 교체한다.

## 작업 루프

```
1. 읽기    무엇을 만드는지 → docs/10-requirements.md 에서 FR/NFR ID 확인 (없으면 D90 대로 먼저 추가)
           어떻게 만드는지 → 아래 "문서 지도"에서 해당 문서 1개 + 같은 일을 하는 기존 코드
2. 구현    요청받은 것만. 추측으로 넓히지 않는다 — 모호하면 묻는다. 성공 조건(테스트)을 먼저 정한다
3. 검증    아래 "검증" — make check 통과 전에는 완료가 아니다
4. 검토    git diff 전체 — karpathy-diff 스킬, 로직이면 /pr-review-toolkit:review-pr code errors tests
           (.ai/ENGINEERING.md 6절). 걸리는 줄은 지우고 다시 검증한다
5. 갱신    바뀐 결정 → .ai/DECISIONS.md / 새 문서 → docs/README.md 인덱스
           사용자에게 보이는 변경 → CHANGELOG.md
```

여러 단계에 걸친 작업은 [D81](docs/81-work-breakdown.md) 의 W 행이 계획이자 진행 상태다 — 별도 상태
파일을 만들지 않는다 ([D82](docs/82-execution-loop.md) L3).

커밋 메시지에 요구사항 ID를 적는다. 예: `install: 관리자 계정 생성 (FR-104)`

## 문서 지도

전체 목록과 참조 규칙: **[docs/README.md](docs/README.md)** ← 막히면 여기부터

| 하려는 일 | 읽을 문서 |
|---|---|
| 무엇을 만들지 확인 | [docs/10-requirements.md](docs/10-requirements.md) |
| 라우팅·패키지·부팅 흐름 | [docs/20-architecture.md](docs/20-architecture.md) |
| 스키마·마이그레이션 | [docs/30-data-model.md](docs/30-data-model.md) |
| 테마·템플릿 | [docs/40-theme.md](docs/40-theme.md) |
| 결제 | [docs/50-commerce.md](docs/50-commerce.md) |
| 입력·인증·업로드 | [docs/60-security.md](docs/60-security.md) |
| 배포·업그레이드 | [docs/70-operations.md](docs/70-operations.md) |
| 다음에 뭘 할지 | [docs/80-roadmap.md](docs/80-roadmap.md) |
| **작업을 실제로 돌릴 때** | [docs/82-execution-loop.md](docs/82-execution-loop.md) — `scripts/next-task.sh` |
| **문서를 쓰거나 요구사항을 추가** | [docs/90-conventions.md](docs/90-conventions.md) |
| 패키지 경계·오류·로깅·SQL·테스트 관례 | [docs/22-dev-standards.md](docs/22-dev-standards.md) |
| **코드를 어떤 모습으로 남길지** | [.ai/ENGINEERING.md](.ai/ENGINEERING.md) — 공통화·리팩터링·간결함·diff 검토 |
| 코딩 행동 지침 (원문) | `karpathy-guidelines` 스킬 — 프로젝트 플러그인 `andrej-karpathy-skills`. 코드를 쓰거나 고칠 때 불러온다 |

**도구**: 쓰는 플러그인·스킬과 이 저장소에서 막아 둔 것(`.claude/settings.json`)은 [.ai/ENGINEERING.md](.ai/ENGINEERING.md) 6절.

**라이브러리·기술 선택을 제안하기 전에 반드시:** [.ai/DECISIONS.md](.ai/DECISIONS.md)
— 이미 배제된 것을 되살리지 않기 위한 목록이다.

**이 파일은 색인이다.** 규칙 본문은 위 표의 파일에 두고 여기에는 한 줄만 늘린다 —
진입점이 길어지면 아무도 끝까지 읽지 않고, 그때부터 규칙은 있으나 마나가 된다.

## 어기면 작업을 버리게 되는 규칙

1. **테마 렌더링 경로에는 `html/template`만.** 코드 생성·사전 컴파일(templ, Next.js 등)
   금지 — 재컴파일 없이 테마를 바꿔야 한다 ([DEC-3.1](.ai/DECISIONS.md))
2. **게시판마다 `CREATE TABLE` 금지.** 커스텀 필드는 JSONB ([DEC-3.9](.ai/DECISIONS.md))
3. **설치 트리와 운영 트리는 별개다.** 하나의 라우터에 `if installed` 분기를 넣지 않는다
4. **SQL은 항상 파라미터 바인딩.** 바인딩 불가한 값(정렬 컬럼 등)은 허용 목록으로 검사
5. **`template.HTML` 사용 지점마다 사유 주석**
6. **인증·세션·CSRF·업로드 검증을 자작하지 않는다**
7. **라이브러리 버전·API는 공식 문서로 확인하고 쓴다.** 기억으로 쓰지 않는다 ([M1](.ai/MISTAKES.md))

## 검증

```bash
# 빠른 검증 — 매 변경
make check            # build + vet + gofmt + test(-race) + docs + selftest — 완료 선언 전 필수
# 전체 검증 — 해당할 때. CI 와 같은 명령이다
make test-integration # 스키마·쿼리·트랜잭션을 건드렸을 때 (ONDOLITH_TEST_DSN, 없으면 Docker 로 띄운다)
make ui               # 템플릿·CSS·JS 를 바꿨을 때 — 전 화면 3폭 측정 + 에디터 브라우저 시험 (Chrome·Docker)
make screens && make crawl && make e2e   # 라우트·화면 흐름을 바꿨을 때 (Docker)
make run              # 로컬 실행 (개발)
```

돌리지 못한 검증은 통과로 세지 않고 「못 돌렸다」와 이유를 적는다.

- `docs` 단계가 [D90](docs/90-conventions.md)의 문서 규칙을 강제한다 — 깨진 링크,
  정의되지 않은 ID 인용, 인덱스 미등록 문서는 빌드를 깨뜨린다
- `selftest` 단계가 셸 도구(문서 검사기·gofmt 훅)에 위반을 심어 **실제로 실패하는지**
  확인한다. 검사를 추가하면 같은 커밋에서 주입 케이스도 추가한다

비자명한 로직(분기·루프·파서·금액/보안 경로)에는 테스트를 하나 남긴다. 자명한 한 줄짜리는 두지 않는다.

**테스트를 쓴 뒤 대상 코드를 일부러 고장내 실패하는지 확인한다.** 통과는 "테스트가
있다"는 뜻이지 "테스트가 문다"는 뜻이 아니다 ([M4](.ai/MISTAKES.md)). 빌드 환경에
의존하는 값(build info·환경변수·시각)은 순수 함수로 분리해야 테스트가 문다.

## 현재 상태

**여기에 Phase 를 적지 않는다.** 적으면 낡는다 — 네 Phase 가 끝난 뒤에도 이 줄은
「Phase 0 설치 마법사」였고, 그걸 읽고 시작한 작업이 이미 있는 것을 다시 만들었다.
Phase 표시는 [docs/80-roadmap.md](docs/80-roadmap.md) 한 곳에만 있고 `make check` 가
[docs/81-work-breakdown.md](docs/81-work-breakdown.md) 의 완료 상태와 대조한다.

지금 무엇을 할지: `sh scripts/next-task.sh` ([D82](docs/82-execution-loop.md))
