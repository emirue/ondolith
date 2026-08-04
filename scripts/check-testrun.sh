#!/bin/sh
# Reads `go test -v` output on stdin and fails if the run did not actually
# exercise anything.
#
# `go test` exits 0 for "[no tests to run]" and for a run where every test
# skipped. A target that filters by name therefore reports success while
# verifying nothing — the same silent-pass shape as .ai/MISTAKES.md M3.
#
# Kept separate from integration.sh so that scripts/selftest.sh can feed it
# canned output and prove it rejects what it claims to reject.
set -u

out=$(cat)
fail=0

skipped=$(printf '%s\n' "$out" | grep -cE '^ *--- SKIP: ' || true)
ran=$(printf '%s\n' "$out" | grep -cE '^ *--- (PASS|FAIL): ' || true)

if printf '%s\n' "$out" | grep -q 'no tests to run'; then
	echo "  ✗ 실행된 테스트가 없다 — 이름 필터가 아무것도 고르지 못했다"
	fail=1
fi

if [ "$ran" -eq 0 ]; then
	echo "  ✗ PASS/FAIL 한 건도 없다 — 검증된 것이 없다"
	fail=1
fi

if [ "$skipped" -ne 0 ]; then
	echo "  ✗ 건너뛴 테스트 $skipped 건 — 통합 실행에서 SKIP 은 하네스 문제다"
	fail=1
fi

[ "$fail" -ne 0 ] && exit 1
echo "  ✓ 테스트 $ran 건 실행, SKIP 없음"
exit 0
