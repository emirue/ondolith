#!/bin/sh
# 사이트 실행 파일 한 바퀴 (D42 1·2·3단계).
#
# 코어를 Go 모듈로 가져와 빌드한 사이트 실행 파일(examples/site)을 빈 DB 에 띄워, 설치부터 앱 요청까지 사람과 앱이
# 하는 순서대로 밟는다. Go 시험은 조립 함수를 직접 부른다 — 이것은 진짜 실행 파일과 진짜 HTTP 다:
#   설치 직후(재시작 없이) 사이트 표가 있다 → 관리자가 A-213 에서 켜고 키를 발급한다 → 앱이 그 키로 쓰고 읽는다 →
#   키 없음·틀린 키 401, 없는 주소 JSON 404 → 관리자가 사이트 화면에서 그 기록을 보고 지운다(알림·작업 로그) →
#   A-201 저장으로 트리를 다시 조립해도 사이트 화면이 남는다 → 인부를 등록하고 앱이 문자 인증으로 토큰을 받아 자기
#   출근을 찍는다(등록 안 된 번호도 같은 응답, 다섯 번 틀리면 잠김) → 그만둔 인부의 토큰은 401, 문자 상한 429 →
#   키를 폐기하면 401 → 끄면 404
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

echo "사이트 실행 파일 한 바퀴 (examples/site 를 별도 모듈로)"
# **사이트 저장소와 같은 모양으로 빌드한다.** 코어 모듈 안에서 빌드하면 internal/ 을 그대로 볼 수 있고, 빌드 정보의
# 주 모듈이 코어라 사이트 버전 갈래가 한 번도 돌지 않는다. 별도 모듈이 replace 로 코어를 가져와 site 만 쓴다 —
# juwon_forestry 가 할 일과 같다. go.sum 은 코어 것에서 시작해 tidy 가 맞춘다.
MOD=$WORK/sitemod
mkdir -p "$MOD/migrations"
cp examples/site/main.go "$MOD/" && cp examples/site/migrations/*.sql "$MOD/migrations/" && cp go.sum "$MOD/go.sum"
{
	echo "module example.com/sitee2e"
	echo
	grep -E '^(go|toolchain) ' go.mod
	echo
	echo "require github.com/emirue/ondolith v0.0.0"
	echo
	echo "replace github.com/emirue/ondolith => $PWD"
} >"$MOD/go.mod"
(cd "$MOD" && go mod tidy >"$WORK/tidy.log" 2>&1 && go build -o "$BIN" .) || { cat "$WORK/tidy.log"; exit 1; }
# replace 로 끼운 코어의 판은 거짓이라 dev 다. 사이트 모듈 경로가 따로 붙는다 (D42 12절).
case "$("$BIN" -version)" in
"ondolith dev "*"· 사이트 example.com/sitee2e "*) ok "밖의 모듈이 site 로 빌드되고, -version 이 코어(dev)와 사이트를 따로 말한다" ;;
*) bad "-version: $("$BIN" -version)" ;;
esac

# 인증번호는 문자 대신 로그로 나간다 — 예제의 개발용 발송(EXAMPLE_SMS=log). SENS 는 단위 시험의 가짜 서버가 본다.
(cd "$WORK" && EXAMPLE_SMS=log exec "$BIN" -addr "127.0.0.1:$PORT" -config ./ondolith.json >>"$WORK/log" 2>&1) &
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

# ---- 사이트 관리자 화면 (D42 7절, A-214) -------------------------------------
page() { curl -s -o "$WORK/page" -w '%{http_code}' -b "$JAR" -c "$JAR" "$BASE$1"; }
page /admin/ >/dev/null
grep -q 'href="/admin/site/checkins"' "$WORK/page" && grep -q '>사이트</div>' "$WORK/page" &&
	ok "사이드바 「사이트」 묶음에 사이트 화면이 있다" || bad "사이드바에 사이트 화면이 없다"
want "사이트 화면: 목록" 200 "$(page /admin/site/checkins)"
grep -q '홍길동' "$WORK/page" && grep -q '<title>출퇴근 기록 — 관리자</title>' "$WORK/page" &&
	ok "사이트 화면이 코어 관리자 틀 안에 사이트 표를 그린다" || bad "사이트 화면에 틀이나 기록이 없다"
CHK=$(sql "select id from site_example_checkins order by id limit 1")
want "사이트 화면: 상세 — 숫자 id (코어의 uuid 검사를 받지 않는다)" 200 "$(page "/admin/site/checkins/$CHK")"
want "사이트 화면: 숫자가 아닌 id 는 사이트가 404" 404 "$(page /admin/site/checkins/abc)"
want "사이트 화면: 기록 지우기" 303 "$(code POST "/admin/site/checkins/$CHK")"
page /admin/site/checkins >/dev/null
grep -q '기록을 지웠습니다.' "$WORK/page" && ok "돌아간 화면에 저장 알림이 한 번 보인다" || bad "저장 알림이 없다"
want "지운 기록은 없다" 0 "$(sql "select count(*) from site_example_checkins where id = $CHK")"
want "작업 로그에 사이트 동작이 관리자 이름으로 남는다" 1 "$(sql "select count(*) from operation_logs l join users u on u.id = l.actor_user_id
	where l.action = 'site.checkin_delete' and l.target_type = 'site' and l.target_id = '$CHK' and u.email = 'admin@example.com'")"
page /admin/roles >/dev/null
grep -q 'site.checkins — 출퇴근 관리' "$WORK/page" && ok "역할 편집기가 사이트 권한을 이름과 함께 보인다" || bad "역할 편집기에 사이트 권한 이름이 없다"
page /admin/system >/dev/null
grep -q 'example.com/sitee2e' "$WORK/page" && ok "시스템 정보에 사이트 빌드가 보인다" || bad "시스템 정보에 사이트 빌드가 없다"
# 관리자 배색은 조립 때 읽는 값이라 A-201 이 운영 트리를 다시 조립한다 (D20). 다시 조립한 트리에도 사이트 화면이 있어야 한다.
TYPE=$(sql "select value from settings where key = 'site.type'")
want "A-201 관리자 배색 바꾸기 (트리 재조립)" 303 "$(code POST /admin/settings --data-urlencode "site.name=사이트 확장" \
	--data-urlencode "site.type=${TYPE:-cms}" --data-urlencode "admin.theme=1c" \
	--data-urlencode "site.dev_mode=" --data-urlencode "auth.email_verification_required=")"
want "다시 조립한 트리의 사이트 화면" 200 "$(page /admin/site/checkins)"
grep -q 'data-admin-theme="1c"' "$WORK/page" && grep -q 'href="/admin/site/checkins"' "$WORK/page" &&
	ok "다시 조립한 트리에도 사이트 화면과 메뉴가 있다 (새 배색으로)" || bad "다시 조립한 트리에서 사이트 화면이나 메뉴가 빠졌다"

# ---- 문자 인증 로그인 (D42 8절, P-909) ----------------------------------------
# smscode 는 그 번호로 나간 마지막 인증번호다. 문자는 응답 밖에서 나가므로 잠깐 기다린다. 둘째 인자와 같은 번호는
# 앞의 것이라 더 기다린다.
smscode() {
	i=0
	while [ "$i" -lt 50 ]; do
		c=$(grep "\[EXAMPLE_SMS\] to=$1 " "$WORK/log" | tail -1 | sed -n 's/.*인증번호 \([0-9]\{6\}\).*/\1/p')
		if [ -n "$c" ] && [ "$c" != "${2:-}" ]; then echo "$c"; return; fi
		i=$((i + 1))
		sleep 0.1
	done
}
other() { [ "$1" = 000000 ] && echo 111111 || echo 000000; }
verify() { app POST /api/site/auth/verify "$KEY" --data "{\"phone\":\"01011112222\",\"code\":\"$1\"}"; }
want "인부 추가 (사이트 관리자 화면)" 303 "$(code POST /admin/site/workers --data-urlencode "name=김인부" --data-urlencode "phone=010-1111-2222")"
WID=$(sql "select id from site_example_workers where phone = '01011112222'")
want "앱: 인증번호 요청" 202 "$(app POST /api/site/auth/code "$KEY" --data '{"phone":"010-1111-2222"}')"
cp "$WORK/body" "$WORK/known"
CODE1=$(smscode 01011112222)
[ -n "$CODE1" ] && ok "인증번호가 나갔다 (개발용 로그 발송)" || bad "인증번호가 나가지 않았다"
want "앱: 등록되지 않은 번호도 같은 상태" 202 "$(app POST /api/site/auth/code "$KEY" --data '{"phone":"01099998888"}')"
cmp -s "$WORK/known" "$WORK/body" && ok "앱: 등록·미등록 응답 본문이 같다" || bad "앱: 응답 본문이 다르다"
sleep 0.5
grep -q "to=01099998888" "$WORK/log" && bad "등록되지 않은 번호에 문자가 나갔다" || ok "등록되지 않은 번호에는 문자가 없다"
want "앱: 틀린 인증번호 401" 401 "$(verify "$(other "$CODE1")")"
want "앱: 맞는 인증번호로 토큰" 200 "$(verify "$CODE1")"
TOKEN=$(sed -n 's/.*"token":"\([^"]*\)".*/\1/p' "$WORK/body")
want "앱: 토큰으로 내 정보 (site.Person)" 200 "$(app GET /api/site/v1/me "$KEY" -H "X-Ondolith-Session: $TOKEN")"
grep -q '"name":"김인부"' "$WORK/body" && ok "앱: 처리기가 로그인한 사람을 안다" || bad "앱: 내 정보가 아니다 — $(cat "$WORK/body")"
want "앱: 내 출근" 201 "$(app POST /api/site/v1/me/checkins "$KEY" -H "X-Ondolith-Session: $TOKEN" --data '{"kind":"in"}')"
want "그 출근이 그 인부를 가리킨다" 1 "$(sql "select count(*) from site_example_checkins where worker_id = $WID")"
want "앱: 토큰 없이 내 정보 401" 401 "$(app GET /api/site/v1/me "$KEY")"
want "앱: 쓴 인증번호로 다시 401" 401 "$(verify "$CODE1")"
app POST /api/site/auth/code "$KEY" --data '{"phone":"01011112222"}' >/dev/null
CODE2=$(smscode 01011112222 "$CODE1")
for _ in 1 2 3 4 5; do verify "$(other "$CODE2")" >/dev/null; done
want "앱: 다섯 번 틀린 뒤에는 맞는 번호도 401" 401 "$(verify "$CODE2")"
want "작업 로그에 인증번호 원문이 없다" 0 "$(sql "select count(*) from operation_logs where position('$CODE1' in summary) > 0")"
want "인부 그만둠" 303 "$(code POST "/admin/site/workers/$WID/leave")"
want "그만둔 인부의 토큰은 401" 401 "$(app GET /api/site/v1/me "$KEY" -H "X-Ondolith-Session: $TOKEN")"
want "작업 로그에 그만둠이 남는다" 1 "$(sql "select count(*) from operation_logs where action = 'site.worker_leave' and target_id = '$WID'")"
SENT=$(grep -c "to=01011112222 " "$WORK/log")
want "그만둔 인부의 인증번호 요청도 같은 응답" 202 "$(app POST /api/site/auth/code "$KEY" --data '{"phone":"01011112222"}')"
sleep 0.5
want "그만둔 인부에게는 문자가 없다" "$SENT" "$(grep -c "to=01011112222 " "$WORK/log")"
want "번호당 10분 3통 — 넷째는 429" 429 "$(app POST /api/site/auth/code "$KEY" --data '{"phone":"01011112222"}')"

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
