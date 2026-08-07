#!/bin/sh
# toss-live.sh — W3-34. 토스 테스트 키로 어댑터를 실측한다.
#
# **`-run` 이 아무것도 고르지 못한 경우를 막는다.** 빌드 태그는 파일이
# 컴파일되는지를 정할 뿐이고, 이름 필터가 빗나가면 `go test` 는
# "no tests to run" 과 함께 exit 0 을 낸다 — 요청을 한 번도 보내지 않은 채
# W3-34 가 통과로 보고된다 (M15, check-testrun.sh 가 존재하는 이유).
#
# 종료 코드는 go test 의 것이다. 파이프로 넘기면 그 코드를 잃는다.
set -u

cd "$(dirname "$0")/.."

if [ -z "${ONDOLITH_TOSS_TEST_SECRET:-}" ]; then
	echo "  ✗ ONDOLITH_TOSS_TEST_SECRET 이 없다 — A-209 에 넣은 테스트 시크릿 키를 준다"
	echo "    절차는 docs/73-toss-verification.md"
	exit 1
fi

log=$(mktemp) || exit 1
trap 'rm -f "$log"' EXIT INT TERM

go test -tags tosslive -count=1 -v -run 'TestLiveToss' ./internal/commerce/ >"$log" 2>&1
code=$?

grep -E '^(ok|FAIL|--- (PASS|FAIL)|    )' "$log" || true

sh "$(dirname "$0")/check-testrun.sh" <"$log" || code=1
exit "$code"
