#!/bin/sh
# report-skips.sh — `make check` 의 테스트 단계.
#
# go test 를 돌리고, **DB 단언이 실행되지 않았으면 그 사실을 알린다.**
# ONDOLITH_TEST_DSN 없이 돌면 결제·환불·반품의 단언이 하나도 실행되지 않는데,
# 그때도 "check ok" 만 보이면 게이트가 전부 돌았다고 읽힌다.
#
# 실패로 만들지는 않는다 — 네트워크 없는 환경에서 게이트가 깨지면 안 되고,
# 실제 실행을 강제하는 것은 `make test-integration` 이다.
#
# **SKIP 줄을 세지 않고 DSN 을 본다.** 캐시된 패키지는 "ok (cached)" 만 찍고
# SKIP 줄을 다시 내놓지 않아서, 줄을 세는 방식은 두 번째 실행부터 조용해진다 —
# 경고가 필요한 바로 그 상황에서 사라진다.
set -eu

go test -race -p 1 ./... || status=$?
status=${status:-0}

if [ -z "${ONDOLITH_TEST_DSN:-}" ]; then
	echo
	echo "  ⚠ ONDOLITH_TEST_DSN 없음 — DB 를 쓰는 단언은 실행되지 않았다."
	echo "    돈이 걸린 경로(결제·환불·반품)는 여기서 검증되지 않았다. make test-integration 으로 확인하라."
fi
exit "$status"
