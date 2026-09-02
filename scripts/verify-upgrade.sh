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

# **이전 릴리즈를 태그에서 그대로 꺼낸다 — 흉내 내지 않는다.**
#
# 예전에는 현재 소스를 복사해 *번호가 가장 큰* 마이그레이션 하나만 빼서 "이전
# 릴리즈" 라고 불렀다. 그러면 **번호가 낮은 마이그레이션이 나중에 추가된 경우를
# 영원히 못 잡는다**: v0.1.0 은 1~5·7~19 를 싣고 나갔고 그 뒤 6 번이 추가돼
# 실제 업그레이드가 goose 의
#   detected 1 missing (out-of-order) migration lower than database version (19)
# 으로 죽었는데, 흉내 낸 이전 버전에는 6 번이 이미 들어 있어서 이 검사는 계속
# 초록이었다. 검사가 도는 것과 검사가 보는 것은 다르다.
tag=$(git describe --tags --abbrev=0 2>/dev/null || true)
[ -n "$tag" ] || { say "✗" "릴리즈 태그가 없다 — 업그레이드해 올 이전 릴리즈가 없다"; exit 1; }
mkdir -p "$tmp/old/src"
git archive "$tag" | tar -x -C "$tmp/old/src" \
	|| { say "✗" "$tag 의 트리를 꺼내지 못했다"; exit 1; }
(cd "$tmp/old/src" && CGO_ENABLED=0 GOOS=linux GOARCH="$arch" \
	go build -trimpath -ldflags "-s -w -X main.version=$tag" -o "$tmp/old/$BIN" ./cmd/ondolith) \
	|| { say "✗" "$tag 바이너리 빌드 실패"; exit 1; }

# 대기 마이그레이션 = 지금은 있고 $tag 에는 없던 것. ③ 이 확인할 대상이다.
ls "$tmp/old/src"/internal/migrations/*.sql | xargs -n1 basename | sort >"$tmp/old.list"
ls internal/migrations/*.sql | xargs -n1 basename | sort >"$tmp/new.list"
pending=$(comm -13 "$tmp/old.list" "$tmp/new.list" | tail -1)
[ -n "$pending" ] || { say "✗" "$tag 이후 새 마이그레이션이 없다 — ③ 이 헛돈다"; exit 1; }
say "✓" "이전 릴리즈 $tag 를 태그에서 빌드 (대기 마이그레이션 $pending)"

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
	echo theme-probe > /work/themes/mine/base.html' >/dev/null 2>&1 \
	|| { say "✗" "프로브 파일을 만들지 못했다 — 이 검사가 헛돈다"; fail=1; }
# **디렉터리 전체를 본다.** 정해 둔 세 경로만 해시하면 업그레이드가 *추가*
# 하거나 *지운* 파일이 보이지 않는다 — 새 바이너리가 테마 디렉터리에 파일을
# 하나 쓰면 해시는 그대로다.
hash_of() { docker run --rm -v "$tmp:/w" alpine:3 sh -c \
	'find /w/ondolith.json /w/uploads /w/themes -type f 2>/dev/null | sort | xargs md5sum 2>/dev/null'; }
before_hash=$(hash_of)
# 파일 수를 함께 확인한다. 셋 다 있어야 이 검사가 무엇이든 본 것이다.
before_n=$(printf '%s\n' "$before_hash" | grep -c . || true)
if [ "${before_n:-0}" -ge 3 ]; then
	say "✓" "설정·업로드·테마 $before_n 개 파일 해시 기록"
else
	say "✗" "해시 대상이 $before_n 개뿐이다 — 프로브 파일이 만들어지지 않았다"
	fail=1
fi

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
	"SELECT count(*) FROM goose_db_version WHERE version_id = $(echo "$pending" | cut -d_ -f1 | sed 's/^0*//')" \
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
[ "$applied" = "1" ] && say "✓" "대기 마이그레이션이 부팅 때 적용됐다 ($pending)" \
	|| { say "✗" "대기 마이그레이션이 적용되지 않았다"; fail=1; }

# ③' **관리자가 업그레이드 뒤에도 사이트에 들어간다.**
#
# `users` 행 수만 세면 보이지 않는 실패가 있다: 대기 마이그레이션이 컬럼을
# 지우는데(00020 이 `users.is_admin` 을 지운다) 어딘가 그것을 아직 읽고 있으면,
# 행은 그대로 남은 채 **로그인이나 관리자 화면에서 깨진다.** 위 세 검사는 전부
# 초록인 채로다. 제3자 검증이 실제로 확인한 것도 「관리자 로그인 및 /admin/
# 접근」이었다 (W4-13).
#
# A-602 의 대기 마이그레이션이 0 인지도 함께 본다 — 부팅이 적용했다는 것과
# 화면이 그렇게 보고한다는 것은 다른 진술이다 (NFR-302).
adm=$(docker run --rm --network "$NET" alpine:3 sh -c "
	apk add --no-cache curl >/dev/null 2>&1 || exit 9
	curl -sS -c /tmp/j -b /tmp/j -o /dev/null -H 'Origin: http://$APP:8080' \
		-d 'email=a@example.com' -d 'password=correct-horse-battery' \
		http://$APP:8080/login
	printf 'admin=%s ' \"\$(curl -sS -b /tmp/j -o /dev/null -w '%{http_code}' http://$APP:8080/admin/)\"
	printf 'pending=%s' \"\$(curl -sS -b /tmp/j http://$APP:8080/admin/system |
		sed -n '/대기 마이그레이션/,+1p' | grep -o '>[0-9]*<' | tr -d '><' | head -1)\"
" 2>&1)
case "$adm" in
"admin=200 pending=0")
	say "✓" "업그레이드 뒤 관리자 로그인·/admin/ 접근 ok · A-602 대기 마이그레이션 0" ;;
*)
	say "✗" "업그레이드 뒤 관리자가 사이트에 들어가지 못한다 ($adm)"; fail=1 ;;
esac

# 설정·업로드·테마가 그대로인지. 바이너리 교체가 이것들을 만지면 안 된다.
after_hash=$(hash_of)
if [ "$after_hash" = "$before_hash" ]; then
	say "✓" "설정·업로드·테마가 바뀌지 않았다 (파일 $before_n 개)"
else
	say "✗" "업그레이드가 파일을 건드렸다"
	printf '%s\n---\n%s\n' "$before_hash" "$after_hash"
	fail=1
fi

# ④ **다운그레이드 실측.** 이전 바이너리로 되돌려도 뜨는지 본다. 추가만 하는
# 마이그레이션은 앞 릴리즈가 모르는 컬럼을 남길 뿐이라 기동을 막지 않는다 —
# 그것이 D30 「두 릴리즈 규칙」이 지키려는 성질이고, 여기서 확인한다.
#
# **컬럼을 지우는 마이그레이션이어도 같다.** 규칙이 릴리즈 N 에서 대체물로
# 옮겨 두게 하므로, N 은 지워진 컬럼을 읽지 않는다 — 되돌려도 뜬다. 그
# 전제가 깨졌다면(N 이 아직 그 컬럼을 읽는다) 여기서 기동이 실패한다.
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
