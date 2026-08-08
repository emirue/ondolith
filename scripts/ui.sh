#!/bin/sh
# 모든 화면을 여러 폭에서 브라우저에 그려 놓고 **레이아웃을 잰다.**
#
# `make screens` 는 응답과 본문 길이를, `make crawl` 은 링크를 본다. 둘 다 200 을
# 받으면 통과하므로 **글자가 상자 밖으로 나가든 표가 화면을 넘든 초록**이다.
# 여기서만 잡을 수 있는 것: 가로 넘침, 부모 밖으로 나간 요소, 겹친 테두리,
# 잘린 글, 스타일이 붙지 않은 폼 요소, 좁은 화면에서 누를 수 없는 크기.
#
# 시드는 `make crawl` 과 **같은 것**을 쓴다 (lib-seed.sh) — 빈 화면은 결함을
# 숨기고, 시드가 두 벌이면 두 검사가 서로 다른 사이트를 본다.
#
# 실행: make ui
set -u

cd "$(dirname "$0")/.." || exit 1

PORT=${UI_PORT:-8101}
BASE="http://127.0.0.1:$PORT"
DB=${UI_DB:-ondolith_ui}
WORK=$(mktemp -d)
BIN=$WORK/ondolith
ADMIN_PW='crawl-admin-passphrase'
MEMBER_PW='crawl-member-passphrase'

pass=0
fail=0
ok() { pass=$((pass + 1)); printf '  ✓ %s\n' "$*"; }
bad() { fail=$((fail + 1)); printf '  ✗ %s\n' "$*"; }
want() { [ "$2" = "$3" ] && ok "$1" || bad "$1 — want $2, got $3"; }

JAR=$WORK/jar
code() { m=$1; p=$2; shift 2; curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -c "$JAR" -X "$m" "$BASE$p" "$@"; }
sql() { docker exec "$CID" psql -U ondolith -d "$DB" -tAc "$1" 2>/dev/null | tr -d '\r'; }

cleanup() {
	[ -n "${SRV:-}" ] && kill "$SRV" 2>/dev/null
	[ -n "${CID:-}" ] && docker exec "$CID" psql -U ondolith -d postgres -q \
		-c "DROP DATABASE IF EXISTS $DB" >/dev/null 2>&1
	rm -rf "$WORK"
}
[ -n "${UI_KEEP:-}" ] || trap cleanup EXIT INT TERM

. "$(dirname "$0")/lib-seed.sh"

# ---- 회원 자료 --------------------------------------------------------------
#
# 구매 이후 화면은 주문이 있어야 그려진다. 크롤과 같은 순서를 밟는다.
JAR_ADMIN=$JAR
JAR=$WORK/jar-member
code POST /signup --data-urlencode "email=member@example.com" \
	--data-urlencode "display_name=회원" --data-urlencode "password=$MEMBER_PW" \
	--data-urlencode "nickname=온돌이" >/dev/null
code POST /cart/items --data-urlencode "variant_id=$VAR" --data-urlencode "quantity=2" >/dev/null
POST_ID=$(sql "select id from posts limit 1")
code POST "/board/notice/$POST_ID/comments" --data-urlencode "body=댓글 예시입니다." >/dev/null
code POST /checkout --data-urlencode "receiver_name=받는이" \
	--data-urlencode "receiver_phone=01012345678" --data-urlencode "postcode=06236" \
	--data-urlencode "address1=서울특별시 강남구 테헤란로 123" \
	--data-urlencode "address2=온돌빌딩 4층 401호" --data-urlencode "delivery_memo=부재 시 경비실" \
	--data-urlencode "orderer_email=member@example.com" \
	--data-urlencode "orderer_phone=01012345678" >/dev/null
ORDER_NO=$(sql "select order_no from orders order by created_at desc limit 1")
sql "update orders set status='배송완료' where order_no='$ORDER_NO'" >/dev/null
code POST /cart/items --data-urlencode "variant_id=$VAR" --data-urlencode "quantity=1" >/dev/null
JAR=$JAR_ADMIN

want "주문이 생겼다" 1 "$(sql "select count(*) from orders")"

# **빈 표는 결함을 숨긴다.** 행이 없으면 열이 좁아져 넘치지 않고, 그 화면의
# 레이아웃은 검사되지 않은 채 통과한다 — 실제로 `/admin/webhooks` 의 감싸미를
# 걷어내는 변이가 물지 않았고, 그 이유가 이것이었다.
ORDER_ID=$(sql "select id from orders limit 1")
sql "insert into webhook_events (pg, event_id, order_id, status, payload)
     values ('toss','evt_ui_1','$ORDER_ID','수신','{\"orderId\":\"x\",\"amount\":12000}'),
            ('toss','evt_ui_2','$ORDER_ID','처리완료','{\"orderId\":\"y\",\"amount\":9000}')" >/dev/null
want "웹훅 이력이 생겼다" 2 "$(sql "select count(*) from webhook_events")"

sql "insert into payments (order_id, kind, status, pg, payment_key, approved_amount, approved_at)
     select id,'주문결제','승인','toss','ui_key',total_amount,now() from orders" >/dev/null
want "결제 기록이 생겼다" 1 "$(sql "select count(*) from payments")"

# **환불 요청이 있어야 A-507 의 표를 잰다.** 리뷰어가 지적한 자리다 — 같은
# 커밋에서 스크롤 감싸미를 새로 붙여 놓고 정작 빈 표로만 그렸다.
# 화면을 지나서 만든다: 구매자가 P-506 으로 취소를 넣는다.
JAR_ADMIN2=$JAR
JAR=$WORK/jar-member
ITEM_ID=$(sql "select oi.id from order_items oi join orders o on o.id=oi.order_id
               where o.order_no='$ORDER_NO' limit 1")
sql "update orders set status='결제완료' where order_no='$ORDER_NO'" >/dev/null
code POST "/orders/$ORDER_NO/cancel" --data-urlencode "item_id=$ITEM_ID" \
	--data-urlencode "qty_$ITEM_ID=1" --data-urlencode "reason=단순 변심" >/dev/null
sql "update orders set status='배송완료' where order_no='$ORDER_NO'" >/dev/null
JAR=$JAR_ADMIN2
want "환불 요청이 생겼다" 1 "$(sql "select count(*) from refunds")"

# 첨부 관리(A-309)·댓글 관리(A-308)·글 관리(A-307) 는 이미 만든 것이 보인다 —
# 첨부는 lib-seed 의 글쓰기가, 댓글은 위에서, 글은 그 글이 채운다. 관리 화면이
# 그것을 실제로 집어내는지는 `make ui` 가 빈 표로 알려준다.
want "첨부가 있다" 1 "$(sql "select count(*) from attachments")"
want "댓글이 있다" 1 "$(sql "select count(*) from comments")"
want "댓글이 공지 게시판의 글에 달렸다" 1 "$(sql "select count(*) from comments c join posts p on p.id=c.post_id join boards b on b.id=p.board_id where b.slug='notice'")"

# ---- 볼 주소 ----------------------------------------------------------------
#
# **크롤이 닿은 곳을 그대로 본다.** 목록을 손으로 적으면 화면이 늘 때마다
# 빠지고, 빠진 화면은 아무도 재지 않는다.
ADMIN_URLS=$WORK/urls-admin
MEMBER_URLS=$WORK/urls-member
ANON_URLS=$WORK/urls-anon

# 회원·익명만 볼 수 있는 화면이 있다 — 관리자 세션으로 열면 303 이라 재지 못한다.
printf '%s\n' /cart /checkout /orders /me /me/password /me/connections \
	/me/delete /orders/guest >"$MEMBER_URLS"
printf '/orders/%s\n/orders/%s/shipping\n/orders/%s/receipt\n/orders/%s/refunds\n/orders/%s/returns\n/orders/%s/return\n/orders/%s/exchange\n' \
	"$ORDER_NO" "$ORDER_NO" "$ORDER_NO" "$ORDER_NO" "$ORDER_NO" "$ORDER_NO" "$ORDER_NO" >>"$MEMBER_URLS"
printf '%s\n' /login /signup /password/reset >"$ANON_URLS"

{
	printf '%s\n' / /shop /board/notice /search /shop/search \
		/admin/ /admin/pages /admin/pages/new \
		/admin/boards /admin/boards/new \
		/admin/menus /admin/users /admin/users/new \
		/admin/user-fields /admin/roles /admin/settings /admin/settings/mail \
		/admin/settings/payment /admin/settings/business /admin/settings/social \
		/admin/themes /admin/themes/upload /admin/oplog /admin/system \
		/admin/webhooks /admin/products /admin/products/new /admin/categories \
		/admin/orders /admin/terms /admin/commerce/policy /admin/reconcile \
		/admin/scan/lookup /admin/scan/receive /admin/scan/stocktake
	# **A-307·A-308·A-309 는 게시판을 골라야 목록이 나온다.** 고르지 않은
	# 화면은 「게시판 주소를 입력하세요」 한 줄이라 잴 것이 없다 — 두 상태를
	# 모두 본다.
	printf '%s\n' /admin/posts /admin/comments /admin/attachments
	printf '/admin/posts?board=notice\n/admin/comments?board=notice\n/admin/attachments?board=notice\n'
	printf '/board/notice/%s\n' "$POST_ID"
	printf '/board/notice/%s/edit\n' "$POST_ID"
	printf '/board/notice/write\n'
	printf '/shop/p/%s\n' "$(sql "select slug from products limit 1")"
	printf '/shop/c/%s\n' "$(sql "select slug from categories limit 1")"
	printf '/admin/orders/%s\n' "$ORDER_NO"
	printf '/admin/orders/%s/shipping\n' "$ORDER_NO"
	printf '/admin/orders/%s/refund\n' "$ORDER_NO"
	printf '/admin/orders/%s/returns\n' "$ORDER_NO"
	printf '/admin/orders/%s/pick\n' "$ORDER_NO"
	printf '/admin/products/%s\n' "$PROD"
	printf '/admin/products/%s/variants\n' "$PROD"
	printf '/admin/products/%s/labels\n' "$PROD"
	printf '/admin/boards/%s\n' "$(sql "select id from boards limit 1")"
	printf '/admin/boards/%s/fields\n' "$(sql "select id from boards limit 1")"
	printf '/admin/pages/%s\n' "$(sql "select id from pages limit 1")"
	printf '/admin/users/%s\n' "$(sql "select id from users where email='member@example.com'")"
} >"$ADMIN_URLS"
# `/` 와 `/admin/` 은 끝이 슬래시라 위 목록에서 함께 적기 어렵다 — 따로 넣는다.
printf '/\n/admin/\n' >>"$ADMIN_URLS"

N=$(( $(wc -l <"$ADMIN_URLS") + $(wc -l <"$MEMBER_URLS") + $(wc -l <"$ANON_URLS") ))
[ "$N" -ge 60 ] && ok "볼 주소 $N 개" || bad "볼 주소가 $N 개뿐이다 — 감사가 헛돈다"

# 실제로 200 인 것만 넘긴다 — 404·303 을 재면 그 화면이 아니라 오류 화면을 잰다.
# **역할마다 자기 쿠키로 확인한다.** 관리자 세션으로 `/me` 를 열면 303 이고,
# 그것을 「없는 화면」으로 세면 회원 화면 전부가 감사에서 빠진다.
live() {
	jar=$1; src=$2; dst=$3
	JAR_SAVE=$JAR; JAR=$jar
	: >"$dst"
	while read -r u; do
		[ -n "$u" ] || continue
		st=$(code GET "$u")
		[ "$st" = "200" ] && printf '%s\n' "$u" >>"$dst" ||
			printf '  · 건너뜀 %s (HTTP %s)\n' "$u" "$st"
	done <"$src"
	JAR=$JAR_SAVE
}
live "$WORK/jar" "$ADMIN_URLS" "$WORK/live-admin"
live "$WORK/jar-member" "$MEMBER_URLS" "$WORK/live-member"
live "$WORK/jar-anon" "$ANON_URLS" "$WORK/live-anon"

[ -n "${UI_LIST:-}" ] && { echo "── 감사 대상(관리자)"; cat "$WORK/live-admin"; }
L=$(( $(wc -l <"$WORK/live-admin") + $(wc -l <"$WORK/live-member") + $(wc -l <"$WORK/live-anon") ))
[ "$L" -ge 55 ] && ok "열리는 주소 $L 개" || bad "열리는 주소가 $L 개뿐이다 — 감사가 헛돈다"

# **상한 창이 지나가길 기다린다.** 위의 200 확인만으로 관리자 트리의 분당
# 60건(D15 4.3-2)을 거의 다 쓴다 — 곧바로 브라우저를 돌리면 첫 화면부터 429 를
# 재게 되고, 오류 문구에는 잴 것이 없어 감사가 조용히 통과한다.
printf '  · 요청 상한 창이 지나가길 기다린다 (60초)\n'
sleep 61

# ---- 브라우저 ---------------------------------------------------------------
# **문법부터 본다.** 감사 스크립트가 파싱 실패로 죽으면 결함 0 과 구분되지 않고,
# 그 실패는 브라우저를 띄운 뒤에야 나온다.
node --check scripts/ui-audit.mjs || { echo "  ✗ 감사 스크립트 문법 오류"; exit 1; }
tojson() { python3 -c 'import json,sys;print(json.dumps([l.strip() for l in sys.stdin if l.strip()]))' <"$1"; }
audit=0
run_audit() {
	UI_BASE="$BASE" UI_ROLE="$1" UI_EMAIL="$2" UI_PASSWORD="$3" \
		UI_URLS="$(tojson "$4")" node scripts/ui-audit.mjs || audit=1
}
run_audit 관리자 admin@example.com "$ADMIN_PW" "$WORK/live-admin"
run_audit 회원 member@example.com "$MEMBER_PW" "$WORK/live-member"
run_audit 익명 "" "" "$WORK/live-anon"

echo
printf 'ui: %d 통과 · %d 실패 (감사 exit %d)\n' "$pass" "$fail" "$audit"
[ "$fail" -eq 0 ] && [ "$audit" -eq 0 ]
