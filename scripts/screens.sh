#!/bin/sh
# D11 의 **모든 GET 화면**을 실제로 열어 본다.
#
# `make e2e` 는 흐름을 따라가므로 그 길에 없는 화면은 지나가지 않는다. 여기서는
# 반대로 **표를 기준으로** 훑는다 — D11 에 있는데 아무도 안 열어 본 화면이
# 있으면 그것이 곧 구멍이다. 실제로 P-110·P-113 이 라우트 없이 「완료」로
# 표시돼 있었고, 커머스 관리 화면 7개가 제목만 남은 채 200 을 돌려주고 있었다.
#
# **200 만으로는 부족하다.** 템플릿이 없는 키를 참조하면 html/template 은 값을
# 비우고 계속 그린다 — 응답은 200 이고 화면은 비어 있다. 그래서 본문 크기와
# 오류 표지도 함께 본다.
#
# 실행: make screens
set -u

cd "$(dirname "$0")/.." || exit 1

PORT=${SCREENS_PORT:-18100}
BASE="http://127.0.0.1:$PORT"
DB=ondolith_screens
WORK=$(mktemp -d)
BIN=$WORK/ondolith
JAR=$WORK/cookies
ADMIN_PW='screens-admin-passphrase'

pass=0
fail=0
ok() { pass=$((pass + 1)); [ -n "${VERBOSE:-}" ] && printf '  ✓ %s\n' "$*"; return 0; }
bad() { fail=$((fail + 1)); printf '  ✗ %s\n' "$*"; }

cleanup() {
	[ -n "${SRV:-}" ] && kill "$SRV" 2>/dev/null
	[ -n "${CID:-}" ] && docker exec "$CID" psql -U ondolith -d postgres -q \
		-c "DROP DATABASE IF EXISTS $DB" >/dev/null 2>&1
	rm -rf "$WORK"
}
trap cleanup EXIT INT TERM

DSN=$(sh scripts/testdb.sh dsn) || exit 1
PGPORT=$(echo "$DSN" | sed 's|.*:\([0-9]*\)/.*|\1|')
CID=$(docker ps --filter "publish=$PGPORT" -q)
[ -n "$CID" ] || { echo "테스트 DB 컨테이너가 없다 — sh scripts/testdb.sh up" >&2; exit 1; }

docker exec "$CID" psql -U ondolith -d postgres -q -c \
	"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '$DB'" >/dev/null 2>&1
docker exec "$CID" psql -U ondolith -d postgres -q \
	-c "DROP DATABASE IF EXISTS $DB" -c "CREATE DATABASE $DB OWNER ondolith" || exit 1

go build -o "$BIN" ./cmd/ondolith || exit 1
if lsof -ti ":$PORT" >/dev/null 2>&1; then
	lsof -ti ":$PORT" | xargs kill -9 2>/dev/null
	sleep 1
fi

start() {
	(cd "$WORK" && exec "$BIN" -addr "127.0.0.1:$PORT" -config ./ondolith.json >>"$WORK/log" 2>&1) &
	SRV=$!
	i=0
	while [ "$i" -lt 100 ]; do
		curl -s -o /dev/null "$BASE/" && { kill -0 "$SRV" 2>/dev/null && return 0; }
		i=$((i + 1))
		sleep 0.1
	done
	echo "서버가 뜨지 않았다:"; cat "$WORK/log"; exit 1
}
start

req() { curl -s -b "$JAR" -c "$JAR" "$@"; }

# ---- 사이트를 커머스로 채운다 ------------------------------------------------
#
# **모든 화면이 그릴 것이 있는 상태를 만든다.** 빈 사이트에서 훑으면 「데이터가
# 없어서 비었다」와 「그리지 못해서 비었다」가 구별되지 않는다.

curl -s -o /dev/null -c "$JAR" -X POST "$BASE/install" \
	--data-urlencode "db_host=127.0.0.1" --data-urlencode "db_port=$PGPORT" \
	--data-urlencode "db_name=$DB" --data-urlencode "db_user=ondolith" \
	--data-urlencode "db_password=ondolith" --data-urlencode "db_sslmode=disable" \
	--data-urlencode "site_name=화면 점검" --data-urlencode "admin_email=admin@example.com" \
	--data-urlencode "admin_password=$ADMIN_PW" \
	--data-urlencode "admin_password_confirm=$ADMIN_PW"

login() {
	req -o /dev/null -X POST "$BASE/login" \
		--data-urlencode "email=admin@example.com" --data-urlencode "password=$ADMIN_PW"
}
login

req -o /dev/null -X POST "$BASE/admin/settings" \
	--data-urlencode "site.name=화면 점검" --data-urlencode "site.type=shop" \
	--data-urlencode "site.meta_description=모든 화면을 열어 본다" \
	--data-urlencode "site.og_image=" --data-urlencode "site.dev_mode=" \
	--data-urlencode "auth.email_verification_required="

# 커머스 라우트는 조립 시점에 정해진다 (FR-710).
kill "$SRV" 2>/dev/null; wait "$SRV" 2>/dev/null
i=0; while [ "$i" -lt 100 ] && lsof -ti ":$PORT" >/dev/null 2>&1; do i=$((i+1)); sleep 0.1; done
start
login

sql() { docker exec "$CID" psql -U ondolith -d "$DB" -tAc "$1" 2>/dev/null | tr -d '\r'; }

req -o /dev/null -X POST "$BASE/admin/pages/new" \
	--data-urlencode "slug=about" --data-urlencode "title=회사 소개" --data-urlencode "body=본문"
PAGE=$(sql "select id from pages where slug='about'")
req -o /dev/null -X POST "$BASE/admin/pages/$PAGE/publish" --data-urlencode "status=published"

req -o /dev/null -X POST "$BASE/admin/boards/new" \
	--data-urlencode "name=공지" --data-urlencode "slug=notice" \
	--data-urlencode "per_page=20" --data-urlencode "allow_comments=1" \
	--data-urlencode "preset=공개"
req -o /dev/null -X POST "$BASE/board/notice/write" \
	--data-urlencode "title=첫 글" --data-urlencode "body=본문입니다"
POST=$(sql "select id from posts limit 1")
req -o /dev/null -X POST "$BASE/board/notice/$POST/comments" --data-urlencode "body=댓글"
COMMENT=$(sql "select id from comments limit 1")

# **A-509 는 이동 전용 화면이다** — 생성 폼이 없다. 카테고리를 만드는 화면이
# D11 에 없는 것이 맞는지는 별개 문제이고(A-509 미결), 여기서는 P-302 를 그릴
# 데이터가 필요하므로 직접 넣는다.
sql "insert into categories (name, slug) values ('매트','matcat')" >/dev/null
req -o /dev/null -X POST "$BASE/admin/products/new" \
	--data-urlencode "name=온돌 매트" --data-urlencode "slug=mat" \
	--data-urlencode "base_price=189000" --data-urlencode "description=따뜻합니다" \
	--data-urlencode "is_visible=on"
PROD=$(sql "select id from products where slug='mat'")
req -o /dev/null -X POST "$BASE/admin/products/$PROD/options" \
	--data-urlencode "option_name=두께" --data-urlencode "option_values=5mm, 10mm"
VAR=$(sql "select id from product_variants where product_id='$PROD' limit 1")
req -o /dev/null -X POST "$BASE/admin/products/$PROD/variants" \
	--data-urlencode "variant_id=$VAR" --data-urlencode "sku_$VAR=" \
	--data-urlencode "price_delta_$VAR=0" --data-urlencode "delta_$VAR=10" \
	--data-urlencode "version_$VAR=0"

req -o /dev/null -X POST "$BASE/admin/settings/payment" \
	--data-urlencode "pg.provider=toss" --data-urlencode "pg.client_key=test_ck" \
	--data-urlencode "pg.secret_key=test_sk"
req -o /dev/null -X POST "$BASE/cart/items" \
	--data-urlencode "variant_id=$VAR" --data-urlencode "quantity=2"
req -o /dev/null -X POST "$BASE/checkout" \
	--data-urlencode "receiver_name=받는이" --data-urlencode "receiver_phone=01012345678" \
	--data-urlencode "postcode=06236" --data-urlencode "address1=서울시" \
	--data-urlencode "address2=101호" --data-urlencode "delivery_memo=" \
	--data-urlencode "orderer_email=admin@example.com" --data-urlencode "orderer_phone=01012345678"
ORDER=$(sql "select order_no from orders limit 1")
sql "update orders set status='결제완료' where order_no='$ORDER'" >/dev/null
sql "insert into payments (order_id, kind, status, pg, payment_key, approved_amount, approved_at)
     select id,'주문결제','승인','toss','k',total_amount,now() from orders where order_no='$ORDER'" >/dev/null
req -o /dev/null -X POST "$BASE/admin/orders/$ORDER/transition" --data-urlencode "to=배송준비"

USER=$(sql "select id from users limit 1")
ROLE=$(sql "select id from roles where key='operator'")
BOARD=$(sql "select id from boards limit 1")

# ---- 훑기 -------------------------------------------------------------------
#
# D11 의 표에서 GET 을 받는 화면을 뽑아 경로 변수만 실제 값으로 채운다. 표를
# 손으로 베끼지 않는 것이 요점이다 — 베끼면 D11 에 화면이 늘어도 여기는 그대로다.

subst() {
	echo "$1" |
		sed "s|{slug}|about|; s|{id}|PLACEHOLDER|; s|{no}|$ORDER|; s|{token}|nosuchtoken|; \
		     s|{provider}|google|; s|{orderNo}|$ORDER|; s|{returnNo}|R1|; s|{path\.\.\.}|css/style.css|; \
		     s|{\$}||"
}

# 화면마다 다른 id 를 쓴다. 하나로 뭉뚱그리면 「없는 id 라 404」가 정상인지
# 아닌지 구별되지 않는다.
id_for() {
	case "$1" in
	P-204 | P-206 | P-207 | P-208) echo "$POST" ;;
	P-209 | P-210) echo "$COMMENT" ;;
	A-302 | A-303) echo "$PAGE" ;;
	A-305 | A-306) echo "$BOARD" ;;
	A-402 | A-405) echo "$USER" ;;
	A-404) echo "$ROLE" ;;
	A-502 | A-503 | A-513) echo "$PROD" ;;
	*) echo "$PAGE" ;;
	esac
}

# 게시판 경로는 slug 가 다르다.
path_for() {
	case "$1" in
	P-203 | P-204 | P-205 | P-206 | P-207 | P-208) echo "$2" | sed "s|{slug}|notice|" ;;
	P-303 | P-304) echo "$2" | sed "s|{slug}|mat|" ;;
	P-302) echo "$2" | sed "s|{slug}|matcat|" ;;
	*) echo "$2" ;;
	esac
}

# **장바구니를 다시 채운다.** 위에서 주문을 만들며 비웠다 — 빈 장바구니에서
# 주문서(P-405)와 결제창(P-407)은 303 으로 돌려보내는 것이 정상이고, 그러면
# 이 스윕은 그 두 화면을 한 번도 그려 보지 못한다.
req -o /dev/null -X POST "$BASE/cart/items" \
	--data-urlencode "variant_id=$VAR" --data-urlencode "quantity=1"
# 결제창(P-407)은 **결제대기** 주문이 있어야 열린다. 위 주문은 결제완료로
# 옮겼으므로 하나 더 만든다 — 없으면 「완료 화면으로 보냄」이 정상 동작인데
# 그것을 결함으로 세게 된다.
req -o /dev/null -X POST "$BASE/checkout" \
	--data-urlencode "receiver_name=받는이" --data-urlencode "receiver_phone=01012345678" \
	--data-urlencode "postcode=06236" --data-urlencode "address1=서울시" \
	--data-urlencode "address2=101호" --data-urlencode "delivery_memo=" \
	--data-urlencode "orderer_email=admin@example.com" --data-urlencode "orderer_phone=01012345678"
# 주문서(P-405)는 **장바구니가 차 있어야** 열린다. 위에서 주문을 만들며 다시
# 비웠으므로 한 번 더 담는다 — 두 화면이 서로 반대 상태를 요구한다.
req -o /dev/null -X POST "$BASE/cart/items" \
	--data-urlencode "variant_id=$VAR" --data-urlencode "quantity=1"

echo "D11 화면 훑기 ($BASE)"

while IFS="$(printf '\t')" read -r id name path methods; do
	case "$methods" in *GET*) ;; *) continue ;; esac
	# 다른 문으로 서비스되는 것들 (internal/app/screens.go servedOutsideTree).
	case "$id" in P-001 | P-002 | P-903 | P-904 | A-102 | P-905) continue ;; esac

	# **자원이 있어야만 열리는 화면.** 이 스윕이 만들 수 없는 상태를 요구한다 —
	# 유효한 일회용 토큰(P-105·P-112), 프로바이더가 돌려준 인가 코드(P-107),
	# 접수된 교환 건(P-514), 업로드된 첨부(P-211). 만들 수 있는 것을 안 만들고
	# 넘기면 검사가 헐거워지므로, **왜 못 만드는지**를 여기 적는다.
	case "$id" in
	P-105 | P-107 | P-112 | P-211 | P-514)
		continue
		;;
	esac
	case "$path" in *'*'*) continue ;; esac

	p=$(path_for "$id" "$path")
	p=$(echo "$p" | sed "s|{id}|$(id_for "$id")|")
	p=$(subst "$p")

	# **일부 화면은 다른 상태에서 열어야 한다.**
	#
	# 로그인·가입은 이미 로그인한 사람을 돌려보내는 것이 정상이고(P-101 GET),
	# 첨부·인증 토큰·교환 차액은 그 자원이 있어야 열린다. 세션을 바꾸거나
	# 자원을 만들지 않고 「404 니까 실패」로 세면, 정상 동작을 결함으로 보고하는
	# 검사가 되어 곧 무시된다.
	case "$id" in
	P-101 | P-103 | P-104 | P-105)
		# 익명으로 연다 — 로그인한 사람에게는 보일 이유가 없는 화면이다.
		out=$(curl -s -w '\n%{http_code}' "$BASE$p")
		;;
	*)
		out=$(req -w '\n%{http_code}' "$BASE$p")
		;;
	esac
	code=$(printf '%s' "$out" | tail -1)
	body=$(printf '%s' "$out" | sed '$d')
	size=$(printf '%s' "$body" | wc -c | tr -d ' ')

	case "$code" in
	200) ;;
	*) bad "$id $name — HTTP $code ($p)"; continue ;;
	esac

	# HTML 이 아닌 응답은 크기로 판단하지 않는다 — sitemap·robots·healthz 는
	# 짧은 것이 정상이고, 레이아웃이라는 것 자체가 없다.
	case "$id" in P-901 | P-902 | P-907 | P-906 | P-304)
		ok "$id $name"
		continue
		;;
	esac

	# **템플릿이 없는 키를 참조하면 그 자리만 비고 200 이 나간다.** 본문이
	# 레이아웃만 남은 크기면 화면이 아무것도 그리지 못한 것이다.
	if [ "$size" -lt 900 ]; then
		bad "$id $name — 본문 ${size}바이트, 레이아웃뿐이다 ($p)"
		continue
	fi
	case "$body" in
	*'<no value>'* | *'&lt;no value&gt;'*)
		bad "$id $name — 템플릿이 없는 값을 그렸다 ($p)"
		continue
		;;
	esac
	ok "$id $name"
done <<EOF
$(perl -ne 'print "$1\t$2\t$3\t$4\n"
	if /^\|\s*([PA]-\d{3})\s*\|\s*([^|]+?)\s*\|\s*`([^`]+)`\s*\|\s*([^|]+?)\s*\|/' docs/11-screens.md)
EOF

printf '\nscreens: %d 통과 · %d 실패\n' "$pass" "$fail"
[ "$fail" -eq 0 ] || { printf '\n서버 로그:\n'; tail -20 "$WORK/log"; exit 1; }
