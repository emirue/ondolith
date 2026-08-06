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
docker run -d --name "$PG" --network "$NET" \
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
	--cpus=1 --memory="${MEM_LIMIT_MB}m" \
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
echo "  설치 완료 (users=$users) — 운영 트리에서 잰다"

# 몇 개 화면을 실제로 그려 본다. 템플릿은 처음 그릴 때 파싱되므로, 한 번도
# 그리지 않은 상태의 RSS 는 운영 중인 값이 아니다.
for path in / /healthz /login /shop; do
	docker run --rm --network "$NET" alpine:3 \
		wget -q -O- "http://$APP:8080$path" >/dev/null 2>&1 || true
done

# 유휴 상태를 재기 전에 잠깐 둔다. 기동 직후의 최고점은 유휴값이 아니다.
sleep 5
rss_bytes=$(docker stats --no-stream --format '{{.MemUsage}}' "$APP" | awk '{print $1}')
rss_mb=$(printf '%s' "$rss_bytes" | awk '
	/GiB/ { gsub(/GiB/,""); printf "%.0f", $1 * 1024; exit }
	/MiB/ { gsub(/MiB/,""); printf "%.0f", $1; exit }
	/KiB/ { gsub(/KiB/,""); printf "%.1f", $1 / 1024; exit }
	{ printf "%.1f", $1 / 1048576 }')

echo "  측정: 유휴 RSS ${rss_mb} MiB / 상한 ${RSS_CEILING_MB} MiB (티어 ${MEM_LIMIT_MB}MB, 1 vCPU)"
over=$(awk -v a="$rss_mb" -v b="$RSS_CEILING_MB" 'BEGIN{print (a > b) ? 1 : 0}')
if [ "$over" = "1" ]; then
	echo "  ✗ 유휴 RSS 가 티어 메모리의 절반을 넘었다 (NFR-101)"
	exit 1
fi
echo "  ✓ NFR-101 유휴 메모리 기준 충족"
