#!/bin/sh
# Runs the database-backed tests.
#
# Deliberately has NO `-run` name filter. A filter silently selects nothing
# when a test is renamed, and `go test` exits 0 on "[no tests to run]", so the
# target would report success while verifying nothing. Instead the whole
# package runs and check-testrun.sh asserts that something actually executed.
set -u

cd "$(dirname "$0")/.." || exit 1

if [ -z "${ONDOLITH_TEST_DSN:-}" ]; then
	echo "ONDOLITH_TEST_DSN 를 설정하세요. 예:"
	echo "  docker run -d --name ondolith-test -e POSTGRES_PASSWORD=testpw \\"
	echo "    -e POSTGRES_DB=ondolith -p 55432:5432 postgres:16-alpine"
	echo "  export ONDOLITH_TEST_DSN='postgres://postgres:testpw@127.0.0.1:55432/ondolith?sslmode=disable'"
	exit 1
fi

log=$(mktemp) || exit 1
trap 'rm -f "$log"' EXIT INT TERM

# The whole module, not a package list: naming packages here is the same trap
# as naming tests with -run (.ai/MISTAKES.md M6) — a new DSN-guarded test in an
# unlisted package would skip forever and nothing would say so.
# -p 1: every package here points at the SAME database, and each resets the
# schema before it runs. Packages run in parallel by default, so internal/install
# and internal/migrations were dropping the schema out from under each other —
# a failure that only appeared once migrations grew DB-backed tests of its own.
go test -race -count=1 -p 1 -v ./... >"$log" 2>&1
code=$?

grep -E '^(ok|FAIL|--- FAIL)' "$log" || true

# With the DSN set nothing in this package may skip: a SKIP here means the
# guard in the test did not see the environment the Makefile promised it.
sh "$(dirname "$0")/check-testrun.sh" <"$log" || code=1

exit "$code"
