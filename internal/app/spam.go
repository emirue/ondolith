package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/emirue/ondolith/internal/auth"
)

// honeypotField is the box people never see (FR-216, 테마의 partials/spam.html).
// 화면 밖에 두고 탭 순서·보조기기에서도 뺀다 — 채워 온 것은 폼을 읽지 않고 칸마다
// 무언가를 넣는 프로그램이다. **하이픈이 든 이름이다:** 커스텀 필드 키는
// `^[a-z][a-z0-9_]*$` 라(D30) 운영자가 「website」 항목을 만들어도 겹치지 않는다.
const honeypotField = "website-url"

const spamRejected = "자동 입력 방지 확인을 통과하지 못했습니다. 다시 시도하세요."

// spamGuard is the check P-103 · P-104 · P-205 · P-208 run before saving.
type spamGuard struct {
	// keys are A-201's Turnstile site key and secret. Both empty is the default:
	// no widget, no outside call (FR-216 「키가 없으면 꺼져 있다」).
	keys   func() (siteKey, secret string)
	verify func(ctx context.Context, secret, token, remoteIP string) error
	log    *slog.Logger
}

// check returns 0 when the submission may go on, else the status and sentence.
// The form must already be parsed.
func (g spamGuard) check(r *http.Request) (int, string) {
	if r.PostFormValue(honeypotField) != "" {
		return http.StatusUnprocessableEntity, spamRejected
	}
	if g.keys == nil {
		return 0, ""
	}
	// 사이트 키 없이는 위젯이 그려지지 않는다 — 그때 검사를 켜면 모든 제출이 막힌다.
	site, secret := g.keys()
	if site == "" || secret == "" {
		return 0, ""
	}
	err := g.verify(r.Context(), secret, r.PostFormValue(auth.TurnstileField), clientIP(r))
	switch {
	case err == nil:
		return 0, ""
	case errors.Is(err, auth.ErrTurnstileRejected):
		return http.StatusUnprocessableEntity, spamRejected
	}
	// 틀린 비밀키·Cloudflare 불통 — 바깥 시스템 실패라 502 다(D19 0.3). 방문자 탓이 아니므로 「자동 입력」 이라고 하지 않고,
	// 운영자가 알 수 있게 로그에 남긴다. 저장은 하지 않는다(닫힌 쪽으로 실패).
	if g.log != nil {
		g.log.Error("Turnstile 확인 실패 — A-201 의 키를 확인하세요", "err", err)
	}
	return http.StatusBadGateway, "자동 입력 방지 확인을 지금 할 수 없습니다. 잠시 후 다시 시도하세요."
}
