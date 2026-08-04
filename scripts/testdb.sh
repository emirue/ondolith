#!/bin/sh
# Starts, reuses and removes the local PostgreSQL that the DB-backed tests need.
#
# WHY THIS IS CAREFUL: the tests begin with `DROP SCHEMA public CASCADE`. Pointed
# at the wrong database that is not a failed test, it is someone's data. So this
# script never guesses which server it reached — it only ever hands back a DSN
# for a container it started itself, and it refuses to continue if anything else
# holds the port.
#
#   scripts/testdb.sh up     컨테이너를 띄우고(있으면 재사용) DSN 을 stdout 에 낸다
#   scripts/testdb.sh down   컨테이너를 지운다
#   scripts/testdb.sh dsn    DSN 만 출력 (띄우지 않는다)
#
# The container is left running after tests on purpose: a loop that re-runs the
# suite pays the ~4s startup once instead of every iteration. `down` removes it.
set -u

NAME=ondolith-testdb
PORT=55432
# D30 §3 / D21 1절 fix the minimum at 18: gen_random_uuid() without pgcrypto and
# UNIQUE NULLS NOT DISTINCT. An older image would pass some tests and fail the
# schema in ways that look like our bug.
IMAGE=postgres:18-alpine
USER=ondolith
PASS=ondolith
DB=ondolith
DSN="postgres://$USER:$PASS@127.0.0.1:$PORT/$DB?sslmode=disable"

case "${1:-up}" in
dsn)
	echo "$DSN"
	exit 0
	;;
down)
	if docker rm -f "$NAME" >/dev/null 2>&1; then
		echo "$NAME 제거됨" >&2
	else
		echo "$NAME 이 없다" >&2
	fi
	exit 0
	;;
up) ;;
*)
	echo "사용법: $0 [up|down|dsn]" >&2
	exit 2
	;;
esac

command -v docker >/dev/null 2>&1 || {
	echo "docker 가 없다. ONDOLITH_TEST_DSN 을 직접 설정하거나 PostgreSQL 18 을 띄우세요." >&2
	exit 1
}
docker info >/dev/null 2>&1 || {
	echo "docker 데몬이 응답하지 않는다. Docker Desktop 을 켜세요." >&2
	exit 1
}

# `docker inspect` on a missing container prints an empty line to stdout AND
# exits non-zero, so `... || echo missing` yields "\nmissing", which equals
# neither branch. The script then waited 60s on a container it never created —
# a failure that looks exactly like a slow database.
state=$(docker inspect -f '{{.State.Running}}' "$NAME" 2>/dev/null | tr -d '[:space:]')
[ -z "$state" ] && state=missing

if [ "$state" = "missing" ]; then
	# Someone else on our port means we do not know what we would be connecting
	# to. Refuse rather than DROP SCHEMA on it.
	holder=$(docker ps --filter "publish=$PORT" --format '{{.Names}}' | head -1)
	if [ -n "$holder" ]; then
		echo "포트 $PORT 를 다른 컨테이너가 쓴다: $holder" >&2
		echo "테스트는 DROP SCHEMA 로 시작한다 — 남의 DB 일 수 있어 중단한다." >&2
		exit 1
	fi
	docker run -d --name "$NAME" \
		-e POSTGRES_USER="$USER" -e POSTGRES_PASSWORD="$PASS" -e POSTGRES_DB="$DB" \
		-p "$PORT:5432" "$IMAGE" >/dev/null || {
		echo "$NAME 기동 실패" >&2
		exit 1
	}
	echo "$NAME ($IMAGE) 기동" >&2
elif [ "$state" = "false" ]; then
	docker start "$NAME" >/dev/null || {
		echo "$NAME 재시작 실패" >&2
		exit 1
	}
	echo "$NAME 재시작" >&2
fi

# Ready means "accepts connections", not "the container exists". Without this
# the first test run races the server's own initialisation and fails with a
# connection error that looks like a code problem.
i=0
until docker exec "$NAME" pg_isready -U "$USER" -d "$DB" >/dev/null 2>&1; do
	i=$((i + 1))
	if [ "$i" -ge 60 ]; then
		echo "$NAME 이 60초 안에 준비되지 않았다" >&2
		docker logs --tail 20 "$NAME" >&2
		exit 1
	fi
	sleep 1
done

echo "$DSN"
