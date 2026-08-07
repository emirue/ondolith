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
	case "$got" in
	*"linux/$arch"*) say "✓" "$arch 실행 → $got" ;;
	*) say "✗" "$arch 산출물이 [$got] 을 보고했다 — linux/$arch 가 아니다"; fail=1 ;;
	esac
done

[ "$fail" -eq 0 ] || { echo "release 검증 실패"; exit 1; }
echo "release 검증 ok  $want"
