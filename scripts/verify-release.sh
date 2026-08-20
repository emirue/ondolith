#!/bin/sh
# verify-release.sh — W4-01. 릴리즈 산출물을 **실제로 실행해서** 검증한다.
#
# 빌드만 확인하고 끝내지 않는 이유: 크로스 컴파일은 성공했는데 대상
# 아키텍처에서 안 도는 바이너리가 나올 수 있고 (잘못된 GOARCH, 실수로 켜진
# CGO), 그 사실은 서버에 올린 뒤에야 드러난다.
#
# 실행 수단은 docker 다. 개발 기계가 linux 도 amd64 도 아니어도 두 산출물을
# 같은 방식으로 돌릴 수 있다 — 없으면 **건너뛰지 않고 실패한다**: "검증했다"
# 와 "검증할 수 없었다" 를 같은 exit 0 으로 두면 게이트가 아니다.
set -eu

cd "$(dirname "$0")/.."
BIN=ondolith
want=${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}
fail=0

say() { printf '  %s %s\n' "$1" "$2"; }

# **어느 경로로 돌았는지 말한다** (GAP-08).
#
# 산출물을 docker 로 실행할 때, 호스트와 같은 아키텍처면 커널이 그냥 exec 하고
# (네이티브) 다르면 binfmt 핸들러가 받는다 (에뮬레이션). 둘은 실패하는 방식이
# 다르다 — 에뮬레이션은 핸들러가 없으면 `exec format error` 로 죽고, 네이티브는
# 그런 실패가 없다. 그래서 **로컬 초록이 CI 초록을 뜻하지 않았다**: Apple
# Silicon 에서 arm64 는 네이티브라 통과했고, amd64 러너에서는 에뮬레이션이라
# 죽었다. 첫 릴리즈가 정확히 그렇게 깨졌다.
#
# 한 기계에서 amd64·arm64 를 모두 돌리면 **한 쪽은 반드시 네이티브, 다른 쪽은
# 반드시 에뮬레이션**이다. 즉 두 경로는 늘 함께 실측돼 왔고, 몰랐을 뿐이다.
# 아래에서 그것을 출력에 남기고 마지막에 둘 다 돌았는지 센다.
#
# **이 카운터는 selftest 가 아니라 `make release` 가 실측한다.** selftest 의
# 사본에는 dist 산출물이 없어 여기까지 오지 못한다(그 앞의 「산출물이 없다」에서
# 멈춘다). 대신 릴리즈가 두 호스트에서 돈다 — 개발 기계(arm64)와 CI 러너
# (amd64) — 이고, 두 곳에서 네이티브/에뮬레이션이 서로 뒤바뀐다. 주입 한 번보다
# 그쪽이 넓다.
host=$(uname -m)
case "$host" in
x86_64 | amd64) host_arch=amd64 ;;
aarch64 | arm64) host_arch=arm64 ;;
*) host_arch=$host ;;
esac
n_native=0
n_emul=0

if ! command -v docker >/dev/null 2>&1; then
	say "✗" "docker 가 없다 — 산출물을 실행해 볼 수 없다"
	exit 1
fi

for target in linux/amd64 linux/arm64; do
	arch=${target#*/}
	path="dist/$BIN-linux-$arch"

	[ -f "$path" ] || { say "✗" "$path 가 없다"; fail=1; continue; }

	# ① 정적 링크. 동적 링크면 대상 서버의 glibc 버전에 걸린다.
	desc=$(file -b "$path")
	case "$desc" in
	*statically\ linked*|*static-pie\ linked*) say "✓" "$arch 정적 링크" ;;
	*) say "✗" "$arch 가 정적 링크가 아니다: $desc"; fail=1 ;;
	esac
	case "$desc" in
	*ELF*) : ;;
	*) say "✗" "$arch 가 ELF 가 아니다: $desc"; fail=1 ;;
	esac

	# ①-b **파일 이름이 아니라 기계 코드를 본다.** 이것이 이 검증기의 존재
	# 이유다: `GOARCH` 를 빠뜨린 빌드는 두 이름으로 같은 아키텍처를 낸다.
	# 정적 링크·ELF 만 보면 그 산출물이 전부 통과한다.
	case "$arch:$desc" in
	amd64:*x86-64*) : ;;
	arm64:*aarch64*) : ;;
	*)
		say "✗" "$arch 산출물의 기계 코드가 다르다: $desc"
		fail=1 ;;
	esac

	# ② **해당 아키텍처에서 실행**해 버전을 보고하게 한다.
	# stderr 를 섞지 않는다: 이미지 pull 진행 표시가 버전 문자열에 끼어들면
	# 대조가 우연히 통과한다.
	if ! got=$(docker run --rm --platform "$target" \
		-v "$PWD/dist:/dist:ro" alpine:3 "/dist/$BIN-linux-$arch" -version 2>/tmp/vr-err); then
		say "✗" "$arch 실행 실패: $(cat /tmp/vr-err)"; fail=1; continue
	fi
	case "$got" in
	*"$want"*) : ;;
	*) say "✗" "$arch 가 보고한 버전 [$got] 에 [$want] 가 없다"; fail=1; continue ;;
	esac
	# ②-b **바이너리 자신이 말하는 아키텍처를 본다.**
	#
	# `docker run --platform` 은 **이미지**를 고를 뿐이다. 프로세스는 호스트
	# 커널이 exec 하므로, 네이티브 아키텍처 바이너리는 어떤 --platform 아래서도
	# 그냥 돈다 — 즉 실행이 성공했다는 사실만으로는 "대상 아키텍처에서 돌았다"
	# 가 증명되지 않는다. 바이너리에게 직접 물어야 한다.
	if [ "$arch" = "$host_arch" ]; then
		path=네이티브
		n_native=$((n_native + 1))
	else
		path=에뮬레이션
		n_emul=$((n_emul + 1))
	fi
	case "$got" in
	*"linux/$arch"*) say "✓" "$arch 실행 ($path) → $got" ;;
	*) say "✗" "$arch 산출물이 [$got] 을 보고했다 — linux/$arch 가 아니다"; fail=1 ;;
	esac
done

# **두 경로가 모두 실측됐는지 센다** (GAP-08).
#
# 하나로 몰리면 이 실행은 한 경로만 본 것이다. 에뮬레이션 쪽이 0 이면
# `exec format error` 부류를 애초에 겪을 수 없는 실행이었고, 그 초록은
# 「검증했다」가 아니라 「그 실패 방식을 지나지 않았다」는 뜻이다.
if [ "$fail" -eq 0 ]; then
	if [ "$n_native" -eq 0 ]; then
		say "✗" "네이티브로 돈 산출물이 없다 (호스트 $host_arch) — 한 경로만 봤다"
		fail=1
	fi
	if [ "$n_emul" -eq 0 ]; then
		say "✗" "에뮬레이션으로 돈 산출물이 없다 (호스트 $host_arch) — 이 기계는 exec format error 부류를 겪을 수 없다"
		fail=1
	fi
fi

[ "$fail" -eq 0 ] || { echo "release 검증 실패"; exit 1; }
echo "release 검증 ok  $want  (네이티브 $n_native · 에뮬레이션 $n_emul · 호스트 $host_arch)"
