# 공용 시드 — `make crawl` 과 `make ui` 가 함께 쓴다.
#
# **두 검사가 같은 사이트를 요구한다.** 빈 화면은 결함을 숨긴다: 목록이 비면
# 표의 정렬도 줄 간격도 보이지 않고, 조합이 없는 상품은 구매 폼을 그리지 않으며,
# 링크가 없으면 크롤이 홈 한 장에서 끝난다. 그 시드를 두 벌로 두면 한쪽만
# 늘어나고, 그때부터 두 검사가 서로 다른 사이트를 본다.
#
# 부르는 쪽이 미리 정해야 하는 것:
#   PORT DB WORK BIN ADMIN_PW JAR   그리고 헬퍼 code() want() ok() bad() sql()
# 부르고 나면 쓸 수 있는 것:
#   CID PGPORT SRV PROD VAR  그리고 함수 start()

# ---- 준비 -------------------------------------------------------------------

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
		curl -s -o /dev/null "$BASE/" && return 0
		i=$((i + 1))
		sleep 0.1
	done
	echo "서버가 뜨지 않았다:"; cat "$WORK/log"; return 1
}
start || exit 1

echo "1단계 — 설치와 자료 넣기"

want "설치" 303 "$(code POST /install \
	--data-urlencode "db_host=127.0.0.1" --data-urlencode "db_port=$PGPORT" \
	--data-urlencode "db_name=$DB" --data-urlencode "db_user=ondolith" \
	--data-urlencode "db_password=ondolith" --data-urlencode "db_sslmode=disable" \
	--data-urlencode "site_name=크롤" --data-urlencode "admin_email=admin@example.com" \
	--data-urlencode "admin_password=$ADMIN_PW" \
	--data-urlencode "admin_password_confirm=$ADMIN_PW")"

code POST /login --data-urlencode "email=admin@example.com" \
	--data-urlencode "password=$ADMIN_PW" >/dev/null

# 커머스를 켜고 재시작한다 — 커머스 라우트는 조립 시점에 정해진다 (FR-710).
code POST /admin/settings --data-urlencode "site.name=크롤" \
	--data-urlencode "site.type=shop" --data-urlencode "site.meta_description=" \
	--data-urlencode "site.og_image=" --data-urlencode "site.dev_mode=" \
	--data-urlencode "auth.email_verification_required=" >/dev/null
kill "$SRV" 2>/dev/null; wait "$SRV" 2>/dev/null
i=0; while [ "$i" -lt 100 ] && lsof -ti ":$PORT" >/dev/null 2>&1; do i=$((i + 1)); sleep 0.1; done
start || exit 1
code POST /login --data-urlencode "email=admin@example.com" \
	--data-urlencode "password=$ADMIN_PW" >/dev/null

# 자료가 없으면 크롤이 홈 한 장에서 끝난다 — 링크가 생기는 만큼만 검사가 된다.
code POST /admin/pages/new --data-urlencode "slug=about" \
	--data-urlencode "title=회사 소개" --data-urlencode "body=본문" >/dev/null
PAGE=$(sql "select id from pages where slug='about'")
code POST "/admin/pages/$PAGE/publish" --data-urlencode "status=published" >/dev/null

code POST /admin/boards/new --data-urlencode "name=공지사항" \
	--data-urlencode "slug=notice" --data-urlencode "per_page=20" \
	--data-urlencode "allow_comments=1" --data-urlencode "allow_attachments=1" \
	--data-urlencode "preset=공개" >/dev/null
# **첨부를 붙여 쓴다** (FR-506). 첨부가 없으면 다운로드 화면(P-211)은 그려질
# 일이 없어 검사 대상에조차 오르지 않는다.
printf 'GIF89a crawl' >"$WORK/up.gif"
want "첨부와 함께 글쓰기" 303 "$(code POST /board/notice/write -F "title=첫 공지" -F "body=본문입니다" -F "attachments=@$WORK/up.gif;type=image/gif")"
want "첨부가 저장됐다" 1 "$(sql "select count(*) from attachments")"

code POST /admin/menus --data-urlencode "title=회사 소개" --data-urlencode "url=/about" >/dev/null

# 회원 항목 하나 (FR-215). 정의가 없으면 가입·내 정보의 그 절이 통째로 안
# 그려져 검사 대상에 오르지 않는다.
code POST /admin/user-fields --data-urlencode "key=nickname" \
	--data-urlencode "label=별명" --data-urlencode "field_type=text" \
	--data-urlencode "sort_order=0" >/dev/null
want "회원 항목이 정의됐다" 1 "$(sql "select count(*) from user_fields")"

code POST /admin/categories/new --data-urlencode "name=매트" \
	--data-urlencode "slug=mat" >/dev/null
code POST /admin/products/new --data-urlencode "name=온돌 매트" \
	--data-urlencode "slug=mat" --data-urlencode "base_price=189000" \
	--data-urlencode "description=따뜻합니다" --data-urlencode "is_visible=on" >/dev/null
PROD=$(sql "select id from products where slug='mat'")
code POST "/admin/products/$PROD/options" --data-urlencode "option_name=두께" \
	--data-urlencode "option_values=5mm, 10mm" >/dev/null
VAR=$(sql "select id from product_variants where product_id='$PROD' order by id limit 1")
code POST "/admin/products/$PROD/variants" --data-urlencode "variant_id=$VAR" \
	--data-urlencode "sku_$VAR=" --data-urlencode "price_delta_$VAR=0" \
	--data-urlencode "delta_$VAR=10" --data-urlencode "version_$VAR=0" >/dev/null

want "상품에 조합이 생겼다" 2 "$(sql "select count(*) from product_variants where product_id='$PROD'")"

# **결제사를 설정한다.** 없으면 주문서(P-405)가 503 이고 — 그것이 옳은 동작이다
# (D19 P-405) — 주문이 생기지 않아 구매 이후 화면 전부가 크롤 대상에서 빠진다.
code POST /admin/settings/payment --data-urlencode "pg.provider=toss" \
	--data-urlencode "pg.client_key=crawl_ck" --data-urlencode "pg.secret_key=crawl_sk" >/dev/null

