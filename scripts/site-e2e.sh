#!/bin/sh
# 사이트 실행 파일 한 바퀴 (D42 1단계).
#
# 코어를 Go 모듈로 가져와 빌드한 사이트 실행 파일(examples/site)을 빈 DB 에 띄워, 설치부터 앱 요청까지 사람과 앱이
# 하는 순서대로 밟는다. Go 시험은 조립 함수를 직접 부른다 — 이것은 진짜 실행 파일과 진짜 HTTP 다:
#   설치 직후(재시작 없이) 사이트 표가 있다 → 관리자가 A-213 에서 켜고 키를 발급한다 → 앱이 그 키로 쓰고 읽는다 →
#   키 없음·틀린 키 401, 없는 주소 JSON 404 → 키를 폐기하면 401 → 끄면 404
#
# 실행: make e2e (scripts/e2e.sh 뒤에 이어 돈다)
set -u

cd "$(dirname "$0")/.." || exit 1

PORT=${SITE_E2E_PORT:-18104}
BASE="http://127.0.0.1:$PORT"
DB=ondolith_site_e2e
WORK=$(mktemp -d)
BIN=$WORK/site
JAR=$WORK/jar
ADMIN_PW='site-e2e-admin-passphrase'

pass=0
fail=0
ok() { pass=$((pass + 1)); printf '  ✓ %s\n' "$*"; }
bad() { fail=$((fail + 1)); printf '  ✗ %s\n' "$*"; }
want() { [ "$2" = "$3" ] && ok "$1" || bad "$1 — want $2, got $3"; }
code() { m=$1; p=$2; shift 2; curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -c "$JAR" -X "$m" "$BASE$p" "$@"; }
# 앱: 쿠키 없이, Origin 없이 — Flutter 의 http 패키지가 보내는 모양이다.
app() { m=$1; p=$2; k=$3; shift 3; curl -s -o "$WORK/body" -w '%{http_code}' -X "$m" -H "Authorization: Bearer $k" "$BASE$p" "$@"; }
sql() { docker exec "$CID" psql -U ondolith -d "$DB" -tAc "$1" 2>/dev/null | tr -d '\r'; }

cleanup() {
	[ -n "${SRV:-}" ] && kill "$SRV" 2>/dev/null
	[ -n "${CID:-}" ] && docker exec "$CID" psql -U ondolith -d postgres -q -c "DROP DATABASE IF EXISTS $DB" >/dev/null 2>&1
	rm -rf "$WORK"
}
trap cleanup EXIT INT TERM

DSN=$(sh scripts/testdb.sh dsn) || exit 1
PGPORT=$(echo "$DSN" | sed 's|.*:\([0-9]*\)/.*|\1|')
CID=$(docker ps --filter "publish=$PGPORT" -q)
[ -n "$CID" ] || { echo "테스트 DB 컨테이너가 없다 — sh scripts/testdb.sh up" >&2; exit 1; }
docker exec "$CID" psql -U ondolith -d postgres -q -c "DROP DATABASE IF EXISTS $DB" -c "CREATE DATABASE $DB OWNER ondolith" || exit 1

echo "사이트 실행 파일 한 바퀴 (examples/site)"
go build -o "$BIN" ./examples/site || exit 1
case "$("$BIN" -version)" in "ondolith "*) ok "-version 이 코어 버전을 말한다" ;; *) bad "-version: $("$BIN" -version)" ;; esac

(cd "$WORK" && exec "$BIN" -addr "127.0.0.1:$PORT" -config ./ondolith.json >>"$WORK/log" 2>&1) &
SRV=$!
i=0
until curl -s -o /dev/null "$BASE/"; do
	i=$((i + 1))
	[ "$i" -lt 100 ] || { echo "서버가 뜨지 않았다:"; cat "$WORK/log"; exit 1; }
	sleep 0.1
done

want "설치" 303 "$(code POST /install \
	--data-urlencode "db_host=127.0.0.1" --data-urlencode "db_port=$PGPORT" \
	--data-urlencode "db_name=$DB" --data-urlencode "db_user=ondolith" \
	--data-urlencode "db_password=ondolith" --data-urlencode "db_sslmode=disable" \
	--data-urlencode "site_name=사이트 확장" --data-urlencode "admin_email=admin@example.com" \
	--data-urlencode "admin_password=$ADMIN_PW" --data-urlencode "admin_password_confirm=$ADMIN_PW")"
want "설치 직후(재시작 없이) 사이트 표가 있다" 1 "$(sql "select count(*) from information_schema.tables where table_name='site_example_checkins'")"
want "사이트 마이그레이션은 따로 기록된다" 1 "$(sql "select count(*) from site_goose_db_version where version_id = 1")"

want "로그인" 303 "$(code POST /login --data-urlencode "email=admin@example.com" --data-urlencode "password=$ADMIN_PW")"
want "켜기 전 앱 요청은 404" 404 "$(app GET /api/site/v1/checkins ondo_none)"
want "A-213 켜기" 303 "$(code POST /admin/settings/api --data-urlencode "on=1")"
want "A-213 키 발급" 200 "$(curl -s -o "$WORK/issued" -w '%{http_code}' -b "$JAR" -c "$JAR" -X POST "$BASE/admin/settings/api/keys" --data-urlencode "name=e2e")"
KEY=$(grep -o 'value="ondo_[A-Za-z0-9_-]*"' "$WORK/issued" | head -1 | sed 's/value="\(.*\)"/\1/')
[ -n "$KEY" ] && ok "발급한 키가 화면에 한 번 보인다" || bad "발급 화면에서 키를 찾지 못했다"
want "다시 열면 키 원문이 없다" 0 "$(curl -s -b "$JAR" "$BASE/admin/settings/api" | grep -c "$KEY")"

want "앱: 출근 기록" 201 "$(app POST /api/site/v1/checkins "$KEY" -H 'Content-Type: application/json' --data '{"worker":"홍길동","kind":"in"}')"
want "앱: 목록" 200 "$(app GET /api/site/v1/checkins "$KEY")"
grep -q '"worker":"홍길동"' "$WORK/body" && ok "앱: 방금 쓴 기록이 보인다" || bad "앱: 목록에 기록이 없다 — $(cat "$WORK/body")"
want "사이트 처리기가 앱 이름을 받았다" e2e "$(sql "select app from site_example_checkins limit 1")"
want "앱: 틀린 본문은 400" 400 "$(app POST /api/site/v1/checkins "$KEY" --data '{"kind":"nap"}')"
want "앱: 키 없음 401" 401 "$(curl -s -o /dev/null -w '%{http_code}' "$BASE/api/site/v1/checkins")"
want "앱: 틀린 키 401" 401 "$(app GET /api/site/v1/checkins ondo_wrong)"
want "앱: 없는 주소 404" 404 "$(app GET /api/site/v1/nothing "$KEY")"
grep -q '"error"' "$WORK/body" && ok "앱: 오류는 JSON" || bad "앱: 오류가 JSON 이 아니다 — $(cat "$WORK/body")"

KID=$(sql "select id from api_keys where name='e2e'")
want "A-213 키 폐기" 303 "$(code POST /admin/settings/api/keys/revoke --data-urlencode "id=$KID")"
want "폐기한 키는 401" 401 "$(app GET /api/site/v1/checkins "$KEY")"
want "작업 로그에 키 원문이 없다" 0 "$(sql "select count(*) from operation_logs where position('$KEY' in summary) > 0")"

curl -s -o "$WORK/issued" -b "$JAR" -c "$JAR" -X POST "$BASE/admin/settings/api/keys" --data-urlencode "name=e2e-2"
KEY2=$(grep -o 'value="ondo_[A-Za-z0-9_-]*"' "$WORK/issued" | head -1 | sed 's/value="\(.*\)"/\1/')
want "새 키는 된다" 200 "$(app GET /api/site/v1/checkins "$KEY2")"
want "A-213 끄기" 303 "$(code POST /admin/settings/api)"
want "끄면 맞는 키로도 404" 404 "$(app GET /api/site/v1/checkins "$KEY2")"

echo
printf 'site-e2e: %d 통과 · %d 실패\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
