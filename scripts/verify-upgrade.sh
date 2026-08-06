#!/bin/sh
# verify-upgrade.sh — W4-11. **실제 데이터가 있는 인스턴스에서** 업그레이드
# 절차를 그대로 밟아 본다.
#
# D70 「업그레이드 절차」가 3단계(백업 → 바이너리 교체 → 재시작)로 끝난다고
# 적혀 있는데, 그것이 사실인지는 데이터가 든 인스턴스에서 밟아 봐야 안다.
# 빈 DB 에서는 대기 마이그레이션도 없고 깨질 데이터도 없어서 늘 성공한다.
#
# 여기서 확인하는 것:
#  ① 설치를 마치고 데이터를 넣은 인스턴스가 만들어진다
#  ② 백업(pg_dump)이 떠지고 복원 가능한 형식이다
#  ③ **한 릴리즈 이전 바이너리로 올라온 인스턴스**에 새 바이너리를 얹으면
#     대기 마이그레이션이 부팅 때 적용되고 기존 데이터가 남는다
#  ④ 그 뒤 백업으로 되돌리면 원래 상태가 된다 (NFR-308 의 유일한 경로)
set -eu

cd "$(dirname "$0")/.."
BIN=ondolith
NET=ondolith-upgrade
PG=ondolith-upgrade-db
APP=ondolith-upgrade-app
fail=0
say() { printf '  %s %s\n' "$1" "$2"; }

command -v docker >/dev/null 2>&1 || { say "✗" "docker 가 없다 — 실측할 수 없다"; exit 1; }
arch=$(docker version --format '{{.Server.Arch}}' 2>/dev/null || echo amd64)
new="dist/$BIN-linux-$arch"
[ -f "$new" ] || { say "✗" "$new 이 없다. make release 를 먼저 돌린다"; exit 1; }

tmp=$(mktemp -d)  # 절대경로다 — $PWD 를 앞에 붙이지 않는다
cleanup() {
	docker rm -f "$APP" "$PG" >/dev/null 2>&1 || true
	docker network rm "$NET" >/dev/null 2>&1 || true
	rm -rf "$tmp" 2>/dev/null || true
}
trap cleanup EXIT

# **한 릴리즈 이전**을 흉내 낸다: 최신 마이그레이션을 뺀 바이너리를 따로 빌드해
# 그것으로 설치한 뒤, 진짜 산출물을 얹는다. 같은 바이너리를 두 번 올리면
# "대기 마이그레이션이 적용된다" 를 확인할 수 없다.
latest=$(ls internal/migrations/*.sql | sort | tail -1)
mkdir -p "$tmp/old"
cp -R . "$tmp/old/src" 2>/dev/null || true
rm -rf "$tmp/old/src/.git" "$tmp/old/src/dist"
rm -f "$tmp/old/src/$latest"
(cd "$tmp/old/src" && CGO_ENABLED=0 GOOS=linux GOARCH="$arch" \
	go build -trimpath -ldflags "-s -w -X main.version=vOLD" -o "$tmp/old/$BIN" ./cmd/ondolith) \
	|| { say "✗" "이전 바이너리 빌드 실패"; exit 1; }
say "✓" "이전 릴리즈 흉내: $(basename "$latest") 를 뺀 바이너리"

docker network create "$NET" >/dev/null
docker run -d --name "$PG" --network "$NET" \
	-e POSTGRES_PASSWORD=u -e POSTGRES_USER=u -e POSTGRES_DB=u postgres:18-alpine >/dev/null
i=0; until docker exec "$PG" pg_isready -U u >/dev/null 2>&1; do
	i=$((i+1)); [ "$i" -gt 60 ] && { say "✗" "DB 가 뜨지 않았다"; exit 1; }; sleep 1
done

# **설정 파일을 미리 쓰지 않는다.** 쓰면 앱이 설치 마법사가 아니라 운영 모드로
# 부팅하고, 관리자 계정이 만들어지지 않는다 — 그 상태로 "데이터가 남았다" 를
# 확인하면 users=0 을 users=0 과 비교하는 공허한 검사가 된다.
# 설정 파일은 마법사가 쓴다 (설치 완료의 정의가 그것이다).

start_app() {  # $1=바이너리 경로
	# **지우기 전에 로그를 모은다.** `docker rm` 하면 그 컨테이너의 로그가
	# 사라져서, 마지막 기동의 로그만 검사하게 된다 — 설치·업그레이드 때 찍힌
	# 것을 못 보고 지나간다.
	docker logs "$APP" >> "$tmp/all.log" 2>&1 || true
	docker rm -f "$APP" >/dev/null 2>&1 || true
	docker run -d --name "$APP" --network "$NET" \
		-v "$1:/ondolith:ro" -v "$tmp:/work" -w /work alpine:3 /ondolith >/dev/null
	i=0
	until docker run --rm --network "$NET" alpine:3 \
		sh -c "wget -q -O- http://$APP:8080/healthz || wget -q -O- http://$APP:8080/install" >/dev/null 2>&1; do
		i=$((i+1))
		[ "$i" -gt 60 ] && { say "✗" "앱이 뜨지 않았다"; docker logs "$APP" 2>&1 | tail -20; return 1; }
		sleep 1
	done
}

# ① 이전 릴리즈로 설치하고 데이터를 넣는다.
start_app "$tmp/old/$BIN" || exit 1
docker run --rm --network "$NET" alpine:3 sh -c "
	wget -q -O- --post-data='db_host=$PG&db_port=5432&db_user=u&db_password=u&db_name=u&db_sslmode=disable&site_name=upgrade-test&admin_email=a@example.com&admin_password=correct-horse-battery&admin_password_confirm=correct-horse-battery' \
		--header='Origin: http://$APP:8080' \
		--header='Content-Type: application/x-www-form-urlencoded' \
		http://$APP:8080/install" >/dev/null 2>&1 || true
i=0; until docker run --rm --network "$NET" alpine:3 wget -q -O- "http://$APP:8080/" >/dev/null 2>&1; do
	i=$((i+1)); [ "$i" -gt 60 ] && { say "✗" "설치가 끝나지 않았다"; docker logs "$APP" 2>&1|tail -20; exit 1; }; sleep 1
done
docker exec "$PG" psql -U u -q -c \
	"INSERT INTO pages (slug,title,body,status) VALUES ('upgrade-probe','업그레이드 표식','본문','published')" \
	>/dev/null 2>&1 || true
before=$(docker exec "$PG" psql -U u -tAc "SELECT count(*) FROM users" 2>/dev/null | tr -d ' \r')
# **관리자 계정이 실제로 만들어졌는지 확인한다.** 0 이면 설치가 아니라 빈 DB 에
# 마이그레이션만 돈 것이고, 그 뒤의 "데이터가 남았다" 는 0 과 0 의 비교다.
if [ "${before:-0}" -lt 1 ]; then
	say "✗" "설치가 관리자 계정을 만들지 않았다 (users=$before) — 설치가 아니라 빈 DB 다"
	docker logs "$APP" 2>&1 | tail -20
	exit 1
fi
say "✓" "이전 릴리즈로 설치 완료 (users=$before)"

# 업그레이드가 건드리면 안 되는 것들의 해시를 뜬다: 설정 파일·업로드·테마.
# 바이너리 교체가 이것들을 만지면 운영자는 백업에서 되돌릴 수밖에 없는데,
# 절차에는 그 단계가 없다.
docker exec "$APP" sh -c 'mkdir -p /work/uploads /work/themes/mine &&
	echo upload-probe > /work/uploads/probe.bin &&
	echo theme-probe > /work/themes/mine/base.html' >/dev/null 2>&1 || true
hash_of() { docker run --rm -v "$tmp:/w" alpine:3 sh -c \
	'md5sum /w/ondolith.json /w/uploads/probe.bin /w/themes/mine/base.html 2>/dev/null | sort'; }
before_hash=$(hash_of)
[ -n "$before_hash" ] && say "✓" "설정·업로드·테마 해시 기록" \
	|| { say "✗" "해시를 뜨지 못했다"; fail=1; }

# ② 백업.
docker exec "$PG" pg_dump -U u -Fc u > "$tmp/before.dump" 2>/dev/null
[ -s "$tmp/before.dump" ] || { say "✗" "백업이 비어 있다"; exit 1; }
head -c 5 "$tmp/before.dump" | grep -q "PGDMP" \
	&& say "✓" "백업 $(wc -c < "$tmp/before.dump" | tr -d ' ') 바이트 (pg_dump 커스텀 형식)" \
	|| { say "✗" "백업이 pg_dump 형식이 아니다"; fail=1; }

# ③ 바이너리 교체 + 재시작. 대기 마이그레이션이 부팅 때 적용되어야 한다.
start_app "$PWD/$new" || exit 1
after=$(docker exec "$PG" psql -U u -tAc "SELECT count(*) FROM users" 2>/dev/null | tr -d ' \r')
probe=$(docker exec "$PG" psql -U u -tAc \
	"SELECT count(*) FROM pages WHERE slug='upgrade-probe'" 2>/dev/null | tr -d ' \r')
applied=$(docker exec "$PG" psql -U u -tAc \
	"SELECT count(*) FROM goose_db_version WHERE version_id = $(basename "$latest" | cut -d_ -f1 | sed 's/^0*//')" \
	2>/dev/null | tr -d ' \r')

# **두 값이 모두 비어도 같다.** psql 이 실패하면 before 도 after 도 빈
# 문자열이 되고, 그 비교는 참이라 "데이터가 남았다" 가 나온다 — 아무것도
# 확인하지 않은 채로. 숫자인지부터 본다.
case "$after" in
'' | *[!0-9]* ) say "✗" "업그레이드 뒤 users 를 세지 못했다 (출력: [$after])"; fail=1 ;;
* )
	[ "$after" = "$before" ] && say "✓" "기존 데이터가 남았다 (users=$after)" \
		|| { say "✗" "users 가 $before → $after 로 바뀌었다"; fail=1; } ;;
esac
[ "$probe" = "1" ] && say "✓" "업그레이드 전에 넣은 행이 그대로다" \
	|| { say "✗" "표식 행이 사라졌다"; fail=1; }
[ "$applied" = "1" ] && say "✓" "대기 마이그레이션이 부팅 때 적용됐다 ($(basename "$latest"))" \
	|| { say "✗" "대기 마이그레이션이 적용되지 않았다"; fail=1; }

# 설정·업로드·테마가 그대로인지. 바이너리 교체가 이것들을 만지면 안 된다.
after_hash=$(hash_of)
[ "$after_hash" = "$before_hash" ] && say "✓" "설정·업로드·테마가 바뀌지 않았다" \
	|| { say "✗" "업그레이드가 파일을 건드렸다"; printf '%s\n---\n%s\n' "$before_hash" "$after_hash"; fail=1; }

# ④ **다운그레이드 실측.** 이전 바이너리로 되돌려도 뜨는지 본다. 추가만 하는
# 마이그레이션은 앞 릴리즈가 모르는 컬럼을 남길 뿐이라 기동을 막지 않는다 —
# 그것이 D30 「두 릴리즈 규칙」이 지키려는 성질이고, 여기서 확인한다.
if start_app "$tmp/old/$BIN"; then
	down_users=$(docker exec "$PG" psql -U u -tAc "SELECT count(*) FROM users" 2>/dev/null | tr -d ' \r')
	case "$down_users" in
	'' | *[!0-9]* ) say "✗" "다운그레이드 뒤 users 를 세지 못했다 (출력: [$down_users])"; fail=1 ;;
	* )
		[ "$down_users" = "$before" ] && say "✓" "이전 바이너리로 되돌려도 뜨고 데이터가 남았다 (users=$down_users)" \
			|| { say "✗" "다운그레이드 후 users=$down_users (want $before)"; fail=1; } ;;
	esac
else
	say "✗" "이전 바이너리로 되돌리자 기동하지 못했다"; fail=1
fi

# ⑤ **로그에 DSN·시크릿이 없다** (W4-12, D22 4절). 실기동 로그로 확인한다 —
# "안 찍는다" 는 규칙은 로그를 봐야 확인된다.
docker logs "$APP" >> "$tmp/all.log" 2>&1 || true
logs=$(cat "$tmp/all.log" 2>/dev/null || true)

# **빈 로그는 "누출 없음" 이 아니다.** 로그를 못 읽었는데 통과시키면, 이
# 검사는 아무것도 확인하지 않은 채 매번 초록이 된다. 기동 로그가 있어야
# 읽은 것이다.
if [ -z "$logs" ]; then
	say "✗" "로그를 하나도 읽지 못했다 — 누출 검사가 헛돈다"
	fail=1
elif ! printf '%s' "$logs" | grep -qE '설치 완료|운영 모드|listening|시작'; then
	say "✗" "기동 로그가 없다 (읽은 길이 $(printf '%s' "$logs" | wc -c)) — 다른 것을 읽었다"
	fail=1
else
	leaked=0
	for needle in "postgres://" "password=" "u:u@" "correct-horse-battery"; do
		printf '%s' "$logs" | grep -q "$needle" && { say "✗" "로그에 [$needle] 이 있다"; leaked=1; }
	done
	[ "$leaked" -eq 0 ] && say "✓" "실기동 로그 $(printf '%s' "$logs" | wc -l | tr -d ' ')줄에 DSN·자격증명이 없다"
	[ "$leaked" -eq 0 ] || fail=1
fi

# ⑥ 백업 복원이 실제로 되는지 (NFR-308 의 유일한 경로).
docker rm -f "$APP" >/dev/null 2>&1 || true
docker exec "$PG" psql -U u -q -c "DROP SCHEMA public CASCADE; CREATE SCHEMA public" >/dev/null 2>&1
docker exec -i "$PG" pg_restore -U u -d u --no-owner >/dev/null 2>&1 < "$tmp/before.dump" || true
restored=$(docker exec "$PG" psql -U u -tAc \
	"SELECT count(*) FROM pages WHERE slug='upgrade-probe'" 2>/dev/null | tr -d ' \r')
[ "$restored" = "1" ] && say "✓" "백업 복원으로 원래 상태가 됐다" \
	|| { say "✗" "복원 후 표식 행이 없다 (restored=$restored)"; fail=1; }

[ "$fail" -eq 0 ] || { echo "업그레이드 절차 검증 실패"; exit 1; }
echo "업그레이드 절차 검증 ok (백업 → 교체 → 재시작 3단계)"
