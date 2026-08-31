#!/bin/sh
# verify-install-doc.sh — D71·D72 를 **문서에 적힌 그대로** 밟는다.
#
# 왜 있나: 문서의 절차는 아무도 실행하지 않으면 틀린 채로 남는다. D71 2절은
# `/opt/ondolith` 를 `sudo mkdir` 로 만들고 3절은 그것을 **일반 사용자로** 띄웠다
# — 그 사이에 소유권을 넘기는 줄이 없어서, 4절 제출이
# `설정 파일을 저장하지 못했습니다: ... permission denied` 로 끝났다. 그 디렉터리는
# 내려받는 곳이 아니라 앱이 쓰는 곳이다(`ondolith.json` 과 업로드·테마).
# D72 는 `User=ondolith` 유닛을 실으면서 그 계정을 만드는 절차가 없었다.
# 둘 다 사람이 문서만 보고 배포해 보다 나왔다 (W4-13).
#
# **완료 판정을 /healthz 로 하지 않는다.** 그 경로는 설치 모드에서도 200 이라
# 설치가 끝났는지를 구분하지 못한다 — 이 스크립트의 첫 판은 그것으로 재다가
# 고장난 절차를 「성공」이라고 답했다. 설치 완료의 정의는 설정 파일이 써진
# 것이고(D20), 그때 `/install` 은 404 가 된다(D71 5절). 둘 다 본다.
#
# ⑥ 은 **음성 대조**다: `chown` 을 뺀 절차가 실제로 실패해야 이 검사가 무엇을
# 보고 있는 것이다. 빠지면 전부 통과시키는 검사와 구별되지 않는다.
set -u

cd "$(dirname "$0")/.."
NET=ondolith-doc
PG=ondolith-doc-db
APP=ondolith-doc-app
fail=0
say() { printf '  %s %s\n' "$1" "$2"; }

command -v docker >/dev/null 2>&1 || { say "✗" "docker 가 없다 — 실측할 수 없다"; exit 1; }
arch=$(docker version --format '{{.Server.Arch}}' 2>/dev/null || echo amd64)
BIN="$PWD/dist/ondolith-linux-$arch"
[ -f "$BIN" ] || { say "✗" "$BIN 이 없다. make release 를 먼저 돌린다"; exit 1; }

cleanup() {
	docker rm -f "$APP" "$PG" >/dev/null 2>&1 || true
	docker network rm "$NET" >/dev/null 2>&1 || true
}
trap cleanup EXIT
cleanup

docker network create "$NET" >/dev/null
docker run -d --name "$PG" --network "$NET" \
	-e POSTGRES_PASSWORD=u -e POSTGRES_USER=u -e POSTGRES_DB=u postgres:18-alpine >/dev/null
i=0; until docker exec "$PG" pg_isready -h 127.0.0.1 -U u >/dev/null 2>&1; do
	i=$((i + 1)); [ "$i" -gt 60 ] && { say "✗" "DB 가 뜨지 않았다"; exit 1; }; sleep 1
done

# PID 1 은 sleep 이다. 앱을 PID 1 로 띄우면 D72 로 넘어갈 때 앱을 세우는 순간
# 컨테이너가 통째로 죽어 이관을 확인할 수 없다.
docker run -d --name "$APP" --network "$NET" -v "$BIN:/src/ondolith:ro" \
	ubuntu:24.04 sleep infinity >/dev/null
docker exec "$APP" sh -c 'useradd -m op &&
	apt-get update -qq >/dev/null 2>&1 && apt-get install -y -qq sudo >/dev/null 2>&1 &&
	echo "op ALL=(ALL) NOPASSWD: ALL" >> /etc/sudoers' || {
	say "✗" "컨테이너 준비 실패"; exit 1; }

as_op() { docker exec "$APP" su - op -c "$1"; }
boot() { # $1=사용자
	docker exec -d "$APP" sh -c "cd /opt/ondolith &&
		setpriv --reuid=$1 --regid=$1 --clear-groups ./ondolith -addr 0.0.0.0:8080"
	i=0
	until docker run --rm --network "$NET" alpine:3 \
		wget -q -O /dev/null "http://$APP:8080/install" >/dev/null 2>&1 ||
		docker run --rm --network "$NET" alpine:3 \
			wget -q -O /dev/null "http://$APP:8080/healthz" >/dev/null 2>&1; do
		i=$((i + 1)); [ "$i" -gt 60 ] && return 1; sleep 1
	done
}
submit() {
	docker run --rm --network "$NET" alpine:3 sh -c "
		wget -q -O- --post-data='db_host=$PG&db_port=5432&db_user=u&db_password=u&db_name=u&db_sslmode=disable&site_name=doc-test&admin_email=a@example.com&admin_password=correct-horse-battery&admin_password_confirm=correct-horse-battery' \
			--header='Origin: http://$APP:8080' \
			--header='Content-Type: application/x-www-form-urlencoded' \
			http://$APP:8080/install" >/dev/null 2>&1 || true
	sleep 3
}
install_code() {
	docker run --rm --network "$NET" alpine:3 \
		wget -S -q -O /dev/null "http://$APP:8080/install" 2>&1 |
		grep -o 'HTTP/1.1 [0-9]*' | tail -1 | awk '{print $2}'
}
reset_all() {
	docker exec "$APP" pkill -f 'ondolith -addr' >/dev/null 2>&1 || true
	docker exec "$APP" rm -rf /opt/ondolith >/dev/null 2>&1 || true
	docker exec "$PG" psql -U u -q -c 'DROP SCHEMA public CASCADE; CREATE SCHEMA public' >/dev/null 2>&1
	sleep 1
}

# ① D71 2절 — 디렉터리를 만들고 소유권을 넘긴다.
as_op 'sudo mkdir -p /opt/ondolith && sudo chown "$(id -un)" /opt/ondolith &&
	cd /opt/ondolith && sudo cp /src/ondolith ondolith && sudo chmod +x ondolith' >/dev/null 2>&1 ||
	{ say "✗" "D71 2절이 실패했다"; exit 1; }
say "✓" "D71 2절: /opt/ondolith 를 만들고 설치자에게 넘겼다"

# ②③ D71 3·4절 — 일반 사용자로 띄우고 브라우저 설치를 제출한다.
boot op || { say "✗" "D71 3절: 앱이 뜨지 않았다"; exit 1; }
submit
code=$(install_code)
cfg=$(docker exec "$APP" stat -c '%U' /opt/ondolith/ondolith.json 2>/dev/null || echo "없음")
if [ "$code" = "404" ] && [ "$cfg" = "op" ]; then
	say "✓" "D71 4절: 설치가 끝났다 (/install=404 · ondolith.json 소유자 $cfg)"
else
	say "✗" "D71 대로 설치가 끝나지 않았다 (/install=$code · ondolith.json 소유자 $cfg)"
	docker logs "$APP" 2>&1 | tail -5
	fail=1
fi

# ④⑤ D72 2절 — 전용 계정을 만들고 소유권을 넘긴 뒤 그 계정으로 띄운다.
docker exec "$APP" pkill -f 'ondolith -addr' >/dev/null 2>&1 || true
sleep 1
docker exec "$APP" sh -c \
	'useradd --system --home-dir /opt/ondolith --shell /usr/sbin/nologin ondolith &&
	 chown -R ondolith:ondolith /opt/ondolith' >/dev/null 2>&1 ||
	{ say "✗" "D72 2절의 계정 생성·이관이 실패했다"; fail=1; }
if boot ondolith && [ "$(install_code)" = "404" ]; then
	say "✓" "D72 2절: ondolith 계정으로 기동해 운영 모드다 (설치 산출물이 함께 넘어갔다)"
else
	say "✗" "D72 2절 뒤 ondolith 계정으로 운영 모드가 되지 않았다"
	docker logs "$APP" 2>&1 | tail -5
	fail=1
fi
if docker exec "$APP" su -s /bin/sh ondolith -c \
	'mkdir -p /opt/ondolith/uploads && touch /opt/ondolith/uploads/.probe' >/dev/null 2>&1; then
	say "✓" "서비스 계정이 업로드 디렉터리에 쓸 수 있다"
else
	say "✗" "서비스 계정이 업로드 디렉터리에 쓸 수 없다 — ReadWritePaths 만으로는 부족하다"
	fail=1
fi

# ⑥ 음성 대조 — chown 을 빼면 반드시 실패해야 한다.
reset_all
as_op 'sudo mkdir -p /opt/ondolith && cd /opt/ondolith &&
	sudo cp /src/ondolith ondolith && sudo chmod +x ondolith' >/dev/null 2>&1
if boot op; then
	submit
	code=$(install_code)
	if [ "$code" = "404" ]; then
		say "✗" "chown 없이도 설치가 끝났다 — 이 검사는 아무것도 재지 않는다"
		fail=1
	else
		say "✓" "음성 대조: chown 을 빼면 설치가 끝나지 않는다 (/install=$code)"
	fi
else
	say "✓" "음성 대조: chown 을 빼면 앱이 뜨지도 않는다"
fi

[ "$fail" -eq 0 ] || { echo "설치 문서 검증 실패 — D71·D72 의 절차가 그대로는 통하지 않는다"; exit 1; }
echo "설치 문서 검증 ok (D71 2·3·4절 → D72 2절)"
