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

# ---- D11 의 라우트 표기를 정규식으로 -----------------------------------------
#
# `/admin/boards/{id}` 같은 표기를 실제 주소와 대조할 수 있는 형태로 바꾼다.
#
# **한 번에 훑는다.** 규칙을 순서대로 적용하면 앞의 규칙이 뒤의 규칙이 찾을
# 글자를 먼저 바꿔 버린다 — 점을 먼저 이스케이프하면 `{path...}` 의 `...` 이
# `[.][.][.]` 이 되어 와일드카드 규칙이 영영 매치되지 않고, 그 자리는 단일
# 세그먼트(`[^/?]+`)로 잘못 변환된다. 그러면 `/static/css/style.css` 처럼
# 슬래시가 든 실제 주소가 매치되지 않아 **멀쩡한 화면이 「감사가 재지 않는
# 화면」으로 오탐된다.** 아래는 네 갈래를 한 패스에서 각자 처리한다.
#
# `{$}` 는 Go 라우터의 「경로가 여기서 끝난다」 표기다 (`/{$}` 는 `/` 하나만).
# 이것을 단일 세그먼트로 보면 `/{$}` 가 `/[^/?]+` 이 되어 **정작 홈 `/` 은
# 놓치고 `/search`·`/cart` 를 매치한다** — 홈이 감사에서 통째로 빠져도 다른
# 주소에 걸려 초록으로 보인다. 자리를 차지하지 않는 표기이므로 빈 문자열이다.
route_re() {
	printf '%s' "$1" | perl -pe '
		s! (\{\$\}) | (\{[^}]*\.\.\.\}) | (\{[^}]*\}) | ([^{]+) !
			defined $1 ? "" :
			defined $2 ? ".*" :
			defined $3 ? "[^/?]+" :
			             ($4 =~ s/([.\[\]^\$()|*+?{}\\])/\\$1/gr)
		!gex'
}

# 이 변환이 틀리면 아래 「빠진 화면」 검사가 통째로 헛돈다 — DB 도 브라우저도
# 필요 없으니 여기서 먼저 확인하고, 틀리면 시작하지 않는다.
route_re_selfcheck() {
	# 표기 → 매치돼야 하는 주소 → 매치되면 안 되는 주소
	printf '%s\n' \
		'/static/{path...}|/static/css/style.css|/statics/x' \
		'/shop/p/{slug}|/shop/p/hoodie|/shop/p/a/b' \
		'/admin/boards/{id}/fields|/admin/boards/9f/fields|/admin/boards/9f' \
		'/{$}|/|/search' \
		'/admin/{$}|/admin/|/admin/users' \
		'/robots.txt|/robots.txt|/robotsatxt' \
		'/admin/|/admin/|/admin/x' |
		while IFS='|' read -r pat yes no; do
			re=$(route_re "$pat")
			# **중괄호를 뺄 수 없다.** `«$pat»` 로 쓰면 셸이 `»` 의 바이트를
			# 변수 이름에 붙여 읽어 `$pat»` 라는 없는 변수가 되고, 진단 문구가
			# 빈칸으로 나온다 — 검사는 물지만 무엇이 틀렸는지 말하지 못한다.
			printf '%s\n' "$yes" | grep -qE "^$re(\?.*)?\$" ||
				bad "route_re: «${pat}» → «${re}» 가 «${yes}» 를 놓친다"
			printf '%s\n' "$no" | grep -qE "^$re(\?.*)?\$" &&
				bad "route_re: «${pat}» → «${re}» 가 «${no}» 까지 먹는다"
		done

	# **사례를 손으로 고르면 손이 모르는 표기를 놓친다.** 위 목록은 내가 고른
	# 것이고, 그래서 `{$}` 가 D11 에 두 번(P-201 홈·A-101 대시보드) 쓰이는데도
	# 빠져 있었다 — 변환기는 그것을 단일 세그먼트로 잘못 읽었고 검사는 엉뚱한
	# 주소에 걸려 초록이었다. 표기 목록을 D11 에서 직접 뽑아 대조한다: 변환기가
	# 모르는 표기가 D11 에 들어오면 여기서 멈춘다.
	perl -ne '
		next unless /^\|\s*[PA]-\d{3}\s*\|\s*[^|]+\|\s*`([^`]+)`/;
		my $p = $1;
		while ($p =~ /(\{[^}]*\})/g) { print "$1\n" }' docs/11-screens.md |
		sort -u |
		while read -r tok; do
			# 아는 표기는 셋뿐이다: `{$}` 경로 끝 · `{name}` 한 세그먼트 ·
			# `{name...}` 여러 세그먼트. **「중괄호면 통과」로 두면 안 된다** —
			# 변환기의 마지막 갈래가 모든 `{...}` 를 한 세그먼트로 삼키므로,
			# 의미가 다른 표기가 들어와도 조용히 잘못 변환된다. `{$}` 가 바로
			# 그렇게 들어와 있었다.
			printf '%s\n' "$tok" |
				grep -qE '^\{(\$|[A-Za-z_][A-Za-z0-9_]*(\.\.\.)?)\}$' ||
				bad "route_re: D11 의 표기 «${tok}» 를 변환기가 모른다"
			# 아는 모양이라도 실제로 자리를 바꾸는지 확인한다 — 표기가 그대로
			# 남으면 정규식의 중괄호가 되어 조용히 다른 것을 매치한다.
			got=$(route_re "/a/$tok/b")
			case "$got" in
			*'{'* | *'}'*)
				bad "route_re: «${tok}» 가 «${got}» 로 남았다 — 변환되지 않았다" ;;
			esac
		done
	return 0
}
if out=$(route_re_selfcheck 2>&1) && [ -z "$out" ]; then
	ok "라우트 표기를 정규식으로 옮기는 규칙 (D11 의 표기를 전부 안다)"
else
	printf '%s\n' "$out"
	printf 'ui: 라우트 정규식 변환이 깨졌다 — 「빠진 화면」 검사가 헛돈다\n'
	exit 1
fi

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
code POST /checkout $(checkout_args) --data-urlencode "receiver_name=받는이" \
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

# **공개 화면은 세 역할이 다 본다.** 홈·상품·게시판·검색을 관리자 세션으로만
# 재면, 방문자가 실제로 보는 화면은 한 번도 재지 않는 것이 된다 — 로그인 링크
# 자리, 관리 버튼이 빠진 자리, 「글쓰기」가 사라진 자리는 관리자 화면에 없다.
# 사이트에 처음 오는 사람이 밟는 경로(홈 → 상품·글)가 바로 여기다.
PUBLIC_URLS=$WORK/urls-public
{
	printf '%s\n' / /shop /board/notice /search /shop/search
	printf '/board/notice/%s\n' "$POST_ID"
	printf '/shop/p/%s\n' "$(sql "select slug from products limit 1")"
	printf '/shop/c/%s\n' "$(sql "select slug from categories limit 1")"
} >"$PUBLIC_URLS"

# 회원·익명만 볼 수 있는 화면이 있다 — 관리자 세션으로 열면 303 이라 재지 못한다.
printf '%s\n' /cart /checkout /orders /me /me/password /me/connections \
	/me/delete /orders/guest >"$MEMBER_URLS"
cat "$PUBLIC_URLS" >>"$MEMBER_URLS"
printf '/orders/%s\n/orders/%s/shipping\n/orders/%s/receipt\n/orders/%s/refunds\n/orders/%s/returns\n/orders/%s/return\n/orders/%s/exchange\n' \
	"$ORDER_NO" "$ORDER_NO" "$ORDER_NO" "$ORDER_NO" "$ORDER_NO" "$ORDER_NO" "$ORDER_NO" >>"$MEMBER_URLS"
printf '%s\n' /login /signup /password/reset >"$ANON_URLS"
cat "$PUBLIC_URLS" >>"$ANON_URLS"

{
	printf '%s\n' /admin/pages /admin/pages/new \
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
	printf '/board/notice/%s/edit\n' "$POST_ID"
	printf '/board/notice/write\n'
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
# `/admin/` 은 끝이 슬래시라 위 목록에서 함께 적기 어렵다 — 따로 넣는다.
printf '/admin/\n' >>"$ADMIN_URLS"
cat "$PUBLIC_URLS" >>"$ADMIN_URLS"

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

# **404 화면도 화면이다.** 위 필터는 200 만 남기므로 오류 화면이 통째로 빠진다 —
# 사용자가 오타 하나로 가장 자주 보는 레이아웃인데(`.error-page`) 아무도 재지
# 않고 있었다. 없는 주소 하나를 넣어 실제로 404 가 오는지 확인한 뒤 더한다.
NOT_FOUND=/ondolith-no-such-page-404
JAR_SAVE=$JAR; JAR=$WORK/jar-anon
st=$(code GET "$NOT_FOUND")
JAR=$JAR_SAVE
if [ "$st" = "404" ]; then
	printf '%s\n' "$NOT_FOUND" >>"$WORK/live-anon"
	ok "404 화면을 감사 대상에 넣었다"
else
	bad "없는 주소가 404 를 주지 않는다 (HTTP $st) — 404 화면을 재지 못한다"
fi

[ -n "${UI_LIST:-}" ] && { echo "── 감사 대상(관리자)"; cat "$WORK/live-admin"; }
L=$(( $(wc -l <"$WORK/live-admin") + $(wc -l <"$WORK/live-member") + $(wc -l <"$WORK/live-anon") ))
[ "$L" -ge 55 ] && ok "열리는 주소 $L 개" || bad "열리는 주소가 $L 개뿐이다 — 감사가 헛돈다"

# ---- 빠진 화면이 없는가 -----------------------------------------------------
#
# **주소를 손으로 적으면 화면이 늘 때 빠진다.** `make screens` 는 D11 의 표에서
# 목록을 뽑는데 여기는 손으로 적었다 — 새 화면이 생기면 이 감사에서 조용히
# 빠지고, 그 사실을 아무도 말해 주지 않는다. 이 세션에서 같은 모양의 결함이
# 다섯 번 나왔다. D11 과 대조한다.
#
# 재지 않는 것이 정상인 화면은 **이유와 함께** 적는다.
ui_skip_reason() {
	case "$1" in
	P-001) echo "설치 전용 — 설치 뒤 닫힌다" ;;
	P-002) echo "와일드카드 리다이렉트 — 화면이 아니다" ;;
	P-105 | P-112) echo "메일의 링크로 들어온다 — 토큰이 있어야 열린다" ;;
	P-107) echo "소셜 제공자가 되돌려 보낸다" ;;
	P-209) echo "댓글 수정 — 글 화면에서 눌러 들어간다 (레이아웃은 comment/form 과 같다)" ;;
	P-211) echo "첨부 다운로드 — 파일을 내려보낸다, 화면이 아니다" ;;
	P-304) echo "조합 조회 — 상품 화면의 조각이라 홀로 그리지 않는다" ;;
	P-407 | P-408 | P-409 | P-410) echo "결제창이 되돌려 보낸다" ;;
	P-502 | P-503) echo "비회원 조회 — 회원 세션으로 이미 잰다" ;;
	P-514) echo "교환 차액 — 차액이 양수인 교환에만 있다" ;;
	P-901 | P-902) echo "XML·텍스트 — 그릴 것이 없다" ;;
	P-904) echo "주소가 없다 — 오류가 났을 때 그려진다" ;;
	P-906) echo "정적 자산" ;;
	P-907) echo "헬스체크 — 본문이 한 줄이다" ;;
	A-102) echo "복구 화면 — 본 트리 밖" ;;
	A-513) echo "QR 라벨은 상품 화면에서 잰다" ;;
	A-601 | A-602) echo "운영 감시 — A-603 과 같은 표를 쓴다" ;;
	*) echo "" ;;
	esac
}

ALL_LIVE=$WORK/all-live
cat "$WORK/live-admin" "$WORK/live-member" "$WORK/live-anon" >"$ALL_LIVE"
missing=0
while IFS="$(printf '\t')" read -r id name path methods; do
	[ -n "$id" ] || continue
	case "$methods" in *GET*) ;; *) continue ;; esac
	# 404 는 **주소로 대조할 수 없다** — 모든 없는 주소가 여기로 온다. 위에서
	# 넣은 탐침이 목록에 남아 있는지로 확인한다. 스킵 사유로 빼지 않는다:
	# 그러면 「재고 있다」가 아니라 「안 재기로 했다」가 되어 버린다.
	if [ "$id" = P-903 ]; then
		grep -qx "$NOT_FOUND" "$ALL_LIVE" && continue
		bad "P-903 404 화면 — 404 탐침이 감사 목록에 없다"
		missing=$((missing + 1))
		continue
	fi
	re=$(route_re "$path")
	grep -qE "^$re(\?.*)?$" "$ALL_LIVE" && continue
	why=$(ui_skip_reason "$id")
	if [ -n "$why" ]; then
		continue
	fi
	missing=$((missing + 1))
	bad "$id $name ($path) — **UI 감사가 재지 않는 화면**"
done <<EOF
$(perl -ne 'print "$1\t$2\t$3\t$4\n"
	if /^\|\s*([PA]-\d{3})\s*\|\s*([^|]+?)\s*\|\s*`([^`]+)`\s*\|\s*([^|]+?)\s*\|/' docs/11-screens.md)
EOF
[ "$missing" -eq 0 ] && ok "D11 의 GET 화면이 전부 감사 대상이다"

# **상한 창이 지나가길 기다린다.** 위의 200 확인만으로 관리자 트리의 분당
# 60건(D15 4.3-2)을 거의 다 쓴다 — 곧바로 브라우저를 돌리면 첫 화면부터 429 를
# 재게 되고, 오류 문구에는 잴 것이 없어 감사가 조용히 통과한다.
printf '  · 요청 상한 창이 지나가길 기다린다 (60초)\n'
sleep 61

# ---- 브라우저 ---------------------------------------------------------------
# **문법부터 본다.** 감사 스크립트가 파싱 실패로 죽으면 결함 0 과 구분되지 않고,
# 그 실패는 브라우저를 띄운 뒤에야 나온다.
node --check scripts/ui-audit.mjs || { echo "  ✗ 감사 스크립트 문법 오류"; exit 1; }
node --check scripts/shots.mjs || { echo "  ✗ 촬영 스크립트 문법 오류"; exit 1; }
tojson() { python3 -c 'import json,sys;print(json.dumps([l.strip() for l in sys.stdin if l.strip()]))' <"$1"; }
audit=0

# **찍기와 재기는 같은 사이트를 봐야 한다.** 여기까지의 시드·로그인·주소
# 목록을 그대로 쓰고 마지막 한 걸음만 가른다 — 두 벌로 두면 「찍은 화면」과
# 「잰 화면」이 서로 다른 상태가 되고, 눈으로 본 것이 증거가 되지 않는다.
run_role() {
	if [ -n "${UI_SHOTS:-}" ]; then
		for scheme in light dark; do
			SHOT_BASE="$BASE" SHOT_ROLE="$1" SHOT_EMAIL="$2" SHOT_PASSWORD="$3" \
				SHOT_DIR="${SHOT_DIR:-shots}" SHOT_SCHEME="$scheme" \
				SHOT_URLS="$(tojson "$4")" node scripts/shots.mjs || audit=1
		done
	else
		# **두 배색을 다 잰다.** CSS 에 다크 값이 한 벌 더 있는데 라이트만
		# 재면 나머지 절반은 아무도 본 적이 없는 화면이다. 값이 다르면
		# 글자 폭도 여백도 달라지고, 넘침은 거기서도 생긴다.
		for scheme in light dark; do
			UI_BASE="$BASE" UI_ROLE="$1" UI_EMAIL="$2" UI_PASSWORD="$3" \
				UI_SCHEME="$scheme" UI_URLS="$(tojson "$4")" \
				node scripts/ui-audit.mjs || audit=1
		done
	fi
}
# **지난 실행의 사진을 남겨 두지 않는다.** 주소가 하나 빠지거나 시드의 UUID 가
# 바뀌면 낡은 파일이 그대로 남고, 그것을 열어 보고 「고쳤다」고 말하게 된다 —
# 이 세션에서 실제로 그럴 뻔했다. 역할 셋이 같은 디렉터리를 쓰므로 부르기
# **전에 한 번만** 지운다. 지우는 것은 PNG 뿐이다: SHOT_DIR 은 부르는 쪽이
# 정하는 값이라 디렉터리를 통째로 지우지 않는다.
if [ -n "${UI_SHOTS:-}" ]; then
	find "${SHOT_DIR:-shots}" -maxdepth 1 -name '*.png' -delete 2>/dev/null
fi
run_role admin admin@example.com "$ADMIN_PW" "$WORK/live-admin"
run_role member member@example.com "$MEMBER_PW" "$WORK/live-member"
run_role anon "" "" "$WORK/live-anon"

echo
if [ -n "${UI_SHOTS:-}" ]; then
	printf 'shots: %d 통과 · %d 실패 · %d 장\n' "$pass" "$fail" \
		"$(find "${SHOT_DIR:-shots}" -name '*.png' | wc -l | tr -d ' ')"
else
	printf 'ui: %d 통과 · %d 실패 (감사 exit %d)\n' "$pass" "$fail" "$audit"
fi
[ "$fail" -eq 0 ] && [ "$audit" -eq 0 ]
