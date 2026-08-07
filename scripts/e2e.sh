#!/bin/sh
# D83 의 케이스 중 HTTP 로 확인 가능한 것을 전부 재실행한다.
#
# **왜 통합 테스트와 별개인가.** `make test-integration` 은 핸들러를 패키지
# 안에서 부른다 — 픽스처가 트리를 조립하고, 설정은 함수로 주입되고, 마이그레이션은
# 테스트가 돌린다. 여기서는 **빌드된 바이너리를 빈 데이터베이스에 붙여** 브라우저가
# 하는 것과 같은 순서로 HTTP 를 친다. 그래서 이 스크립트만 잡을 수 있는 것이 있다:
# 설치 마법사가 만든 관리자에게 역할이 없던 것, `/admin` 이 404 이던 것, 커머스
# 라우트가 조립 시점에 정해지는 것 — 전부 픽스처 밖에서만 보이는 결함이었다.
#
# D83 의 나머지(스타일이 붙었는지, 표가 읽히는지)는 눈으로 봐야 한다. 그 항목은
# 문서에 👁 로 표시돼 있고 여기서 다루지 않는다.
#
# 실행: make e2e
set -u

cd "$(dirname "$0")/.." || exit 1

PORT=${E2E_PORT:-18099}
BASE="http://127.0.0.1:$PORT"
DB=ondolith_e2e
WORK=$(mktemp -d)
BIN=$WORK/ondolith
JAR=$WORK/cookies
ADMIN_PW='e2e-admin-passphrase'

pass=0
fail=0

ok() {
	pass=$((pass + 1))
	printf '  ✓ %s\n' "$*"
}

bad() {
	fail=$((fail + 1))
	printf '  ✗ %s\n' "$*"
}

# want <case> <expected> <actual> — 값 비교. 무엇이 왜 다른지 남긴다.
want() {
	if [ "$2" = "$3" ]; then
		ok "$1"
	else
		bad "$1 — want $2, got $3"
	fi
}

# code <method> <path> [data...] — 상태 코드만 낸다.
code() {
	m=$1
	p=$2
	shift 2
	curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -c "$JAR" -X "$m" "$BASE$p" "$@"
}

body() {
	m=$1
	p=$2
	shift 2
	curl -s -b "$JAR" -c "$JAR" -X "$m" "$BASE$p" "$@"
}

# sql <query> — 데이터베이스를 직접 본다. 응답만으로는 "썼다" 를 확인할 수 없다.
sql() { docker exec "$CID" psql -U ondolith -d "$DB" -tAc "$1" 2>/dev/null | tr -d '\r'; }

cleanup() {
	[ -n "${SRV:-}" ] && kill "$SRV" 2>/dev/null
	[ -n "${CID:-}" ] && docker exec "$CID" psql -U ondolith -d postgres -q \
		-c "DROP DATABASE IF EXISTS $DB" >/dev/null 2>&1
	rm -rf "$WORK"
}
trap cleanup EXIT INT TERM

# ---- 준비 -------------------------------------------------------------------

DSN=$(sh scripts/testdb.sh dsn) || exit 1
PGPORT=$(echo "$DSN" | sed 's|.*:\([0-9]*\)/.*|\1|')
CID=$(docker ps --filter "publish=$PGPORT" -q)
[ -n "$CID" ] || { echo "테스트 DB 컨테이너가 없다 — sh scripts/testdb.sh up" >&2; exit 1; }

# 앞선 실행이 죽다 남긴 연결이 있으면 DROP 이 거부된다. 끊고 지운다 —
# 여기서 실패하면 이후 전부가 「이미 있는 데이터베이스」 위에서 돌아 무의미해진다.
docker exec "$CID" psql -U ondolith -d postgres -q -c \
	"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '$DB'" >/dev/null 2>&1
docker exec "$CID" psql -U ondolith -d postgres -q \
	-c "DROP DATABASE IF EXISTS $DB" -c "CREATE DATABASE $DB OWNER ondolith" || exit 1

go build -o "$BIN" ./cmd/ondolith || exit 1

# **포트를 먼저 비운다.** 앞선 실행이 죽다 남긴 프로세스가 물고 있으면 새
# 인스턴스는 `address already in use` 로 죽고, 남은 옛 프로세스가 옛 설정으로
# 응답한다 — 그러면 결과가 이번 실행의 것이 아니게 된다. 조용한 오답이라
# 실패보다 나쁘다.
if lsof -ti ":$PORT" >/dev/null 2>&1; then
	echo "포트 $PORT 를 쓰는 프로세스를 정리한다"
	lsof -ti ":$PORT" | xargs kill -9 2>/dev/null
	sleep 1
fi

# 설정 파일이 **없는** 상태로 띄운다 — 그것이 T1.1 의 전제다.
# **`exec` 다.** 없으면 `$!` 는 서브셸의 PID 이고, `kill` 이 서브셸만 죽여
# 바이너리는 포트를 쥔 채 남는다 — 재시작이 「address already in use」로 죽고
# 옛 서버가 옛 설정으로 응답한다.
(cd "$WORK" && exec "$BIN" -addr "127.0.0.1:$PORT" -config ./ondolith.json >"$WORK/log" 2>&1) &
SRV=$!

# 뜰 때까지 기다린다. 고정 sleep 은 느린 기계에서 깜빡인다.
i=0
while [ "$i" -lt 100 ]; do
	curl -s -o /dev/null "$BASE/install" && break
	i=$((i + 1))
	sleep 0.1
done
[ "$i" -lt 100 ] || { echo "서버가 뜨지 않았다:"; cat "$WORK/log"; exit 1; }
kill -0 "$SRV" 2>/dev/null ||
	{ echo "서버 프로세스가 죽었다 (포트를 남이 쥐고 있을 수 있다):"; cat "$WORK/log"; exit 1; }

# ---- 1단계 · 설치 -----------------------------------------------------------
echo "1단계 — 설치"

want "T1.1 미설치 상태의 / 는 설치로 보낸다" 303 "$(code GET /)"
want "T1.1 Location" /install \
	"$(curl -s -o /dev/null -w '%{redirect_url}' "$BASE/" | sed "s|$BASE||")"

want "T1.3 설치가 완료된다" 303 "$(code POST /install \
	--data-urlencode "db_host=127.0.0.1" --data-urlencode "db_port=$PGPORT" \
	--data-urlencode "db_name=$DB" --data-urlencode "db_user=ondolith" \
	--data-urlencode "db_password=ondolith" --data-urlencode "db_sslmode=disable" \
	--data-urlencode "site_name=E2E" --data-urlencode "admin_email=admin@example.com" \
	--data-urlencode "admin_password=$ADMIN_PW" \
	--data-urlencode "admin_password_confirm=$ADMIN_PW")"

want "T1.4 재시작 없이 홈이 뜬다" 200 "$(code GET /)"
want "T1.5 설치 뒤 /install 은 닫힌다" 404 "$(code GET /install)"
[ -f "$WORK/ondolith.json" ] && ok "T1.6 설정 파일이 생겼다" || bad "T1.6 설정 파일이 없다"
grep -q "$DB" "$WORK/ondolith.json" &&
	ok "T1.6 database_url 이 들어 있다" || bad "T1.6 database_url 이 비어 있다"

# **관리자에게 역할이 있어야 한다.** 이것이 없어서 새로 설치한 사이트의 유일한
# 관리자가 /admin 에 못 들어갔다. 응답만으로는 보이지 않아 DB 를 직접 본다.
want "T1.3 관리자에게 admin 역할이 붙는다" admin \
	"$(sql "select r.key from users u join user_roles ur on ur.user_id=u.id
	        join roles r on r.id=ur.role_id where u.email='admin@example.com'")"

# ---- 2단계 · 인증 -----------------------------------------------------------
echo "2단계 — 인증"

want "T2.1 관리자 로그인" 303 "$(code POST /login \
	--data-urlencode "email=admin@example.com" --data-urlencode "password=$ADMIN_PW")"
want "T2.1 /admin 은 /admin/ 으로 보낸다" 308 "$(code GET /admin)"
want "T2.1 /admin/ 이 열린다" 200 "$(code GET /admin/)"

# 회원가입 직후 로그인 상태 (M16 이 고친 자리). 별도 쿠키 항아리를 쓴다 —
# 관리자 세션을 덮으면 이후 단계가 익명으로 돈다.
JAR_ADMIN=$JAR
JAR=$WORK/cookies-buyer
want "T2.3 회원가입" 303 "$(code POST /signup \
	--data-urlencode "email=buyer@example.com" --data-urlencode "display_name=구매자" \
	--data-urlencode "password=e2e-buyer-passphrase")"
want "T2.3 가입 직후 로그인 상태다" 200 "$(code GET /me)"
want "T2.6 로그아웃" 303 "$(code POST /logout)"
want "T2.6 로그아웃 뒤 /me 는 밀린다" 303 "$(code GET /me)"
JAR=$JAR_ADMIN

# ---- 3단계 · 테마 자산 ------------------------------------------------------
echo "3단계 — 테마"

CSS=$(body GET / | perl -nle 'print $1 if m{href="(/static/[^"]+\.css[^"]*)"}' | head -1)
[ -n "$CSS" ] && ok "T3.1 홈이 스타일시트를 건다" || bad "T3.1 홈에 스타일시트 링크가 없다"
[ -n "$CSS" ] && want "T3.1 스타일시트가 내려온다" 200 "$(code GET "$CSS")"
want "T3.4 테마 목록" 200 "$(code GET /admin/themes)"

# ---- 4단계 · 콘텐츠 ---------------------------------------------------------
echo "4단계 — 콘텐츠"

want "T4.1 페이지 생성" 303 "$(code POST /admin/pages/new \
	--data-urlencode "slug=about" --data-urlencode "title=회사 소개" \
	--data-urlencode "body=본문")"
PAGE=$(sql "select id from pages where slug='about'")
want "T4.1 페이지 발행" 303 "$(code POST "/admin/pages/$PAGE/publish" \
	--data-urlencode "status=published")"
want "T4.1 발행된 페이지가 공개로 보인다" 200 "$(code GET /about)"

want "T4.2 게시판 생성" 303 "$(code POST /admin/boards/new \
	--data-urlencode "name=공지사항" --data-urlencode "slug=notice" \
	--data-urlencode "per_page=20" --data-urlencode "allow_comments=1" \
	--data-urlencode "preset=공개")"
want "T4.2 게시판이 열린다" 200 "$(code GET /board/notice)"

want "T4.3 글쓰기" 303 "$(code POST /board/notice/write \
	--data-urlencode "title=첫 공지" --data-urlencode "body=본문입니다")"
body GET /board/notice | grep -q "첫 공지" &&
	ok "T4.3 목록에 나타난다" || bad "T4.3 목록에 없다"

body GET "/search?q=공지" | grep -q "첫 공지" &&
	ok "T4.6 검색에 나온다" || bad "T4.6 검색 결과에 없다"

want "T4.7 sitemap.xml" 200 "$(code GET /sitemap.xml)"
want "T4.7 robots.txt" 200 "$(code GET /robots.txt)"
body GET /sitemap.xml | grep -q "/about" &&
	ok "T4.7 sitemap 에 발행된 페이지가 있다" || bad "T4.7 sitemap 에 페이지가 없다"

# ---- 5단계 · 커머스 준비 ----------------------------------------------------
echo "5단계 — 커머스 준비"

want "T5.6 커머스 전에는 /shop 이 없다" 404 "$(code GET /shop)"

want "T5.1 사이트 유형을 커머스로" 303 "$(code POST /admin/settings \
	--data-urlencode "site.name=E2E" --data-urlencode "site.type=shop" \
	--data-urlencode "site.meta_description=" --data-urlencode "site.og_image=" \
	--data-urlencode "site.dev_mode=" --data-urlencode "auth.email_verification_required=")"
want "T5.1 설정이 저장됐다" shop "$(sql "select value from settings where key='site.type'")"
want "T5.1 재시작 전에는 아직 없다 (FR-710 조립 시점 결정)" 404 "$(code GET /shop)"

# 재시작. 커머스 라우트는 조립 시점에 정해지므로 이것이 절차의 일부다.
#
# **포트가 풀릴 때까지 기다린다.** 종료를 보내자마자 새로 띄우면 `address
# already in use` 로 죽고, 그러면 이후 케이스가 전부 「연결 실패」로 실패해
# 진짜 원인이 그 아래 묻힌다.
restart() {
	kill "$SRV" 2>/dev/null
	wait "$SRV" 2>/dev/null
	# **listener 가 실제로 풀릴 때까지 기다린다.** `wait` 는 자식이 끝난 것만
	# 알려주고, 커널이 소켓을 놓는 것은 그 다음이다. HTTP 로 확인하려 하면
	# 아직 살아 있는 소켓이 응답해 루프가 100 번을 다 돌고, 그 뒤 새 프로세스가
	# 「address already in use」로 죽는다 — 그러면 남은 옛 서버가 옛 설정으로
	# 응답하면서 결과가 조용히 이번 실행의 것이 아니게 된다.
	i=0
	while [ "$i" -lt 100 ] && lsof -ti ":$PORT" >/dev/null 2>&1; do
		i=$((i + 1))
		sleep 0.1
	done
	(cd "$WORK" && exec "$BIN" -addr "127.0.0.1:$PORT" -config ./ondolith.json >>"$WORK/log" 2>&1) &
	SRV=$!
	i=0
	while [ "$i" -lt 100 ]; do
		if curl -s -o /dev/null "$BASE/"; then
			# 응답한 것이 **우리 프로세스**인지 본다. 옛 프로세스가 살아 있으면
			# 응답은 오지만 그것은 옛 설정으로 조립된 트리다.
			kill -0 "$SRV" 2>/dev/null && return 0
			bad "재시작한 프로세스가 죽었다 — 옛 서버가 응답하고 있다"
			return 1
		fi
		i=$((i + 1))
		sleep 0.1
	done
	bad "서버 재시작 실패"
	return 1
}
restart || exit 1
code POST /login --data-urlencode "email=admin@example.com" \
	--data-urlencode "password=$ADMIN_PW" >/dev/null

want "T5.1 재시작 뒤 /shop 이 열린다" 200 "$(code GET /shop)"

want "T5.3 상품 생성" 303 "$(code POST /admin/products/new \
	--data-urlencode "name=온돌 매트" --data-urlencode "slug=mat" \
	--data-urlencode "base_price=189000" --data-urlencode "description=따뜻합니다" \
	--data-urlencode "is_visible=on")"
PROD=$(sql "select id from products where slug='mat'")

# **옵션이 조합을 만든다.** 이것이 없으면 담을 것이 없어 아무것도 팔 수 없다.
want "T5.4 옵션 저장" 303 "$(code POST "/admin/products/$PROD/options" \
	--data-urlencode "option_name=두께" --data-urlencode "option_values=5mm, 10mm")"
want "T5.4 조합 2개가 생긴다" 2 "$(sql "select count(*) from product_variants where product_id='$PROD'")"

VAR=$(sql "select id from product_variants where product_id='$PROD' limit 1")
STOCK=$(sql "select stock from product_variants where id='$VAR'")
want "T5.4 새 조합의 재고는 0 이다" 0 "$STOCK"
want "T5.4 재고 증감" 303 "$(code POST "/admin/products/$PROD/variants" \
	--data-urlencode "variant_id=$VAR" --data-urlencode "sku_$VAR=" \
	--data-urlencode "price_delta_$VAR=0" --data-urlencode "delta_$VAR=10" \
	--data-urlencode "version_$VAR=0")"
want "T5.4 재고가 10 이 됐다" 10 "$(sql "select stock from product_variants where id='$VAR'")"

want "T5.6 PG 미설정이면 주문서를 거절한다" 503 "$(code GET /checkout)"

# ---- 6단계 · 구매 흐름 ------------------------------------------------------
echo "6단계 — 구매 흐름"

want "T6.1 상품 상세" 200 "$(code GET /shop/p/mat)"
want "T6.3 장바구니 담기" 303 "$(code POST /cart/items \
	--data-urlencode "variant_id=$VAR" --data-urlencode "quantity=2")"
body GET /cart | grep -q "378,000원" &&
	ok "T6.3 합계가 수량을 따른다" || bad "T6.3 합계가 맞지 않는다"

want "T5.6 PG 설정" 303 "$(code POST /admin/settings/payment \
	--data-urlencode "pg.provider=toss" --data-urlencode "pg.client_key=test_ck" \
	--data-urlencode "pg.secret_key=test_sk")"
want "T6.4 주문서가 열린다" 200 "$(code GET /checkout)"

want "T6.4 주문 생성" 303 "$(code POST /checkout \
	--data-urlencode "receiver_name=받는이" --data-urlencode "receiver_phone=01012345678" \
	--data-urlencode "postcode=06236" --data-urlencode "address1=서울시 강남구" \
	--data-urlencode "address2=101호" --data-urlencode "delivery_memo=" \
	--data-urlencode "orderer_email=admin@example.com" \
	--data-urlencode "orderer_phone=01012345678")"
NO=$(sql "select order_no from orders order by created_at desc limit 1")
[ -n "$NO" ] && ok "T6.4 주문번호가 발급됐다 ($NO)" || bad "T6.4 주문이 만들어지지 않았다"
want "T6.4 금액은 서버가 계산한다" 378000 "$(sql "select total_amount from orders where order_no='$NO'")"

# ---- 7단계 · 주문 관리 ------------------------------------------------------
echo "7단계 — 주문 관리"

want "T7.1 주문 목록" 200 "$(code GET /admin/orders)"
body GET /admin/orders | grep -q "$NO" &&
	ok "T7.1 목록에 주문이 있다" || bad "T7.1 목록에 주문이 없다"
want "T7.1 주문 상세" 200 "$(code GET "/admin/orders/$NO")"

# 결제 승인은 PG 왕복이 필요하다 (D73). 여기서는 그 뒤의 관리 흐름을 본다.
sql "update orders set status='결제완료' where order_no='$NO'" >/dev/null
sql "insert into payments (order_id, kind, status, pg, payment_key, approved_amount, approved_at)
     select id,'주문결제','승인','toss','e2e_key',total_amount,now() from orders where order_no='$NO'" >/dev/null

want "T7.2 허용되지 않는 전이는 거부된다" 422 \
	"$(code POST "/admin/orders/$NO/transition" --data-urlencode "to=배송완료")"
want "T7.2 허용된 전이는 통과한다" 303 \
	"$(code POST "/admin/orders/$NO/transition" --data-urlencode "to=배송준비")"

want "T7.3 송장 입력" 303 "$(code POST "/admin/orders/$NO/shipping" \
	--data-urlencode "carrier=온돌택배" --data-urlencode "tracking_no=1234567890")"
body GET "/orders/$NO/shipping" | grep -q "1234567890" &&
	ok "T7.3 구매자 화면에 송장이 보인다" || bad "T7.3 구매자 화면에 송장이 없다"

ITEM=$(sql "select oi.id from order_items oi join orders o on o.id=oi.order_id
            where o.order_no='$NO' limit 1")
want "T7.4 부분 환불 접수" 303 "$(code POST "/admin/orders/$NO/refund" \
	--data-urlencode "item_id=$ITEM" --data-urlencode "qty_$ITEM=1" \
	--data-urlencode "reason=단순 변심" --data-urlencode "password=$ADMIN_PW")"
# **금액을 폼에서 받지 않는다** — 서버가 주문 시점 스냅샷에서 계산한다 (FR-625).
want "T7.4 금액은 스냅샷에서 계산된다" 189000 \
	"$(sql "select r.amount from refunds r join orders o on o.id=r.order_id
	        where o.order_no='$NO'")"

want "T7.7 비회원 조회 폼" 200 "$(code GET /orders/guest)"

# ---- 8단계 · 운영 -----------------------------------------------------------
echo "8단계 — 운영"

want "T8.1 작업 로그" 200 "$(code GET /admin/oplog)"
[ "$(sql "select count(*) from operation_logs")" -gt 0 ] &&
	ok "T8.1 파괴적 작업이 기록됐다" || bad "T8.1 작업 로그가 비어 있다"
want "T8.2 시스템 정보" 200 "$(code GET /admin/system)"
want "T8.3 헬스체크" 200 "$(code GET /healthz)"
want "T8.4 없는 주소는 404" 404 "$(code GET /nope-does-not-exist)"

# 부팅 자체 점검이 조용해야 한다. 늘 울리는 경고는 아무도 읽지 않고, 안 읽으면
# 진짜 누락이 그 밑에 묻힌다 — 실제로 P-110·P-113 이 그렇게 묻혀 있었다.
#
# **커머스를 켠 뒤의 부팅만 본다.** 첫 부팅은 `cms` 모드라 커머스 화면이
# 등록되지 않는 것이 정상이고, 그 경고까지 세면 이 검사는 늘 실패한다.
WARN=$(sed -n '/운영 모드/,$p' "$WORK/log" | grep -c "라우트 자체 점검")
want "부팅 자체 점검 경고 없음 (커머스 켠 뒤)" 0 "$WARN"

# ---- 결과 -------------------------------------------------------------------
printf '\ne2e: %d 통과 · %d 실패\n' "$pass" "$fail"
if [ "$fail" -ne 0 ]; then
	printf '서버 로그:\n'
	tail -30 "$WORK/log"
	exit 1
fi
