#!/bin/sh
# Proves the repo's shell tooling fails when it is supposed to.
#
# checkdocs.sh only earns trust if it is shown to REJECT violations, not merely
# to pass on a clean tree — an earlier version reported violations and still
# exited 0 (see .ai/MISTAKES.md M3). The same applies to the gofmt hook, whose
# branches (bad JSON, empty input, non-Go paths) are otherwise never exercised.
#
# Both are checked here by injecting faults into a throwaway copy of the repo,
# and this runs as part of `make check`.
set -u

cd "$(dirname "$0")/.." || exit 1
ROOT=$(pwd)

fail=0
err() {
	printf '  ✗ %s\n' "$*"
	fail=1
}
ok() { printf '  ✓ %s\n' "$*"; }

TMP=$(mktemp -d) || exit 1
trap 'rm -rf "$TMP"' EXIT INT TERM

REPO=$TMP/repo
cp -R "$ROOT" "$REPO" || exit 1
rm -rf "$REPO/.git" "$REPO/dist" "$REPO/ondolith"

# detected <name> <expected-substring>: the checker must REPORT the violation,
# EXIT non-zero, and report it through THE CHECK WE MEANT TO EXERCISE.
#
# The first two together guard .ai/MISTAKES.md M3, where the checker printed
# violations from inside a piped `while` loop but still exited 0 because the
# loop body ran in a subshell. Output alone, or exit code alone, would each
# have missed that.
#
# The third guards M10: an injection satisfied by a DIFFERENT check leaves the
# intended one unproven. Two injections passed that way — one for a check whose
# `comm` had four arguments and died on every run (docs/90-conventions.md 9절).
detected() {
	name=$1
	want=$2
	out=$(sh "$REPO/scripts/checkdocs.sh" 2>&1)
	code=$?

	reported=no
	case "$out" in *✗*) reported=yes ;; esac

	hit=no
	case "$out" in *"$want"*) hit=yes ;; esac

	if [ "$reported" = yes ] && [ "$code" -ne 0 ] && [ "$hit" = yes ]; then
		ok "탐지: $name"
	elif [ "$reported" = yes ] && [ "$code" -eq 0 ]; then
		err "M3 재발 — 위반을 출력했는데 exit 0: $name"
	elif [ "$hit" = no ] && [ "$reported" = yes ]; then
		err "M10 — 다른 검사가 대신 잡았다. '$want' 가 출력에 없다: $name"
	elif [ "$code" -ne 0 ]; then
		err "exit $code 인데 위반 내용을 출력하지 않았다: $name"
	else
		err "위반을 탐지하지 못함: $name"
	fi
}

# inject <name> <file-to-restore> <shell-command> <expected-substring>
inject() {
	cp "$REPO/$2" "$TMP/backup"
	(cd "$REPO" && eval "$3")
	detected "$1" "$4"
	cp "$TMP/backup" "$REPO/$2"
}

# inject_new <name> <file-to-delete> <shell-command> <expected-substring>
inject_new() {
	(cd "$REPO" && eval "$3")
	detected "$1" "$4"
	rm -f "$REPO/$2"
}

echo "selftest: checkdocs 실패 주입"

if sh "$REPO/scripts/checkdocs.sh" >/dev/null 2>&1; then
	ok "기준선: 위반 없는 사본은 통과"
else
	err "기준선 실패 — 사본이 이미 규칙을 어기고 있다"
fi

inject "깨진 링크" docs/00-overview.md \
	'printf "\n[없는문서](99-nope.md)\n" >> docs/00-overview.md' \
	'깨진 링크: ./docs/00-overview.md'
inject "미정의 FR 인용" docs/20-architecture.md \
	'printf "\nFR-999 참조\n" >> docs/20-architecture.md' \
	'정의되지 않은 요구사항 인용: FR-999'
inject "FR 중복 정의" docs/10-requirements.md \
	'printf "| FR-101 | 중복 | 필수 | 0 | x |\n" >> docs/10-requirements.md' \
	'요구사항 중복 정의: FR-101'
inject "완료 기준이 빈 요구사항" docs/10-requirements.md \
	'perl -pi -e "s/^(\| NFR-204 \|[^|]*\|[^|]*\|)[^|]*\|/\$1 |/" docs/10-requirements.md' \
	'완료 기준이 비어 있다'
# A full cell can still be worthless: "…인지 결론" is an open decision, not a
# criterion, and the empty-cell check above passes it (old FR-206).
inject "완료 기준이 심의 형태" docs/10-requirements.md \
	'perl -pi -e "s/^(\| NFR-204 \|[^|]*\|[^|]*\|)[^|]*\|/\${1} 도입할지 검토 |/" docs/10-requirements.md' \
	'완료 기준이 심의 형태다'
# The gap that let 카테고리 관리·송장 입력 go missing: a mandatory feature with a
# public screen consuming its data but nothing creating it.
inject "필수 FR 인데 실현하는 화면이 없음" docs/10-requirements.md \
	'printf "| FR-899 | 화면 없는 신규 기능 | 필수 | 3 | 검증 가능한 기준 |\n" >> docs/10-requirements.md' \
	'필수 요구사항을 실현하는 화면이 없다: FR-899'
# FR-616 is cited by exactly one screen (A-510), so removing it leaves the
# requirement with no screen at all — which is what this check detects.
inject "화면이 유일하게 인용하던 필수 FR 을 제거" docs/11-screens.md \
	'perl -pi -e "s/^(\| A-510 \|.*)\| FR-616 \|/\$1| FR-604 |/" docs/11-screens.md' \
	'필수 요구사항을 실현하는 화면이 없다: FR-616'
inject "미정의 DEC 인용" docs/30-data-model.md \
	'printf "\nDEC-9.9 참조\n" >> docs/30-data-model.md' \
	'정의되지 않은 결정 인용: DEC-9.9'
inject "미정의 M# 인용" docs/60-security.md \
	'printf "\nM77 참조\n" >> docs/60-security.md' \
	'정의되지 않은 실수 인용: M77'
inject "H1 ID 불일치" docs/30-data-model.md \
	'perl -pi -e "s/^# D30\./# D31./ if \$.==1" docs/30-data-model.md' \
	'H1이 '\''# D30. '\''로 시작하지 않음'
inject "옛 결정 ID 부활" docs/40-theme.md \
	'printf "\n(D3.1 참조)\n" >> docs/40-theme.md' \
	'옛 결정 ID 형식 (DEC- 를 쓸 것): ./docs/40-theme.md'
inject "인덱스가 없는 파일을 지목" docs/README.md \
	'perl -pi -e "s/\(80-roadmap\.md\)/(81-ghost.md)/g" docs/README.md' \
	'인덱스가 없는 파일을 가리킴: docs/81-ghost.md'
inject_new "인덱스 미등록 문서" docs/15-orphan.md \
	'printf "# D15. 고아 문서\n" > docs/15-orphan.md' \
	'인덱스 미등록: 15-orphan.md'
inject_new "파일명 규칙 위반" docs/Bad_Name.md \
	'printf "# D15. 잘못된 이름\n" > docs/Bad_Name.md' \
	'파일명 규칙 위반 (NN-kebab-case.md): Bad_Name.md'

# Several violations inside a single check, and a violation that only trips the
# LAST check: both are the shapes M3 made disappear.
inject "한 검사 안에서 위반 여러 건" docs/20-architecture.md \
	'printf "\nFR-991 FR-992 FR-993 참조\n" >> docs/20-architecture.md' \
	'정의되지 않은 요구사항 인용: FR-993'
inject "마지막 검사에서만 위반" docs/50-commerce.md \
	'printf "\n(D2.9 참조)\n" >> docs/50-commerce.md' \
	'옛 결정 ID 형식 (DEC- 를 쓸 것): ./docs/50-commerce.md'

# Screen inventory rules (docs/90-conventions.md 6-1절).
inject "미정의 화면 인용" docs/20-architecture.md \
	'printf "\nP-998 참조\n" >> docs/20-architecture.md' \
	'정의되지 않은 화면 인용: P-998'
inject "화면 중복 정의" docs/11-screens.md \
	'perl -nle "if (/^\\|\\s*P-\\d{3}\\s*\\|/) { print; exit }" docs/11-screens.md >> docs/11-screens.md' \
	'화면 중복 정의: P-001'
inject "접근 값이 허용 목록 밖" docs/11-screens.md \
	'printf "| P-997 | 나쁜접근 | \`/x\` | GET | 아무나 | 없음 | SC-1 | FR-510 |\n" >> docs/11-screens.md' \
	'접근 값이 허용 목록에 없다 (P-997)'
inject "접근이 역할로 표기됨 (권한이어야 함)" docs/11-screens.md \
	'printf "| P-994 | 역할표기 | \`/w\` | GET | 역할:operator | 없음 | SC-1 | FR-510 |\n" >> docs/11-screens.md' \
	'접근 값이 허용 목록에 없다 (P-994)'
inject "상태변경 값이 허용 목록 밖" docs/11-screens.md \
	'printf "| P-996 | 나쁜상태 | \`/y\` | GET | 공개 | 어쩌면 | SC-1 | FR-510 |\n" >> docs/11-screens.md' \
	'상태변경 값이 허용 목록에 없다 (P-996)'
inject "화면 유형이 SC-1..SC-8 밖" docs/11-screens.md \
	'printf "| P-993 | 미분류 | \`/v\` | GET | 공개 | 없음 | SC-9 | FR-510 |\n" >> docs/11-screens.md' \
	'화면 유형이 SC-1..SC-8 이 아니다 (P-993)'
inject "관리자 화면이 권한 없이 접근 가능" docs/11-screens.md \
	'printf "| A-999 | 무방비 관리자 | \`/admin/x\` | GET | 공개 | 없음 | SC-4 | FR-701 |\n" >> docs/11-screens.md' \
	'관리자 화면인데 접근이 권한이 아니다 (A-999)'
inject "상태변경 화면인데 상세 절 없음" docs/11-screens.md \
	'printf "| P-995 | 상세없는 폼 | \`/z\` | POST | 공개 | 있음 | SC-2 | FR-510 |\n" >> docs/11-screens.md' \
	'상태변경 화면인데 상세 절이 없다: P-995'

# Tech stack (D21): a dependency table that drifts from go.mod is a wrong
# answer presented as a verified one.
inject "의존성 표에서 모듈 한 줄 누락" docs/21-tech-stack.md \
	'perl -ni -e "print unless /^\| \`golang\.org\/x\/crypto\` \|/" docs/21-tech-stack.md' \
	'없거나 버전이 다른 의존성: golang.org/x/crypto'
inject "의존성 표의 버전이 go.mod 와 다름" docs/21-tech-stack.md \
	'perl -pi -e "s/^(\| \`golang\.org\/x\/crypto\` \| \`)v[0-9.]+(\`)/\${1}v0.0.1\${2}/" docs/21-tech-stack.md' \
	'go.mod 에 없는 의존성을 적었다: golang.org/x/crypto v0.0.1'
inject "go.mod 최소 버전 표기 누락" docs/21-tech-stack.md \
	'perl -pi -e "s/\x60[0-9]+\.[0-9]+\.[0-9]+\x60/(미기재)/g" docs/21-tech-stack.md' \
	'go.mod 의 최소 버전'

# Open-decision ledger (M9): per-document tables let decided items linger.
inject "다른 문서에 미결 표가 다시 생김" docs/50-commerce.md \
	'printf "\n## 아직 정하지 않은 것\n\n| 항목 | 결정 시점 |\n|---|---|\n| 흩어진 미결 | Phase 3 |\n" >> docs/50-commerce.md' \
	'docs/50-commerce.md 에 미결 표가 있다'
# Duplicate an ID that is actually in the ledger — appending a resolved ID
# would create no duplicate and the case would pass without testing anything.
inject "미결 ID 중복" docs/18-open-decisions.md \
	'perl -nle "print; print if /^\| OPEN-\d\d \|/ && !\$done++" docs/18-open-decisions.md > /tmp/led && cp /tmp/led docs/18-open-decisions.md' \
	'미결 ID 중복: OPEN-'
# M11: six inline `- **미결**:` notes kept saying "not decided" after the ledger
# rows were deleted. The link is what makes the two fall together.
inject "인라인 미결이 대장을 인용하지 않음" docs/13-screens-admin.md \
	'printf "\n- **미결**: 대장에 없는 무언가\n" >> docs/13-screens-admin.md' \
	'인라인 미결이 대장 항목을 인용하지 않았다'
inject "인라인 미결이 닫힌 항목을 인용" docs/13-screens-admin.md \
	'printf "\n- **미결**: 이미 결정된 것 (OPEN-99)\n" >> docs/13-screens-admin.md' \
	'대장에 없는 항목을 인용한다 (이미 결정된 것 아닌가): OPEN-99'
inject "인라인이 아닌 인용이 대장에 없는 번호를 가리킴" docs/19-screen-io.md \
	'printf "\n| 어떤 화면 | 무언가 (OPEN-97) |\n" >> docs/19-screen-io.md' \
	'대장에 없는 미결 번호를 인용한다'
inject "미결 대장이 비어 읽히지 않음" docs/18-open-decisions.md \
	'perl -pi -e "s/^\| OPEN-/| XPEN-/" docs/18-open-decisions.md' \
	'OPEN- 항목을 하나도 읽지 못했다'

# 문서가 자기 자신의 낡은 사본을 품는 것. D50 이 153줄짜리 중간 블록을 3벌
# 갖고 있었고, 그 사본들은 W3-02 가 닫은 결정을 "아직 정하지 않은 것" 이라고
# 적고 있었다 — 링크도 ID도 제목도 멀쩡해서 다른 검사는 전부 통과했다.
inject "문서 안에 낡은 사본이 남음" docs/50-commerce.md \
	'sed -n "1,40p" docs/50-commerce.md >> docs/50-commerce.md' \
	'그대로 복제돼 있다'

# Theme contract (FR-308): a screen with no template silently falls through to
# whatever the core happens to render.
inject "템플릿도 예외도 없는 공개 화면" docs/17-theme-contract.md \
	'perl -ni -e "print unless /^\| \`auth\/login\.html\` \| P-101 \|/" docs/17-theme-contract.md' \
	'템플릿도 예외도 없는 화면: P-101'
inject "D17 이 존재하지 않는 화면에 템플릿 배정" docs/17-theme-contract.md \
	'printf "| \`ghost.html\` | P-207 | 폴백 |\n" >> docs/17-theme-contract.md' \
	'화면에 템플릿을 배정했다: P-207'
inject "템플릿 표 형식이 깨져 이름을 못 읽음" docs/17-theme-contract.md \
	'perl -pi -e "s/^\| \`([a-z][a-z0-9\/._-]*\.(html|xml|txt))\` \|/| \$1 |/" docs/17-theme-contract.md' \
	'템플릿 이름을 하나도 읽지 못했다'
# The contract must cover what the core actually renders, and the fallback
# theme must hold every name it promises. D17 said `auth/verify.html` while the
# code rendered verify-done.html — the checks above compare screen IDs and
# never look at a file name.
inject "코어가 그리는 템플릿이 D17 에 없음" docs/17-theme-contract.md \
	'perl -ni -e "print unless /^\| \`auth\/verify\.html\` \|/" docs/17-theme-contract.md' \
	'docs/17-theme-contract.md 에 없다: auth/verify.html'
inject_new "D17 이 약속한 템플릿이 폴백 테마에 없음" internal/theme/builtin/auth/verify.html \
	'mv internal/theme/builtin/auth/verify.html "$TMP/verify.html"' \
	'폴백 테마에 없다: internal/theme/builtin/auth/verify.html'
cp "$TMP/verify.html" "$REPO/internal/theme/builtin/auth/verify.html"

# 주문 상태머신 (FR-604): D14 5절 다이어그램과 Go 표가 어긋나면, 문서에만 있는
# 전이는 관리자가 못 하는 조작이고 코드에만 있는 전이는 아무도 합의하지 않은
# 조작이다. 양쪽 방향을 다 주입한다.
inject "다이어그램에 있는 전이가 코드에 없음" internal/commerce/state.go \
	'perl -ni -e "print unless /재배송 도착/" internal/commerce/state.go' \
	'internal/commerce/state.go 에 없는 전이: 교환발송 배송완료'
inject "코드에만 있는 전이" internal/commerce/state.go \
	'perl -pi -e "s/^\tStatusReturnOpen: \{$/\tStatusReturnOpen: {\n\t\tStatusRefunded: {\"A-511\"},/" internal/commerce/state.go' \
	'다이어그램에 없는 전이: 반품접수 환불'
inject "5절 다이어그램이 사라짐" docs/14-screen-flows.md \
	'perl -pi -e "s/^## 5\. 주문 상태머신 \(FR-604\)/## 5. 주문 상태/" docs/14-screen-flows.md' \
	'전이를 하나도 읽지 못했다'

# Module gating (FR-710): a plain site must not register commerce routes, which
# only holds if D11 actually declares the split.
inject "모듈 구성 절이 사라짐" docs/11-screens.md \
	'perl -pi -e "s/^## 모듈 구성 \(FR-710\)/## 기타/" docs/11-screens.md' \
	''\''## 모듈 구성'\'' 절이 없다'
inject "P-905 모듈 예외 표기가 사라짐" docs/11-screens.md \
	'perl -pi -e "s/P-905/P-9xx-웹훅/g" docs/11-screens.md' \
	'P-905 의 모듈 예외를 적지 않았다'

# Data coverage: the check that would have caught the six missing admin screens
# (.ai/MISTAKES.md M8) — a table with a consumer but no producer.
inject "D30 테이블이 커버리지 표에 없음" docs/16-data-coverage.md \
	'perl -ni -e "print unless /^\| \`shipments\` \|/" docs/16-data-coverage.md' \
	'에 없는 테이블: shipments'
inject "만드는 화면 칸이 비어 있음" docs/16-data-coverage.md \
	'perl -pi -e "s/^(\| \`shipments\` \|)[^|]*\|/\$1 |/" docs/16-data-coverage.md' \
	'만드는 화면이 비어 있다: shipments'
inject "보여주는 화면 칸이 비어 있음" docs/16-data-coverage.md \
	'perl -pi -e "s/^(\| \`shipments\` \|[^|]*\|)[^|]*\|/\$1 |/" docs/16-data-coverage.md' \
	'보여주는 화면이 비어 있다: shipments'
inject "커버리지 표에 D30 에 없는 테이블" docs/16-data-coverage.md \
	'printf "| \`ghost_table\` | A-101 | P-201 | |\n" >> docs/16-data-coverage.md' \
	'D30 에 없는 테이블을 적었다: ghost_table'

# Connection integrity: D11 and D15 hold the same screen↔permission map in two
# hand-written places. The bidirectional case (마지막) is where M3 came back a
# third time — a `while` on the right of a pipe swallowed every failure.
# A screen with no work item never gets built — the WBS is what someone reads
# to decide what to do next. Ranges are expanded, so this must survive them.
# A document that lies about the code is the hardest drift to notice: both
# sides look right on their own. A Phase 1 column really did get written into
# the shipped-schema section.
#
# The regexes match the backtick with `.` on purpose — a literal backtick here
# is command-substituted by the shell when selftest evals the injection, which
# silently turns the whole command into a no-op.
inject "D30 이 마이그레이션에 없는 컬럼을 적음" docs/30-data-model.md \
	'perl -pi -e "s/display_name/ghost_col/ if /^. .display_name. ./" docs/30-data-model.md' \
	'이 마이그레이션에 없는 컬럼을 적었다: users.ghost_col'
inject "마이그레이션에 있는 컬럼이 D30 에서 빠짐" docs/30-data-model.md \
	'perl -ni -e "print unless /^. .display_name. ./" docs/30-data-model.md' \
	'에 없는 컬럼: users.display_name'
# Phase 1 turned ten documented tables into real SQL. The check now follows
# every CREATE TABLE, so drift in a table that is not `users` must fail too —
# the old version only ever compared users against 00001.
inject "Phase 1 테이블의 컬럼이 D30 과 어긋남" internal/migrations/00004_content.sql \
	'perl -pi -e "s/^(\\s+template   text.*)\$/\$1\\n    author_id  uuid,/" internal/migrations/00004_content.sql' \
	'에 없는 컬럼: pages.author_id'
inject "마이그레이션이 만드는 테이블에 D30 정의가 없음" docs/30-data-model.md \
	'perl -pi -e "s/^..(.)menus(.)../**\${1}menus_renamed\${2}**/" docs/30-data-model.md' \
	'만드는 테이블인데 docs/30-data-model.md 에 정의가 없다: menus'
# D82 runs D81 as a loop: next-task.sh picks the task whose prerequisites are
# all done. A dangling prerequisite makes it wait on nothing, a cycle deadlocks
# it with no explanation, and a row marked done without its deliverable makes
# the remaining count a lie.
inject "선행이 표에 없는 작업을 가리킴" docs/81-work-breakdown.md \
	'perl -pi -e "s/^(\\| W1-07 \\| 권한 판정 순수 함수[^|]*\\|)[^|]*\\|/\$1 W1-99 |/" docs/81-work-breakdown.md' \
	'선행 W1-99 가 표에 없다'
inject "선행 관계에 순환이 생김" docs/81-work-breakdown.md \
	'perl -pi -e "s/^(\\| W1-03 \\| RBAC 스키마[^|]*\\|)[^|]*\\|/\$1 W1-13 |/" docs/81-work-breakdown.md' \
	'선행 관계에 순환이 있다'
inject "완료로 표시했는데 산출물이 없음" docs/81-work-breakdown.md \
	'perl -pi -e "s{^(\\| W1-03 \\|[^|]*\\|[^|]*\\|) .00002_rbac[.]sql. \\|}{\$1 \x60 00002_ghost.sql\x60 |}" docs/81-work-breakdown.md' \
	'산출물이 없다'
inject "요약표 합계가 실제 작업 수와 다름" docs/81-work-breakdown.md \
	'perl -pi -e "s/\\| \\*\\*합계\\*\\* \\| \\*\\*(\\d+)\\*\\*/qq(| **합계** | **) . (\$1-1) . qq(**)/e" docs/81-work-breakdown.md' \
	'요약표 합계'
inject "요약표 Phase 작업 수가 실제와 다름" docs/81-work-breakdown.md \
	'perl -pi -e "s/^(\\| Phase 3 — 커머스 \\| )(\\d+)( \\|)/qq(\$1) . (\$2-1) . qq(\$3)/e" docs/81-work-breakdown.md' \
	'요약표 W3 작업 수'
# D20's package list is where someone reads to decide where new code goes.
# W1-02 asked for a one-time re-check; a one-time look is worth nothing the next
# time a package appears, so both directions run every build.
inject "D20 이 없는 패키지를 현재 구조로 적음" docs/20-architecture.md \
	'perl -pi -e "s|^(  migrations/)|  ghostpkg/           없는 패키지\\n\$1|" docs/20-architecture.md' \
	'없는 패키지를 현재 구조로 적었다: internal/ghostpkg'
inject_new "새 패키지가 D20 에 없음" internal/ghostpkg/x.go \
	'mkdir -p internal/ghostpkg && printf "package ghostpkg\\n" > internal/ghostpkg/x.go' \
	'internal/ghostpkg 가 docs/20-architecture.md 「패키지 구조」에 없다'
# The boot self-check trusts screens.go to say what D11 declares. A copy that
# drifts makes that check confidently wrong in both directions, so both are
# injected: an entry the doc does not have, and a doc screen the map lost.
inject "인벤토리 유형이 D11 과 다름" internal/app/screens.go \
	'perl -pi -e "s/^(\\t\"P-101\": SC)2,/\${1}5,/" internal/app/screens.go' \
	'internal/app/screens.go 의 항목이 docs/11-screens.md 과 다르다: P-101'
inject "D11 화면이 인벤토리에 없음" internal/app/screens.go \
	'perl -ni -e "print unless /^\\t\"A-601\":/" internal/app/screens.go' \
	'docs/11-screens.md 의 화면이 internal/app/screens.go 에 없다: A-601'
# Phase 마다 시드 파일이 하나씩 는다. 검사가 첫 파일만 읽으면 뒤의 시드는
# D15 와 대조되지 않은 채 지나간다 — perl 에 여러 파일을 넘기고 <> 로 슬러프
# 하면 첫 `-- +goose Down` 에서 잘려 정확히 그렇게 된다.
inject "Phase 2 시드의 부여가 D15 와 다름" internal/migrations/00009_board_seed.sql \
	"perl -0777 -pi -e \"s/^\\s*\\('operator', 'board\\.view'\\),\\n//m\" internal/migrations/00009_board_seed.sql" \
	'심지 않았다: grant/operator/board.view'
inject "Phase 2 시드가 스코프 권한을 전역 부여" internal/migrations/00009_board_seed.sql \
	"perl -0777 -pi -e \"s/(\\('operator', 'board\\.view'\\),)/('member',   'post.read'),\\n    \\1/\" internal/migrations/00009_board_seed.sql" \
	'없는 것을 심는다: grant/member/post.read'
inject "화면에 작업 항목이 없음" docs/81-work-breakdown.md \
	'perl -pi -e "s/\\(P-511~P-513\\)/(P-511~P-512)/" docs/81-work-breakdown.md' \
	'작업 항목이 없는 화면: P-513'
inject "고아 화면 (상세 문서에 언급 없음)" docs/13-screens-admin.md \
	'perl -pi -e "s/A-601/A-6zz/g" docs/13-screens-admin.md' \
	'고아 화면 — docs/13-screens-admin.md 에 언급이 없다: A-601'

# Input spec (D19): a state-changing screen without one is a form whose
# validation gets invented while it is being written.
inject "상태변경 화면인데 입력 명세가 없음" docs/19-screen-io.md \
	'perl -pi -e "s/^### P-101\b/### P-1zz/" docs/19-screen-io.md' \
	"상태변경 화면인데 입력 명세가 없다: P-101"
inject "D19 가 존재하지 않는 화면을 명세" docs/19-screen-io.md \
	'printf "\n### P-992 유령 화면\n\n본문\n" >> docs/19-screen-io.md' \
	'없는 화면을 docs/19-screen-io.md 가 명세한다: P-992'
inject "D19 절 제목 형식이 깨져 하나도 못 읽음" docs/19-screen-io.md \
	'perl -pi -e "s/^### ([PA]-\d{3})/### \[\$1\]/" docs/19-screen-io.md' \
	'화면 절을 하나도 찾지 못했다'
# D19 restates its own scope three ways. Two screens were added to the sections
# and to every other document while D19's headline count and 대상 table stayed
# behind — 15-1 compares D19 against D11 and never notices.
inject "D19 대상 표가 자기가 명세한 화면을 빠뜨림" docs/19-screen-io.md \
	'perl -pi -e "s{^(\\| A-5xx )\\((\\d+)\\)( \\|.*) A-\\d{3} \\|\$}{sprintf(qq[%s(%d)%s |], \$1, \$2-1, \$3)}e" docs/19-screen-io.md' \
	'대상 표에 올리지 않은 화면: A-'
inject "D19 대역 행의 선언 개수가 실제와 다름" docs/19-screen-io.md \
	'perl -pi -e "s{^(\\| A-5xx )\\((\\d+)\\)}{sprintf(qq[%s(%d)], \$1, \$2+1)}e" docs/19-screen-io.md' \
	'대역 A-5xx 의 선언 개수가 실제와 다르다'
inject "D19 머리말의 화면 개수가 절 수와 다름" docs/19-screen-io.md \
	'perl -pi -e "s{^## 공개 화면 \\((\\d+)\\)}{sprintf(qq[## 공개 화면 (%d)], \$1-1)}e" docs/19-screen-io.md' \
	'가 그 구간의 실제 값'
# A wrong number that is itself a real count: 28 is the admin figure, written
# over the public one. Set membership passed this — the check now binds each
# figure to the region it appears in.
inject "D19 개수가 다른 구간의 실제 값으로 바뀜" docs/19-screen-io.md \
	'perl -0777 -pi -e "my (\$a) = /^## 관리자 화면 \\((\\d+)\\)/m; s/^## 공개 화면 \\(\\d+\\)/## 공개 화면 (\$a)/m" docs/19-screen-io.md' \
	'가 그 구간의 실제 값'
inject "D19 개수 진술이 하나도 남지 않음" docs/19-screen-io.md \
	'perl -pi -e "s/화면|대상/스크린/g" docs/19-screen-io.md' \
	'화면 개수 진술을 하나도 읽지 못했다'
# The class and the permission are written twice, in D11 and again in D19.
# Two hand-written copies drift (M9), and this pair drifting means the
# validation spec defends a screen that is locked differently.
inject "D19 의 보안 유형이 D11 과 다름" docs/19-screen-io.md \
	'perl -pi -e "s/SC-5/SC-4/ if /^### A-201 /" docs/19-screen-io.md' \
	'A-201 머리줄이 docs/11-screens.md 과 다르다'
inject "D19 의 요구 권한이 D11 과 다름" docs/19-screen-io.md \
	'perl -pi -e "s/settings\.update/theme.activate/ if /^### A-201 /" docs/19-screen-io.md' \
	'A-201 머리줄이 docs/11-screens.md 과 다르다'
# D80's 기획 완결성 table answers "얼마나 됐나" with numbers copied by hand from
# other documents. Five of them were stale for two days while `make check` was
# green (.ai/MISTAKES.md M12).
inject "D80 기획 완결성 표의 숫자가 낡음" docs/80-roadmap.md \
	'perl -pi -e "s{화면 인벤토리 \\((\\d+)개\\)}{sprintf(qq[화면 인벤토리 (%d개)], \$1-1)}e" docs/80-roadmap.md' \
	'11-screens.md 행의 숫자가 문서에서 센 값과 다르다'
inject "D80 기획 완결성 표에서 숫자가 통째로 사라짐" docs/80-roadmap.md \
	'perl -pi -e "s{\\(FR \\d+ / NFR \\d+\\)}{}" docs/80-roadmap.md' \
	'10-requirements.md 행의 숫자가 문서에서 센 값과 다르다: [없음]'
# Same two numbers, wrong order. Set membership called this correct.
inject "D80 표의 두 숫자가 자리만 뒤바뀜" docs/80-roadmap.md \
	'perl -pi -e "s{\\(FR (\\d+) / NFR (\\d+)\\)}{(FR \$2 / NFR \$1)}" docs/80-roadmap.md' \
	'10-requirements.md 행의 숫자가 문서에서 센 값과 다르다'
inject "D80 기획 완결성 표를 찾지 못함" docs/80-roadmap.md \
	'perl -pi -e "s/^## 기획 완결성.*/## 진행 상황/" docs/80-roadmap.md' \
	'기획 완결성 표에서 숫자를 가진 행을 하나도 읽지 못했다'
# The RBAC seed is D15 §2.2/§2.5 copied into INSERTs. D81's own risk table says
# the mitigation is to parse the document and compare — a second hand-written
# copy is the M9/M11/M12 failure, and here it would silently lock a role out of
# a screen, because permissions carry no implication (D15 §2.1).
inject "시드가 D15 의 부여를 빠뜨림" internal/migrations/00003_rbac_seed.sql \
	'perl -ni -e "print unless /.operator., .role\\.view./" internal/migrations/00003_rbac_seed.sql' \
	'있는 것을 심지 않았다: grant/operator/role.view'
inject "시드가 D15 에 없는 부여를 심음" internal/migrations/00003_rbac_seed.sql \
	'perl -pi -e "s{^(    \\(.editor.,   .page\\.view.\\),)\$}{\$1\\n    (\x27member\x27,   \x27page.view\x27),}" internal/migrations/00003_rbac_seed.sql' \
	'없는 것을 심는다: grant/member/page.view'
# 시드가 있는 Phase 는 1·2 다. 3 으로 옮기면 그 권한은 아직 심지 않아야 한다.
inject "D15 가 권한을 아직 시드 없는 Phase 로 옮김" docs/15-access-control.md \
	'perl -pi -e "s{^(\\| .menu\\.manage. \\|[^|]*\\|[^|]*\\|)\\s*1\\s*(\\|)}{\${1} 3 \$2}" docs/15-access-control.md' \
	'없는 것을 심는다: grant/operator/menu.manage'
inject "D15 매트릭스에서 부여가 사라졌는데 시드가 그대로" docs/15-access-control.md \
	'perl -pi -e "if (/^### 2\\.5 /) { \$i = 1 } elsif (\$i && /^### /) { \$i = 0 } s/●/ / if \$i && /^\\| .menu\\.manage. \\|/" docs/15-access-control.md' \
	'없는 것을 심는다: grant/operator/menu.manage'
inject "정의되지 않은 권한을 화면이 요구" docs/11-screens.md \
	'perl -pi -e "s/\| 권한:log\.view \|/| 권한:nosuch.perm |/" docs/11-screens.md' \
	'정의되지 않은 권한을 화면이 요구한다: nosuch.perm'
inject "권한이 존재하지 않는 화면을 지목" docs/15-access-control.md \
	'perl -pi -e "s/\| A-601 \|/| A-999 |/" docs/15-access-control.md' \
	'권한 log.view 가 존재하지 않는 화면을 가리킨다: A-999'
inject "권한↔화면 대응이 한쪽에만 있음" docs/15-access-control.md \
	'perl -pi -e "s/\| P-203, P-204, P-211 \|/| P-203, P-211 |/" docs/15-access-control.md' \
	'post.read 행이 P-204 를 사용 화면으로 적지 않았다'
# Delete the row from the §2.5 seed matrix only (its cells are empty or ●),
# leaving the §2.2 definition intact — otherwise a different check fires and
# this case would never exercise the drift detector it is written for.
inject "시드 매트릭스에서만 권한 한 줄 누락" docs/15-access-control.md \
	'perl -ni -e "print unless /^\| \`log\.view\` \|(\s*\|)+\s*\$/ || /^\| \`log\.view\` \|[ ●|]*\$/" docs/15-access-control.md' \
	'권한이 2.5 시드 매트릭스에 없다: log.view'

# The legacy-id marker must exempt exactly the line it is on — no more.
cp "$REPO/docs/40-theme.md" "$TMP/backup"
printf '\n옛 형식 D3.1 인용 <!-- checkdocs:allow-legacy-id -->\n' >>"$REPO/docs/40-theme.md"
if sh "$REPO/scripts/checkdocs.sh" >/dev/null 2>&1; then
	ok "마커가 붙은 줄은 통과"
else
	err "마커가 붙은 줄을 여전히 위반으로 본다"
fi
cp "$TMP/backup" "$REPO/docs/40-theme.md"

echo "selftest: 테스트 실행 검사기 (check-testrun.sh)"

CTR=$ROOT/scripts/check-testrun.sh

# expect_ctr <expected-exit> <name> <canned go test -v output>
expect_ctr() {
	printf '%s' "$3" | sh "$CTR" >/dev/null 2>&1
	code=$?
	if [ "$code" -eq "$1" ]; then
		ok "$2 → exit $code"
	else
		err "$2 → exit $code, want $1"
	fi
}

# `go test` exits 0 for all of these, which is exactly why they are checked.
expect_ctr 1 "이름 필터가 아무것도 못 고름" \
	'ok  	pkg	0.1s [no tests to run]
'
expect_ctr 1 "전부 SKIP" \
	'=== RUN TestA
--- SKIP: TestA (0.00s)
ok  	pkg	0.1s
'
expect_ctr 1 "하위 테스트만 SKIP" \
	'=== RUN TestA
--- PASS: TestA (0.00s)
    --- SKIP: TestA/sub (0.00s)
ok  	pkg	0.1s
'
expect_ctr 1 "PASS/FAIL 한 건도 없음" \
	'ok  	pkg	0.1s
'
expect_ctr 0 "정상 실행" \
	'=== RUN TestA
--- PASS: TestA (0.00s)
--- PASS: TestB (0.00s)
ok  	pkg	0.1s
'
# `[no test files]` and `[no tests to run]` look alike and mean opposite things:
# the first is a package with nothing to test, the second is a filter that
# selected nothing. Conflating them makes the checker either useless or noisy.
expect_ctr 0 "테스트 없는 패키지가 섞여도 오탐 없음" \
	'=== RUN TestA
--- PASS: TestA (0.00s)
ok  	github.com/x/a	0.1s
?   	github.com/x/b	[no test files]
'
expect_ctr 1 "전부 테스트 없는 패키지" \
	'?   	github.com/x/a	[no test files]
?   	github.com/x/b	[no test files]
'
expect_ctr 1 "FAIL 만 있어도 실행은 인정, SKIP 이 있으면 거부" \
	'=== RUN TestA
--- FAIL: TestA (0.00s)
--- SKIP: TestB (0.00s)
FAIL	github.com/x/a	0.1s
'
expect_ctr 0 "FAIL 만 있으면 검사기는 통과 (실패 판정은 go test 몫)" \
	'=== RUN TestA
--- FAIL: TestA (0.00s)
FAIL	github.com/x/a	0.1s
'

# integration.sh must never exit 0 without having actually run the DB tests.
#
# It used to guarantee that by refusing whenever ONDOLITH_TEST_DSN was unset. It
# now starts a local container instead, so the guarantee moves: with no DSN and
# no docker there is no database to be had, and it must fail rather than report
# success. The path WITH docker is exercised by `make test-integration` itself.
#
# /usr/bin:/bin keeps sh and dirname but drops /usr/local/bin, where docker
# lives. Emptying PATH entirely would make this pass for the wrong reason — the
# script would die on a missing `dirname` before ever reaching the docker check
# (M10), and the output below is asserted to prove which branch ran.
out=$(unset ONDOLITH_TEST_DSN; PATH=/usr/bin:/bin; export PATH;
	/bin/sh "$ROOT/scripts/integration.sh" 2>&1)
code=$?
case "$out" in
*"docker 가 없다"*) reason=docker ;;
*) reason=other ;;
esac
if [ "$code" -eq 0 ]; then
	err "integration.sh 가 DB 없이 통과했다"
elif [ "$reason" != docker ]; then
	err "integration.sh 가 실패하긴 했으나 docker 부재 때문이 아니다: $out"
else
	ok "integration.sh 는 DB 를 얻지 못하면 거부한다"
fi

echo "selftest: 작업 선택기 (next-task.sh)"

# next-task.sh drives the loop in D82, so its exit code is the signal that says
# "keep going", "stop, done", or "stop, broken". If all three were 0 an
# automated loop could not tell finished from deadlocked — the same shape of
# silent success as M3.
#
# Fixtures rather than mutations of the real 123-task table: a deadlock needs
# EVERY remaining task blocked, which a one-line edit to the real document
# cannot produce, and an injection that fails to inject proves nothing (M10).
NT=$ROOT/scripts/next-task.sh

# expect_nt <expected-exit> <name> <docs/81 body>
expect_nt() {
	fix=$TMP/nt
	rm -rf "$fix"
	mkdir -p "$fix/docs" "$fix/scripts"
	printf '# D81. 작업 분해\n\n| ID | 작업 | 선행 | 산출물 | 완료 기준 |\n|---|---|---|---|---|\n%s' "$3" \
		>"$fix/docs/81-work-breakdown.md"
	cp "$NT" "$fix/scripts/next-task.sh"
	sh "$fix/scripts/next-task.sh" >/dev/null 2>&1
	code=$?
	if [ "$code" -eq "$1" ]; then
		ok "$2 → exit $code"
	else
		err "$2 → exit $code, want $1"
	fi
}

expect_nt 0 "착수 가능한 작업이 있다" \
	'| W1-01 | 첫 작업 | — | `a.sql` | 기준 |
'
expect_nt 0 "선행이 완료된 작업을 고른다" \
	'| W1-01 | 첫 작업 **(완료 — x)** | — | `a.sql` | 기준 |
| W1-02 | 둘째 작업 | W1-01 | `b.sql` | 기준 |
'
expect_nt 2 "남은 작업이 없다" \
	'| W1-01 | 첫 작업 **(완료 — x)** | — | `a.sql` | 기준 |
'
expect_nt 3 "남았는데 전부 서로를 기다린다 (교착)" \
	'| W1-01 | 첫 작업 | W1-02 | `a.sql` | 기준 |
| W1-02 | 둘째 작업 | W1-01 | `b.sql` | 기준 |
'
expect_nt 1 "표를 하나도 읽지 못한다" \
	'| W1-01 : 첫 작업 : — : `a.sql` : 기준 |
'
# A prerequisite outside the table must not block forever: D81 has rows whose
# 선행 names a release, not a task ("Phase 1 릴리즈").
expect_nt 0 "표 밖 선행은 착수를 막지 않는다" \
	'| W2-01 | 첫 작업 | Phase 1 릴리즈 | `a.sql` | 기준 |
'

echo "selftest: 테스트 DB 관리 (testdb.sh)"

# testdb.sh hands the DB-backed tests their DSN, and those tests open with
# DROP SCHEMA public CASCADE. The branches checked here are the ones that must
# not silently do nothing: an unknown subcommand, and a machine with no docker.
#
# The docker-dependent paths (create / reuse / refuse a port someone else holds)
# need a daemon, so they are exercised by `make test-integration` itself rather
# than here — `make check` has to run offline.
TDB=$ROOT/scripts/testdb.sh

# expect_tdb <expected-exit> <name> <args> [PATH override]
expect_tdb() {
	if [ $# -ge 4 ]; then
		# /bin/sh by absolute path: with PATH emptied the shell itself would not
		# be found and the run would exit 127 before reaching the script.
		( PATH=$4 && export PATH && /bin/sh "$TDB" $3 ) >/dev/null 2>&1
	else
		sh "$TDB" $3 >/dev/null 2>&1
	fi
	code=$?
	if [ "$code" -eq "$1" ]; then
		ok "$2 → exit $code"
	else
		err "$2 → exit $code, want $1"
	fi
}

expect_tdb 0 "dsn 은 띄우지 않고 출력만 한다" dsn
expect_tdb 2 "모르는 하위 명령은 거부한다" bogus
# An empty PATH removes docker. Without the guard the script would run
# `docker inspect`, get "command not found", and wait 60 seconds on a container
# that can never exist — the failure this branch replaces.
expect_tdb 1 "docker 가 없으면 즉시 중단한다" up /nonexistent-bin

# The DSN it prints must be the one it starts: if these drifted apart the tests
# would connect somewhere the script never created.
if [ "$(sh "$TDB" dsn)" = "postgres://ondolith:ondolith@127.0.0.1:55432/ondolith?sslmode=disable" ]; then
	ok "dsn 출력이 스크립트가 띄우는 컨테이너와 일치"
else
	err "dsn 출력이 기대와 다르다: $(sh "$TDB" dsn)"
fi

echo "selftest: gofmt 훅"

HOOK=$ROOT/.claude/hooks/gofmt-on-write.sh

hook_exit() {
	printf '%s' "$1" | sh "$HOOK" >/dev/null 2>&1
	echo $?
}

for case_name in '깨진 JSON:not json' '빈 입력:' '빈 객체:{}' \
	'file_path 없음:{"tool_input":{}}' \
	'없는 파일:{"tool_input":{"file_path":"/nonexistent/x.go"}}'; do
	label=${case_name%%:*}
	payload=${case_name#*:}
	code=$(hook_exit "$payload")
	if [ "$code" -eq 0 ]; then
		ok "훅이 조용히 통과: $label"
	else
		err "훅이 exit $code — 훅 오류 알림을 유발한다: $label"
	fi
done

UGLY=$TMP/ugly.go
printf 'package x\n\nfunc  F( )  {\nvar a  int\n_ = a\n}\n' >"$UGLY"
code=$(hook_exit "{\"tool_input\":{\"file_path\":\"$UGLY\"}}")
if [ "$code" -ne 0 ]; then
	err "훅이 Go 파일에서 exit $code"
elif [ -n "$(gofmt -l "$UGLY")" ]; then
	err "훅이 Go 파일을 포맷하지 않았다"
else
	ok "훅이 Go 파일을 포맷했다"
fi

NOTGO=$TMP/keep.md
printf 'package x\nfunc  F( ){}\n' >"$NOTGO"
before=$(cat "$NOTGO")
code=$(hook_exit "{\"tool_input\":{\"file_path\":\"$NOTGO\"}}")
if [ "$code" -ne 0 ]; then
	err "훅이 비-Go 파일에서 exit $code"
elif [ "$(cat "$NOTGO")" != "$before" ]; then
	err "훅이 비-Go 파일을 건드렸다"
else
	ok "훅이 비-Go 파일을 건드리지 않았다"
fi

if [ "$fail" -ne 0 ]; then
	printf '\nselftest 실패\n'
	exit 1
fi
echo "selftest ok"
