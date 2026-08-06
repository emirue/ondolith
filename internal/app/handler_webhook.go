package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/emirue/ondolith/internal/commerce"
)

// maxWebhookBody bounds the request before anything reads it. 서명 검증 전에
// 파싱하지 않는다는 규칙(D19 P-905)은 크기 상한이 있어야 의미가 있다 — 없으면
// 파싱 전에 이미 메모리를 다 쓴다.
const maxWebhookBody = 1 << 20 // 1 MiB

// webhookDeps is what P-905 needs. shopDeps 를 통째로 받지 않는다 — 이 경로는
// 세션도 액터도 없고, 그 사실이 타입에 보여야 한다.
type webhookDeps struct {
	store   *commerce.Store
	gateway func() commerce.Gateway
	pgName  func() string
	log     *slog.Logger
}

// webhookMux is P-905's **별도 서브트리**다 (D15 SC-8 1항).
//
// **`CrossOriginProtection` 이 이 요청을 통과시키는 것은 우연이다.** 그 미들
// 웨어는 브라우저가 붙이는 `Origin`·`Sec-Fetch-Site` 를 보고 판단하는데, PG 의
// 서버는 브라우저가 아니라 그 헤더를 붙이지 않는다 — 즉 "교차 출처가 아니어서"
// 통과하는 것이 아니라 **판단할 근거가 없어서** 통과한다. 설계된 보호가 아닌
// 것에 기대지 않기 위해 이 경로를 본 트리 밖에 둔다. 여기에는 세션도 CSRF 도
// 액터도 붙지 않고, 붙지 않는다는 것이 이 함수가 존재하는 이유다.
func webhookMux(d webhookDeps) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhooks/payment/{pg}", d.receive)
	return mux
}

// receive is P-905.
//
// **수신과 처리를 분리한다** (D19 P-905): 기록이 끝나면 곧바로 200 을 돌려주고
// 대조는 그 뒤에 한다. 처리 결과로 응답 코드를 가르면 우리 쪽 오류가 PG 의
// 재전송을 부르고, 재전송이 다시 같은 오류를 낸다.
func (d webhookDeps) receive(w http.ResponseWriter, r *http.Request) {
	pg := r.PathValue("pg")
	// 등록된 어댑터만. 목록 밖은 404 다 — 어떤 PG 를 쓰는지 알려주지 않는다.
	//
	// **결제사를 고르지 않았으면 전부 404 다** (A-209 의 「사용 안 함」).
	//
	// 빈 설정을 따로 검사하지 않는다: 경로 패턴이 빈 세그먼트를 매치하지
	// 않으므로 `pg` 는 항상 비어 있지 않고, 그러면 `pg != ""` 가 늘 참이라
	// 이 비교만으로 전부 막힌다. 따로 둔 검사는 무는 것이 없어 지웠다.
	if pg != d.pgName() {
		http.NotFound(w, r)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBody))
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// 서명·형태 검증. **실패는 조용히 버린다** — 사유를 응답에 담지 않는다
	// (D15 SC-8 2항). 어디가 틀렸는지 알려주면 그것이 곧 위조 안내서다.
	ev, err := d.gateway().VerifyWebhook(r.Context(), body)
	if err != nil {
		d.log.Warn("웹훅 검증 실패", "pg", pg, "remote", r.RemoteAddr,
			"content_type", r.Header.Get("Content-Type"), "bytes", len(body))
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// **멱등의 근거는 유니크 인덱스다** (FR-610). 조회로 판단하면 동시에 도착한
	// 재전송 두 건이 둘 다 "없다" 를 읽고 함께 통과한다.
	id, err := d.store.RecordWebhook(r.Context(), pg, ev)
	if errors.Is(err, commerce.ErrWebhookDuplicate) {
		d.log.Debug("웹훅 재전송", "pg", pg, "event", ev.EventID)
		w.WriteHeader(http.StatusOK)
		return
	}
	if err != nil {
		// 기록조차 못 했다. 이것만은 재전송이 의미가 있다.
		d.log.Error("웹훅 기록", "pg", pg, "event", ev.EventID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// 수신 성공. 여기서 응답이 끝난다.
	w.WriteHeader(http.StatusOK)

	// 처리는 요청 밖이다. r.Context() 는 응답과 함께 취소되므로 쓸 수 없다.
	//
	// 프로세스가 여기서 죽으면 그 행은 `수신` 으로 남는다 — 그것이 A-603 이
	// 보여주는 집합이고, D50 이 자동 재처리를 두지 않기로 한 자리다.
	go func() {
		if err := d.store.ProcessWebhook(context.WithoutCancel(r.Context()), id, ev); err != nil {
			d.log.Error("웹훅 처리", "pg", pg, "event", ev.EventID, "err", err)
		}
	}()
}
