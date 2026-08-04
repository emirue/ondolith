# Ondolith 문서 인덱스

모든 문서의 목록. **새 문서를 만들면 반드시 이 표에 추가한다** — 등록하지 않으면
`make check`가 실패한다.

문서는 두 갈래다.

- **`docs/`** — 제품 문서. 무엇을 왜 만드는가. 사람과 에이전트가 함께 읽는다.
- **`.ai/`** — 작업 지침. 어떻게 만드는가. 에이전트가 매 세션 읽는다.

> **문서를 쓰거나 요구사항을 추가하기 전에 [D90 문서 작성 규칙](90-conventions.md)을 읽는다.**
> 번호 체계, ID 규칙, 새 문서를 만들 조건, 추가 절차가 전부 거기 있다.
> 기계로 검사되는 항목은 `scripts/checkdocs.sh`가 `make check`에서 강제한다.

---

## 제품 문서 (`docs/`)

| ID | 문서 | 내용 | 언제 읽나 |
|---|---|---|---|
| D00 | [00-overview.md](00-overview.md) | 제품 정의, 목표·비목표, 대상 사용자 | 프로젝트를 처음 접할 때 |
| D10 | [10-requirements.md](10-requirements.md) | **기획 문서.** FR/NFR 전체 명세 + Phase 배정 | 무엇을 만들지 정할 때 |
| D11 | [11-screens.md](11-screens.md) | **화면 인벤토리.** 모든 화면의 단일 정의 (경로·접근·상태변경) | 화면을 추가·수정할 때 |
| D12 | [12-screens-public.md](12-screens-public.md) | 공개 화면 상세 — 인증·콘텐츠·커머스 | 사용자 화면 구현 |
| D13 | [13-screens-admin.md](13-screens-admin.md) | 관리자 화면 상세 — 게시판 설정·커스텀 필드·주문 | 관리자 화면 구현 |
| D14 | [14-screen-flows.md](14-screen-flows.md) | 공개↔관리자 연계, 데이터 흐름, 상태머신 | 화면 간 관계가 헷갈릴 때 |
| D15 | [15-access-control.md](15-access-control.md) | **권한 모델과 화면별 접근 통제.** 역할·권한·검사 지점 | 권한을 다룰 때 (=거의 항상) |
| D16 | [16-data-coverage.md](16-data-coverage.md) | **데이터 커버리지.** 테이블마다 만드는 화면·보여주는 화면 | 테이블·화면을 추가할 때 |
| D17 | [17-theme-contract.md](17-theme-contract.md) | **테마 계약.** 템플릿 목록·함수맵·뷰 모델 — 테마 개발자용 | 테마를 만들거나 렌더링을 건드릴 때 |
| D18 | [18-open-decisions.md](18-open-decisions.md) | **미결 대장.** 아직 안 정한 것 전부 (`OPEN-##`) | Phase 착수 전, 무엇을 먼저 정할지 볼 때 |
| D19 | [19-screen-io.md](19-screen-io.md) | **화면 입력·검증 명세.** 63개 화면의 받는 것·**받지 않는 것**·오류·거부 조건 | 폼과 핸들러를 만들 때 |
| D20 | [20-architecture.md](20-architecture.md) | 부팅 모드, 라우트 트리, 패키지 구조, 요청 흐름 | 코드를 건드리기 전 |
| D21 | [21-tech-stack.md](21-tech-stack.md) | **기술 스택.** 의존성 전수·버전·선택 근거·추가 기준·빌드 | 라이브러리를 고르거나 추가할 때 |
| D22 | [22-dev-standards.md](22-dev-standards.md) | **개발 규약.** 코딩·오류·로깅·동시성·SQL·테스트 전략·구현 순서 | 코드를 쓸 때 (=항상) |
| D30 | [30-data-model.md](30-data-model.md) | 테이블 설계, 커스텀 필드(JSONB), 마이그레이션 규칙 | 스키마를 바꿀 때 |
| D40 | [40-theme.md](40-theme.md) | 테마 로딩·오버라이드·계약, 템플릿 함수맵 | 테마/렌더링 작업 |
| D50 | [50-commerce.md](50-commerce.md) | 상품·주문 모델, PG 어댑터, 토스페이먼츠 연동 | Phase 3 |
| D60 | [60-security.md](60-security.md) | 위협 모델, 방어 규칙, 취약 지점 체크리스트 | 입력·인증·업로드를 다룰 때 |
| D70 | [70-operations.md](70-operations.md) | 배포, **업그레이드(패치) 절차**, 백업, 모니터링 | 릴리즈·운영 |
| D80 | [80-roadmap.md](80-roadmap.md) | Phase별 범위와 완료 기준 | 다음에 뭘 할지 정할 때 |
| D81 | [81-work-breakdown.md](81-work-breakdown.md) | **작업 분해.** Phase별 작업·선행 관계·완료 기준·임계 경로 | 무엇부터 만들지 정할 때 |
| D82 | [82-execution-loop.md](82-execution-loop.md) | **실행 루프.** 종료 신호·진행 표시·재개 절차 (`scripts/next-task.sh`) | 작업을 실제로 돌릴 때 |
| D90 | [90-conventions.md](90-conventions.md) | **문서 작성 규칙.** 번호·ID·추가 절차·기계 검증 | 문서를 쓰기 전 |

## 작업 지침 (`.ai/`)

| 문서 | 내용 |
|---|---|
| [.ai/CLAUDE.md](../.ai/CLAUDE.md) | 루트 `CLAUDE.md`로 가는 포인터 (규칙 본문 없음) |
| [.ai/DECISIONS.md](../.ai/DECISIONS.md) | **확정 스택 + 배제된 선택지.** 되살리지 말 것 |
| [.ai/PATTERNS.md](../.ai/PATTERNS.md) | 이 저장소의 코드 관례 |
| [.ai/MISTAKES.md](../.ai/MISTAKES.md) | 반복된 실수 기록 |

## 저장소 루트

| 문서 | 내용 |
|---|---|
| [CLAUDE.md](../CLAUDE.md) | **작업 규칙 본문.** Claude Code 진입점 |
| [README.md](../README.md) | 저장소 첫인상, 빌드 방법 |
| [CONTRIBUTING.md](../CONTRIBUTING.md) | 기여 절차 |
| [CHANGELOG.md](../CHANGELOG.md) | 릴리즈 변경 이력 |

---

## ID 빠른 참조

정식 규칙과 할당 절차는 [D90](90-conventions.md) 5절. 여기는 찾아보기용이다.

| 접두사 | 뜻 | 정의 위치 |
|---|---|---|
| `D##` | 문서 | 이 인덱스 |
| `FR-###` / `NFR-###` | 기능 / 비기능 요구사항 | [D10](10-requirements.md) |
| `DEC-#` / `DEC-#.#` | 확정·배제 결정 | [.ai/DECISIONS.md](../.ai/DECISIONS.md) |
| `M#` | 기록된 실수 | [.ai/MISTAKES.md](../.ai/MISTAKES.md) |

커밋 메시지 본문에 요구사항 ID를 적는다. 예: `install: 관리자 계정 생성 (FR-104)`
