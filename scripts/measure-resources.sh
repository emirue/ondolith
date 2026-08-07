#!/bin/sh
# measure-resources.sh — W4-08. NFR-101 의 티어에서 **실제로 띄워 재본다.**
#
# 1 vCPU / 512MB 는 문서에 적힌 목표일 뿐이고, 그 안에서 도는지는 그 제약을
# 걸고 돌려 봐야 안다. docker 의 `--cpus`·`--memory` 가 cgroup 으로 같은
# 제약을 만든다 — 실제 Lightsail 인스턴스와 같은 것은 아니지만, **메모리
# 상한을 넘기면 커널이 죽인다는 점은 같다.**
#
# 측정값은 D70 에 적는다. 여기서는 그 숫자를 내고, 상한을 넘으면 실패한다.
set -eu

cd "$(dirname "$0")/.."
BIN=ondolith
NET=ondolith-measure
PG=ondolith-measure-db
APP=ondolith-measure-app
MEM_LIMIT_MB=512
# NFR-101: 유휴 RSS 가 티어 메모리의 **절반**을 넘지 않는다.
RSS_CEILING_MB=$((MEM_LIMIT_MB / 2))

command -v docker >/dev/null 2>&1 || { echo "  ✗ docker 가 없다 — 실측할 수 없다"; exit 1; }

arch=$(docker version --format '{{.Server.Arch}}' 2>/dev/null || echo amd64)
bin="dist/$BIN-linux-$arch"
[ -f "$bin" ] || { echo "  ✗ $bin 이 없다. make release 를 먼저 돌린다"; exit 1; }

cleanup() {
	docker rm -f "$APP" "$PG" >/dev/null 2>&1 || true
	docker network rm "$NET" >/dev/null 2>&1 || true
	rm -rf "$tmp" 2>/dev/null || true
}
trap cleanup EXIT
tmp=$(mktemp -d)

docker network create "$NET" >/dev/null
# PostgreSQL 을 같은 인스턴스에 올린 구성이다 (D70 이 권장값을 적는 대상).
# **PostgreSQL 도 같은 티어 안에 둔다.** 앱만 제한하고 DB 를 밖에 두면
# "1 vCPU / 512MB 에서 돈다" 가 아니라 "앱 혼자 512MB 안에서 돈다" 를 재게
# 된다 — D70 이 적는 구성은 둘을 같은 인스턴스에 올린 것이다.
#
# 512MB 를 나눠 쓴다: DB 384MB (shared_buffers 128MB + 작업 메모리·커넥션),
# 앱 128MB. 앱의 유휴 RSS 가 한 자리 MiB 이므로 이 배분이 실제 권고와 같다.
docker run -d --name "$PG" --network "$NET" \
	--cpus=1 --memory=384m \
	-e POSTGRES_PASSWORD=m -e POSTGRES_USER=m -e POSTGRES_DB=m \
	postgres:18-alpine -c shared_buffers=128MB >/dev/null

# **설정 파일을 미리 쓰지 않는다.** 쓰면 앱이 설치 마법사가 아니라 운영 모드로
# 부팅해서, 아래 설치 POST 가 아무 일도 하지 않고 관리자 계정도 안 생긴다 —
# "설치를 마친 뒤" 라고 적어 놓고 빈 DB 를 재는 것이 된다. 설정 파일은
# 마법사가 쓴다.

# DB 가 뜰 때까지 기다린다. 여기서 실패하면 측정이 아니라 하네스 문제다.
i=0
until docker exec "$PG" pg_isready -U m >/dev/null 2>&1; do
	i=$((i + 1)); [ "$i" -gt 60 ] && { echo "  ✗ DB 가 뜨지 않았다"; exit 1; }
	sleep 1
done

docker run -d --name "$APP" --network "$NET" \
	--cpus=1 --memory=128m \
	-v "$PWD/$bin:/ondolith:ro" -v "$tmp:/work" -w /work \
	alpine:3 /ondolith >/dev/null

# 설치 화면이 응답할 때까지. 「떴다」의 정의는 프로세스가 아니라 응답이다.
i=0
until docker exec "$APP" /ondolith -version >/dev/null 2>&1 &&
	docker run --rm --network "$NET" alpine:3 \
		sh -c "wget -q -O- http://$APP:8080/healthz 2>/dev/null || wget -q -O- http://$APP:8080/install 2>/dev/null" \
		>/dev/null 2>&1; do
	i=$((i + 1))
	[ "$i" -gt 60 ] && { echo "  ✗ 60초 안에 응답하지 않았다"; docker logs "$APP" 2>&1 | tail -20; exit 1; }
	sleep 1
done

# **설치를 마친 상태에서 잰다.** 설치 트리는 템플릿도 라우트도 적어서, 그
# 숫자는 운영 중인 사이트의 것이 아니다 — 재는 대상을 실제로 쓰는 구성으로
# 맞춘다 (마이그레이션 전부 적용 + 운영 트리 조립 + 테마 로드).
install_out=$(docker run --rm --network "$NET" alpine:3 sh -c "
	wget -q -O- --post-data='db_host=$PG&db_port=5432&db_user=m&db_password=m&db_name=m&db_sslmode=disable&site_name=measure&admin_email=a@example.com&admin_password=correct-horse-battery&admin_password_confirm=correct-horse-battery' \
		--header='Origin: http://$APP:8080' \
		--header='Content-Type: application/x-www-form-urlencoded' \
		http://$APP:8080/install 2>&1 || true" 2>&1) || true

# 설치가 끝나면 운영 트리로 갈아탄다. 홈이 200 이면 그 상태다.
i=0
until docker run --rm --network "$NET" alpine:3 \
	wget -q -O- "http://$APP:8080/" >/dev/null 2>&1; do
	i=$((i + 1))
	if [ "$i" -gt 60 ]; then
		echo "  ✗ 설치가 끝나지 않았다"
		printf '%s\n' "$install_out" | tail -5
		docker logs "$APP" 2>&1 | tail -20
		exit 1
	fi
	sleep 1
done
# 설치가 실제로 끝났는지: 관리자 계정이 있어야 한다.
users=$(docker exec "$PG" psql -U m -tAc "SELECT count(*) FROM users" 2>/dev/null | tr -d ' \r')
if [ "${users:-0}" -lt 1 ]; then
	echo "  ✗ 설치가 관리자 계정을 만들지 않았다 (users=$users) — 빈 DB 를 재게 된다"
	docker logs "$APP" 2>&1 | tail -20
	exit 1
fi
# **shop 모드로 잰다.** 설치 폼에는 사이트 유형 필드가 없어서 기본은 cms 이고,
# 그러면 커머스 라우트도 웹훅 서브트리도 등록되지 않는다 — 결제를 다루는 배포의
# 크기를 재려는데 그 절반이 빠진 구성을 재게 된다. 트리는 조립 시점에 정해지므로
# 설정을 바꾼 뒤 **다시 띄운다** (D20 「모듈 게이팅」).
docker exec "$PG" psql -U m -q -c \
	"INSERT INTO settings (key,value) VALUES ('site.type','shop'),('pg.provider','toss')
	 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value" >/dev/null 2>&1 || true
docker restart "$APP" >/dev/null
i=0
until docker run --rm --network "$NET" alpine:3 \
	wget -q -O- "http://$APP:8080/shop" >/dev/null 2>&1; do
	i=$((i + 1))
	[ "$i" -gt 60 ] && { echo "  ✗ shop 모드로 다시 뜨지 않았다"; docker logs "$APP" 2>&1|tail -20; exit 1; }
	sleep 1
done
echo "  설치 완료 (users=$users) — shop 모드 운영 트리에서 잰다"

# 몇 개 화면을 실제로 그려 본다. 템플릿은 처음 그릴 때 파싱되므로, 한 번도
# 그리지 않은 상태의 RSS 는 운영 중인 값이 아니다.
for path in / /healthz /login /shop; do
	docker run --rm --network "$NET" alpine:3 \
		wget -q -O- "http://$APP:8080$path" >/dev/null 2>&1 || true
done

# 유휴 상태를 재기 전에 잠깐 둔다. 기동 직후의 최고점은 유휴값이 아니다.
sleep 5

# **아직 살아 있는지 먼저 본다.** 죽은 컨테이너에 `docker stats` 를 걸면
# `0B / 0B` 를 exit 0 으로 내놓는다 — 즉 **메모리 한도를 넘겨 커널에 죽은
# 경우가 0 MiB 라는 최고 성적으로 보고된다.** NFR-101 이 깨진 바로 그 순간에
# 기준 충족이 찍히는 셈이다. 화면을 그리는 루프가 `|| true` 로 끝나므로
# 그 죽음은 다른 어디에서도 드러나지 않는다.
state=$(docker inspect -f '{{.State.Running}} {{.State.OOMKilled}} {{.State.ExitCode}}' "$APP" 2>/dev/null || echo "unknown")
case "$state" in
"true false"*) : ;;
*)
	echo "  ✗ 측정 시점에 앱이 살아 있지 않다 (Running/OOMKilled/Exit = $state)"
	docker logs "$APP" 2>&1 | tail -20
	exit 1 ;;
esac
rss_bytes=$(docker stats --no-stream --format '{{.MemUsage}}' "$APP" | awk '{print $1}')
rss_mb=$(printf '%s' "$rss_bytes" | awk '
	/GiB/ { gsub(/GiB/,""); printf "%.0f", $1 * 1024; exit }
	/MiB/ { gsub(/MiB/,""); printf "%.0f", $1; exit }
	/KiB/ { gsub(/KiB/,""); printf "%.1f", $1 / 1024; exit }
	{ printf "%.1f", $1 / 1048576 }')

# **측정값이 숫자인지 먼저 본다.** `docker stats` 가 실패하면 rss_mb 가 비고,
# 빈 문자열과 상한을 비교하면 awk 가 0(=통과)을 낸다 — 재지 못한 것이 기준을
# 충족한 것으로 보고된다. 이 스크립트의 존재 이유가 그 숫자 하나이므로,
# 숫자가 없으면 실패다.
case "$rss_mb" in
'' | *[!0-9.]* )
	echo "  ✗ 유휴 RSS 를 재지 못했다 (docker stats 출력: [${rss_bytes:-없음}])"
	exit 1 ;;
esac

# DB 도 함께 잰다. 합이 티어를 넘으면 그 구성은 그 인스턴스에서 안 돈다.
pg_bytes=$(docker stats --no-stream --format '{{.MemUsage}}' "$PG" | awk '{print $1}')
pg_mb=$(printf '%s' "$pg_bytes" | awk '
	/GiB/ { gsub(/GiB/,""); printf "%.0f", $1 * 1024; exit }
	/MiB/ { gsub(/MiB/,""); printf "%.0f", $1; exit }
	/KiB/ { gsub(/KiB/,""); printf "%.1f", $1 / 1024; exit }
	{ printf "%.1f", $1 / 1048576 }')
case "$pg_mb" in
'' | *[!0-9.]* ) echo "  ✗ DB 메모리를 재지 못했다 (출력: [${pg_bytes:-없음}])"; exit 1 ;;
esac

echo "  측정: 앱 ${rss_mb} MiB + DB ${pg_mb} MiB / 티어 ${MEM_LIMIT_MB}MB (1 vCPU)"
echo "        앱 유휴 RSS 상한 ${RSS_CEILING_MB} MiB"

# 합이 티어를 넘으면 그 구성은 그 인스턴스에서 돌지 않는다.
total_over=$(awk -v a="$rss_mb" -v b="$pg_mb" -v t="$MEM_LIMIT_MB" \
	'BEGIN{print (a + b > t) ? 1 : 0}')
if [ "$total_over" = "1" ]; then
	echo "  ✗ 앱+DB 합이 티어 메모리를 넘었다 (NFR-101)"
	exit 1
fi
over=$(awk -v a="$rss_mb" -v b="$RSS_CEILING_MB" 'BEGIN{print (a + 0 > b + 0) ? 1 : 0}')
if [ "$over" = "1" ]; then
	echo "  ✗ 유휴 RSS 가 티어 메모리의 절반을 넘었다 (NFR-101)"
	exit 1
fi
echo "  ✓ NFR-101 유휴 메모리 기준 충족"
