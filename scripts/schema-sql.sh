#!/bin/sh
# docs/schema.sql 을 다시 만든다 — ERD 도구(erdmates 등)에 올리는 통합 스키마.
#
# **손으로 쓰지 않는다.** 마이그레이션 18개를 눈으로 합쳐 적으면 반드시
# 어긋나고, 어긋나도 아무도 모른다. 실제로 `orders.user_id` 가 문서에는
# RESTRICT, 스키마에는 SET NULL 로 1년 가까이 갈라져 있었다 (M16 다음 사례,
# CHANGELOG 「주문 이력이 있는 계정」). 그래서 이 파일은 **빈 데이터베이스에
# 마이그레이션을 전부 적용한 뒤 pg_dump 로 뽑는다.** 뽑은 것이 곧 진실이다.
#
# 재생성: make schema
set -eu

cd "$(dirname "$0")/.."

OUT=docs/schema.sql
DB=ondolith_schema_dump

DSN=$(sh scripts/testdb.sh dsn)
CID=$(docker ps --filter "publish=$(echo "$DSN" | sed 's|.*:\([0-9]*\)/.*|\1|')" -q)
[ -n "$CID" ] || { echo "테스트 DB 컨테이너가 없다 — sh scripts/testdb.sh up" >&2; exit 1; }

# 매번 새 데이터베이스에서 뽑는다. 기존 것에 얹으면 지금은 안 만드는 옛 객체가
# 섞여 들어오고, 그러면 덤프가 마이그레이션보다 커진다.
docker exec "$CID" psql -U ondolith -d postgres -q \
	-c "DROP DATABASE IF EXISTS $DB;" -c "CREATE DATABASE $DB OWNER ondolith;"

TARGET=$(echo "$DSN" | sed "s|/[^/?]*?|/$DB?|")
ONDOLITH_TEST_DSN="$TARGET" go run ./scripts/migrate-only

# --schema-only: 데이터는 ERD 와 무관하다. --no-owner/--no-privileges: 덤프를
# 읽는 쪽이 우리 롤 이름을 알 필요가 없고, 그것이 들어가면 diff 가 환경마다
# 달라진다.
{
	cat <<'EOF'
-- Ondolith 통합 스키마 — ERD 도구에 올리는 파일.
--
-- **자동 생성이다. 손으로 고치지 말 것.** `make schema` 가 빈 데이터베이스에
-- internal/migrations/*.sql 을 전부 적용한 뒤 pg_dump 로 뽑는다. 여기를 고쳐도
-- 다음 생성에서 사라지고, 그 사이에 이 파일은 거짓말을 한다.
--
-- 스키마를 바꾸려면 마이그레이션을 추가하고 (docs/30-data-model.md),
-- 그다음 `make schema` 를 돌린다.
--
-- 설계 의도·정규화 근거·외래키 정책은 docs/30-data-model.md 가 설명한다.
-- 이 파일은 그 결과물이지 설명이 아니다.
EOF
	docker exec "$CID" pg_dump -U ondolith -d "$DB" \
		--schema-only --no-owner --no-privileges --no-comments
} >"$OUT"

docker exec "$CID" psql -U ondolith -d postgres -q -c "DROP DATABASE $DB;"

printf '%s: 테이블 %s개\n' "$OUT" "$(grep -c '^CREATE TABLE' "$OUT")"
