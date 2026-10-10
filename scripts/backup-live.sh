#!/bin/sh
# backup-live.sh — FR-713. 실제 pg_dump·pg_restore 로 백업 덤프 → 빈 DB 복원 → 같은 글을 본다.
#
# test-integration 에 넣지 않는 이유: DB 서버(18)와 같거나 높은 버전의 pg_dump·pg_restore 가 PATH 에
# 있어야 한다. 시험은 pgdump 빌드 태그 뒤에 있어 기본 실행에서는 컴파일되지 않는다.
# 이름 필터가 빗나가도 exit 0 이 나지 않게 check-testrun.sh 로 실제 실행을 확인한다 (toss-live.sh 와 같다).
set -u
cd "$(dirname "$0")/.." || exit 1
if ! command -v pg_dump >/dev/null || ! command -v pg_restore >/dev/null; then
	echo "  ✗ PATH 에 pg_dump·pg_restore 가 없다 — DB 서버와 같은 버전의 PostgreSQL 클라이언트를 설치한다"
	exit 1
fi
pg_dump --version
if [ -z "${ONDOLITH_TEST_DSN:-}" ]; then
	ONDOLITH_TEST_DSN=$(sh "$(dirname "$0")/testdb.sh" up) || exit 1
	export ONDOLITH_TEST_DSN
fi
log=$(mktemp) || exit 1
trap 'rm -f "$log"' EXIT INT TERM
go test -tags pgdump -count=1 -p 1 -v -run 'TestBackupRestoresIntoAnEmptyDatabase' ./internal/app/ >"$log" 2>&1
code=$?
grep -E '^(ok|FAIL|--- (PASS|FAIL|SKIP)|    )' "$log" || true
sh "$(dirname "$0")/check-testrun.sh" <"$log" || code=1
exit "$code"
