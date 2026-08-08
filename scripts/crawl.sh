#!/bin/sh
# 홈에서 시작해 **링크를 실제로 따라간다.** 죽은 링크가 하나라도 있으면 실패한다.
#
# **왜 e2e·screens 로는 부족한가.** `make e2e` 는 흐름을 따라가고 `make screens`
# 는 D11 의 표를 훑는다. 둘 다 **주소를 스크립트가 적는다** — 그래서 화면이
# 만들어 내는 주소가 틀려도 보이지 않는다. 실제로 장바구니는 상품 링크를
# `/shop/p/{UUID}` 로 그리고 있었고 라우트는 `/shop/p/{slug}` 였다. 두 검사
# 모두 초록이었다. 사람이 클릭하면 404 가 나는 자리다.
#
# 여기서는 **응답에서 주소를 꺼내** 큐에 넣는다. 그래서 이 검사만 잡을 수 있는
# 것이 있다: 화면이 만든 링크가 라우트와 어긋난 것, 어떤 화면에서도 닿을 수 없는
# 화면, 그리고 로그인 상태에 따라 달라지는 링크.
#
# 실행: make crawl
set -u

cd "$(dirname "$0")/.." || exit 1

PORT=${CRAWL_PORT:-18100}
BASE="http://127.0.0.1:$PORT"
DB=ondolith_crawl
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
trap cleanup EXIT INT TERM


. "$(dirname "$0")/lib-seed.sh"

# ---- 크롤 -------------------------------------------------------------------
#
# **BFS 다.** 큐에서 하나 꺼내 받아오고, 거기서 나온 링크를 큐에 넣는다.
# `seen` 은 중복 방문을 막는다 — 없으면 헤더의 홈 링크만으로 무한히 돈다.
crawl() {
	role=$1
	queue=$WORK/q.$role
	seen=$WORK/seen.$role
	: >"$seen"
	printf '/\n' >"$queue"
	n=0
	dead=0

	while :; do
		url=$(head -1 "$queue")
		[ -n "$url" ] || break
		sed -i.bak '1d' "$queue" && rm -f "$queue.bak"
		grep -qxF "$url" "$seen" && continue
		printf '%s\n' "$url" >>"$seen"

		out=$WORK/page
		st=$(curl -s -b "$JAR" -c "$JAR" -o "$out" -w '%{http_code}' "$BASE$url")
		n=$((n + 1))

		# 4xx·5xx 는 전부 실패다. 링크는 **화면이 만든 것**이므로, 그 주소가
		# 없다는 것은 화면이 없는 곳을 가리키고 있다는 뜻이다.
		case "$st" in
		2* | 3*) ;;
		*)
			dead=$((dead + 1))
			bad "$role: $url → HTTP $st (링크가 가리키는 곳이 없다)"
			continue
			;;
		esac

		# 링크를 꺼낸다. 같은 출처의 것만, 조각(#)은 떼고, 정적 자산은 뺀다.
		# **GET 폼의 action 도 주소다.** 검색(P-212·P-305)은 링크가 아니라
		# 폼으로만 들어가므로, href 만 모으면 그 화면은 영영 검사되지 않는다.
		# POST 폼은 따라가지 않는다 — 따라가면 크롤이 자료를 바꾼다.
		{
			perl -nle 'print $1 while /href="([^"]+)"/g' "$out"
			perl -0ne 'while (/<form([^>]*)>/gs) { my $a = $1;
				next if $a =~ /method\s*=\s*"post"/i;
				print "$1\n" if $a =~ /action="([^"]+)"/ }' "$out"
		} |
			sed 's/#.*//' |
			grep '^/' |
			grep -v '^/static/' |
			grep -v '^/uploads/' |
			grep -vE '[?&]page=([3-9]|[0-9]{2,})' |
			sort -u >>"$queue"
	done
	printf '  %s: %d 화면 방문, 죽은 링크 %d\n' "$role" "$n" "$dead"
	[ -n "${CRAWL_LIST:-}" ] && sed 's/^/      /' "$seen"
	[ "$dead" -eq 0 ] && ok "$role 로 도달한 링크가 전부 살아 있다"
	# 헛돌기 방지 — 한 장에서 끝났으면 크롤이 아니다.
	[ "$n" -ge 5 ] || bad "$role: $n 화면밖에 안 돌았다 — 크롤이 헛돌았다"
}

echo "2단계 — 관리자로 크롤"
crawl admin

echo "3단계 — 회원으로 크롤"
JAR=$WORK/jar-member
code POST /signup --data-urlencode "email=member@example.com" \
	--data-urlencode "display_name=회원" --data-urlencode "password=$MEMBER_PW" \
	--data-urlencode "nickname=온돌이" >/dev/null
want "가입할 때 회원 항목이 저장됐다" 온돌이 \
	"$(sql "select custom_fields->>'nickname' from users where email='member@example.com'")"
# 장바구니에 담아 둔다 — 빈 장바구니는 상품 링크를 그리지 않아 그 링크가
# 검사되지 않는다.
code POST /cart/items --data-urlencode "variant_id=$VAR" --data-urlencode "quantity=1" >/dev/null
want "장바구니에 담겼다" 1 "$(sql "select count(*) from cart_items")"

# 댓글이 있어야 P-209·P-210 이 그려진다 — 없으면 그 두 화면은 검사 대상에조차
# 오르지 않는다.
POST_ID=$(sql "select id from posts limit 1")
code POST "/board/notice/$POST_ID/comments" --data-urlencode "body=크롤 댓글" >/dev/null
want "댓글이 달렸다" 1 "$(sql "select count(*) from comments")"

# 주문이 있어야 구매 이후 화면(P-502·505·508·509·511·512·513)이 그려진다.
code POST /checkout --data-urlencode "receiver_name=받는이" \
	--data-urlencode "receiver_phone=01012345678" --data-urlencode "postcode=06236" \
	--data-urlencode "address1=서울시 강남구" --data-urlencode "address2=101호" \
	--data-urlencode "delivery_memo=" --data-urlencode "orderer_email=member@example.com" \
	--data-urlencode "orderer_phone=01012345678" >/dev/null
ORDER_NO=$(sql "select order_no from orders order by created_at desc limit 1")
[ -n "$ORDER_NO" ] && ok "주문이 생겼다 ($ORDER_NO)" || bad "주문이 만들어지지 않았다"
# 배송완료로 옮긴다 — 반품·교환 요청(P-511·512)은 그 상태에서만 그려진다.
sql "update orders set status='배송완료' where order_no='$ORDER_NO'" >/dev/null

# 주문서(P-405)는 장바구니가 비면 그릴 것이 없다. 위 주문이 장바구니를
# 비웠으므로 다시 담는다.
code POST /cart/items --data-urlencode "variant_id=$VAR" --data-urlencode "quantity=1" >/dev/null

# **주문을 하나 더 만든다.** 교환을 걸면 그 주문은 배송완료를 벗어나 반품·교환
# 요청 버튼이 사라지므로(그것이 옳은 동작이다), 두 화면을 함께 검사하려면
# 배송완료로 남는 주문이 따로 있어야 한다.
code POST /checkout --data-urlencode "receiver_name=받는이" \
	--data-urlencode "receiver_phone=01012345678" --data-urlencode "postcode=06236" \
	--data-urlencode "address1=서울시 강남구" --data-urlencode "address2=101호" \
	--data-urlencode "delivery_memo=" --data-urlencode "orderer_email=member@example.com" \
	--data-urlencode "orderer_phone=01012345678" >/dev/null
KEEP_NO=$(sql "select order_no from orders order by created_at desc limit 1")
sql "update orders set status='배송완료' where order_no='$KEEP_NO'" >/dev/null
want "배송완료로 남는 주문이 따로 있다" 2 "$(sql "select count(*) from orders")"
code POST /cart/items --data-urlencode "variant_id=$VAR" --data-urlencode "quantity=1" >/dev/null

# **교환 차액 결제(P-514)까지 만든다.** 차액이 양수인 교환이 차액결제대기가
# 됐을 때만 존재하는 화면이라, 여기까지 만들지 않으면 그 링크는 검사되지
# 않는다 — 방금 그 링크가 없어서 못 가던 화면이다.
VAR2=$(sql "select id from product_variants where product_id='$PROD' and id <> '$VAR' limit 1")
# 재고와 차액을 관리자 화면으로 넣는다 — 바꿔 넣을 조합에 재고가 없으면 교환은
# 거부된다 (FR-618).
JAR_MEMBER0=$JAR
JAR=$WORK/jar
code POST "/admin/products/$PROD/variants" --data-urlencode "variant_id=$VAR2" \
	--data-urlencode "sku_$VAR2=" --data-urlencode "price_delta_$VAR2=5000" \
	--data-urlencode "delta_$VAR2=10" --data-urlencode "version_$VAR2=0" >/dev/null
want "바꿔 넣을 조합에 재고가 생겼다" 10 "$(sql "select stock from product_variants where id='$VAR2'")"
JAR=$JAR_MEMBER0
ITEM=$(sql "select oi.id from order_items oi join orders o on o.id = oi.order_id where o.order_no = '$ORDER_NO' limit 1")
[ -n "$ITEM" ] && ok "주문 품목을 찾았다" || bad "주문 품목이 없다 — 아래 교환 접수가 헛돈다"
want "교환 접수" 303 "$(code POST "/orders/$ORDER_NO/exchange" \
	--data-urlencode "item_id=$ITEM" --data-urlencode "qty_$ITEM=1" \
	--data-urlencode "new_variant_id=$VAR2" --data-urlencode "reason=크롤 교환")"
RET=$(sql "select return_no from returns limit 1")
[ -n "$RET" ] && ok "교환이 접수됐다 ($RET)" || bad "교환이 접수되지 않았다"

# 수거 확인은 관리자가 한다 (A-511) — 여기서 차액이 확정되고 차액결제대기가 된다.
JAR_MEMBER=$JAR
JAR=$WORK/jar
code POST "/admin/orders/$ORDER_NO/returns" --data-urlencode "return_no=$RET" \
	--data-urlencode "action=pickup" --data-urlencode "fault=구매자" >/dev/null
# 수거 다음이 차액 확정이다 (D14 5절: 교환수거 → 차액결제대기, 차액 > 0).
code POST "/admin/orders/$ORDER_NO/returns" --data-urlencode "return_no=$RET" \
	--data-urlencode "action=exchange" >/dev/null
want "교환이 차액결제대기가 됐다" 차액결제대기 \
	"$(sql "select status from returns where return_no='$RET'")"
JAR=$JAR_MEMBER
crawl member

echo "4단계 — 익명으로 크롤"
JAR=$WORK/jar-anon
: >"$JAR"
crawl anon

# ---- 도달 가능성 ------------------------------------------------------------
#
# **링크가 살아 있는 것과 닿을 수 있는 것은 다르다.** 위 단계는 화면이 그린
# 링크를 검사한다 — 아무 화면도 그리지 않은 주소는 검사 대상에조차 오르지
# 않는다. 그래서 D11 의 GET 화면 전부를 놓고 「관리자가 홈부터 눌러서 닿는가」를
# 따로 본다. 닿지 못하는 화면은 만들어 두고 아무도 갈 수 없는 화면이다.
echo "5단계 — 홈에서 눌러서 닿는가"

# 링크로 닿을 수 없는 것이 정상인 화면. **이유를 함께 적는다** — 목록이
# 길어지면 「왜 뺐는지」가 검사의 내용이 되기 때문이다.
unreachable_reason() {
	case "$1" in
	P-001) echo "설치 전용 — 설치 뒤 닫힌다" ;;
	P-002) echo "와일드카드 리다이렉트 — 화면이 아니다" ;;
	P-105 | P-112) echo "메일의 링크로 들어온다" ;;
	P-107) echo "소셜 제공자가 되돌려 보낸다" ;;
	P-304) echo "조합 조회 — 상품 화면이 스크립트로 부른다" ;;
	P-407) echo "주문서를 제출하면 서버가 보낸다" ;;
	P-408 | P-409 | P-410) echo "결제창이 되돌려 보낸다" ;;
	P-901 | P-902) echo "크롤러·검색엔진이 읽는다" ;;
	P-904) echo "주소가 없다 — 오류가 났을 때 그려진다" ;;
	P-906) echo "정적 자산 — 크롤이 일부러 제외한다" ;;
	P-907) echo "운영 감시가 부른다" ;;
	*) echo "" ;;
	esac
}

miss=0
while IFS="$(printf '\t')" read -r id name path methods; do
	[ -n "$id" ] || continue
	case "$methods" in *GET*) ;; *) continue ;; esac
	# `{x}` 는 아무 한 조각이나 받는다. 도달 판정은 **주소 모양**으로 한다 —
	# 특정 글 하나가 아니라 그 화면에 닿았는지가 질문이다.
	re=$(printf '%s' "$path" | sed 's|[.]|[.]|g; s|{[^}]*}|[^/]+|g')
	if grep -qE "^$re$" "$WORK"/seen.admin "$WORK"/seen.member "$WORK"/seen.anon; then
		continue
	fi
	why=$(unreachable_reason "$id")
	if [ -n "$why" ]; then
		ok "$id $name — 링크로 닿지 않는 것이 정상 ($why)"
	else
		miss=$((miss + 1))
		bad "$id $name ($path) — **홈에서 눌러서 닿을 수 없다**"
	fi
done <<EOF
$(perl -ne 'print "$1\t$2\t$3\t$4\n"
	if /^\|\s*(P-\d{3})\s*\|\s*([^|]+?)\s*\|\s*`([^`]+)`\s*\|\s*([^|]+?)\s*\|/' docs/11-screens.md)
EOF

# 관리자 화면은 한 곳만 본다 — 거기에 닿으면 사이드바가 나머지를 잇는다.
grep -qxF '/admin/' "$WORK"/seen.admin &&
	ok "관리자가 홈에서 /admin/ 로 갈 수 있다" ||
	bad "**관리자로 로그인해도 /admin/ 으로 가는 링크가 사이트에 없다**"

echo
printf 'crawl: %d 통과 · %d 실패\n' "$pass" "$fail"
[ "$fail" -eq 0 ] || { printf '\n서버 로그(오류만):\n'; grep -i 'level=ERROR' "$WORK/log" | tail -10; exit 1; }
