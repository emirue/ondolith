package commerce

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"time"

	"github.com/emirue/ondolith/internal/commerce/commerceq"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	// ErrWebhookDuplicate 는 이미 받은 이벤트라는 뜻이다. **오류가 아니라 정상
	// 응답의 근거다** — 재전송에 멱등이어야 하므로 200 으로 답한다 (FR-610).
	ErrWebhookDuplicate = errors.New("commerce: 이미 받은 웹훅 이벤트입니다")
	// ErrWebhookUnmatched 는 알림이 우리 결제를 가리키지 않는다는 뜻이다 — 그 번호의
	// 주문이 없거나, 살아 있는 결제가 없거나, 결제키·secret 이 승인 때 받은 것과 다르다.
	// P-905 의 「출처 검증 실패」다: 400 이고 아무것도 기록하지 않는다.
	ErrWebhookUnmatched = errors.New("commerce: 우리 결제와 맞지 않는 웹훅입니다")
)

// RecordWebhook writes the event and returns its row id.
//
// **우리 결제와 맞는 알림만 기록한다** (matchWebhook). 이 주소는 공개이고 토스는
// 결제 상태 알림에 서명하지 않으므로, 형태만 보고 기록하면 아무나 주문번호와
// 결제키를 지어내 요청마다 원문(최대 64KiB)을 쌓을 수 있었다. 모든 기록이 이 함수를
// 지나므로 대조도 여기서 한다.
//
// **중복 판단은 애플리케이션 조회가 아니라 유니크 인덱스가 한다.** `SELECT` 로 먼저
// 봐서는 동시에 도착한 재전송 두 건이 함께 통과한다 — 둘 다 "없다" 를 읽기
// 때문이다. 여기서 기대하는 실패는 23505 이고, 그것이 곧 멱등이다.
//
// 주문 연결은 **우리가 발급한 order_no 로만** 찾는다. 페이로드의 내부 PK 는
// 형식상 있어도 읽지 않는다 (D19 P-905 받지 않는 필드).
func (s *Store) RecordWebhook(ctx context.Context, pg string, ev *WebhookEvent) (string, error) {
	if err := s.matchWebhook(ctx, ev); err != nil {
		return "", err
	}
	id, err := s.q.RecordWebhook(ctx, commerceq.RecordWebhookParams{
		Pg: pg, EventID: ev.EventID, OrderNo: ev.OrderNo, Payload: ev.Raw})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return "", ErrWebhookDuplicate
	}
	if err != nil {
		return "", err
	}
	return id, nil
}

// matchWebhook reports whether ev names one of our payments. 결제 상태 알림은
// paymentKey 로, 입금 알림(paymentKey 가 없다)은 secret 으로 그 결제에 묶인다. 어느
// 쪽이든 secret 이 오가면 같아야 한다 — processWebhook 과 같은 규칙을 기록 앞에 둔다.
// 상수 시간 비교다: `!=` 는 앞에서부터 비교하다 멈추므로 한 글자씩 맞춰 볼 수 있다.
func (s *Store) matchWebhook(ctx context.Context, ev *WebhookEvent) error {
	o, err := s.q.OrderForWebhook(ctx, ev.OrderNo)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrWebhookUnmatched
	}
	if err != nil {
		return err
	}
	p, err := s.q.LivePayment(ctx, o.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrWebhookUnmatched
	}
	if err != nil {
		return err
	}
	same := func(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }
	switch {
	case ev.PaymentKey == "" && ev.Secret == "":
		return ErrWebhookUnmatched // 무엇도 이 알림을 그 결제에 묶지 않는다
	case ev.PaymentKey != "" && !same(ev.PaymentKey, p.PaymentKey):
		return ErrWebhookUnmatched
	case (ev.Secret != "" || p.Secret != "") && !same(ev.Secret, p.Secret):
		return ErrWebhookUnmatched
	}
	return nil
}

// ProcessWebhook is the half that runs after the 200 went out.
//
// **웹훅 본문을 진실로 채택하지 않는다** (D19 P-905 받지 않는 필드): 금액은
// 저장된 주문 금액과 **대조만** 하고, 상태 문자열은 그대로 넣지 않는다.
// 외부가 `구매확정` 을 지정할 수 있으면 취소·환불 경로가 임의로 닫힌다.
//
// 어떤 결과든 `webhook_events.status` 에 남긴다. 남기지 않으면 A-603 이
// 「처리되지 않은 웹훅」을 보여줄 수 없고, D50 이 자동 재처리를 두지 않기로
// 한 이상 사람이 보는 그 목록이 유일한 복구 수단이다.
func (s *Store) ProcessWebhook(ctx context.Context, gw Gateway, eventID string, ev *WebhookEvent) error {
	err := s.processWebhook(ctx, gw, ev)
	status, msg := "처리완료", ""
	if err != nil {
		status, msg = "실패", err.Error()
		if len(msg) > 500 {
			msg = msg[:500]
		}
	}
	if uerr := s.q.FinishWebhook(ctx, commerceq.FinishWebhookParams{
		ID: eventID, Status: status, Error: msg}); uerr != nil {
		return errors.Join(err, uerr)
	}
	return err
}

func (s *Store) processWebhook(ctx context.Context, gw Gateway, ev *WebhookEvent) error {
	o, err := s.q.OrderForWebhook(ctx, ev.OrderNo)
	if errors.Is(err, pgx.ErrNoRows) {
		// 기록 전 대조(matchWebhook)가 이 경우를 막으므로 사실상 닿지 않는다.
		return fmt.Errorf("%w: 주문 %q", ErrNotFound, ev.OrderNo)
	}
	if err != nil {
		return err
	}
	orderID, orderStatus, total := o.ID, o.Status, int(o.TotalAmount)

	// 살아 있는 주문결제 행 하나. '승인' 만 찾으면 가상계좌(입금 전 '대기')의
	// 입금 알림이 「승인된 결제가 없다」로 떨어져 영영 결제완료가 되지 않는다.
	p, err := s.q.LivePayment(ctx, orderID)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: 살아 있는 결제가 없다", ErrNoPayment)
	}
	if err != nil {
		return err
	}
	paymentID, paymentKey, payStatus, stored := p.ID, p.PaymentKey, p.Status, p.Secret
	// **secret 대조.** 승인 응답이 준 값과 같아야 한다 (D50). 상수 시간이다 —
	// `!=` 는 앞에서부터 비교하다 멈추므로 한 글자씩 맞춰 볼 수 있다. 어느 한
	// 쪽에만 있는 것도 불일치다: 우리 쪽에 secret 이 있는데 알림이 비어 오면
	// 그 알림은 우리 결제에 대한 것이 아니다.
	if (stored != "" || ev.Secret != "") &&
		subtle.ConstantTimeCompare([]byte(ev.Secret), []byte(stored)) != 1 {
		return errors.New("commerce: 웹훅 secret 이 승인 응답과 다릅니다")
	}

	// 금액은 **대조만** 한다. 웹훅이 알려준 값을 저장하지 않는다 (FR-610).
	if ev.Amount > 0 && ev.Amount != total {
		return fmt.Errorf("%w: 주문 %d, 웹훅 %d", ErrAmountMismatch, total, ev.Amount)
	}
	if payStatus != string(PaymentPending) {
		return nil
	}

	// **입금 확인.** 진실은 웹훅 본문이 아니라 조회 API 다 (D50 — secret 은
	// 아는 사람이 흉내낼 수 있고, 그 사람이 곧 우리에게 입금을 통보할 수
	// 있다). 웹훅은 「지금 물어볼 때」를 알려줄 뿐이고, PG 가 DONE 과 우리
	// 금액을 답해야만 입금대기 → 결제완료 로 옮긴다 (D14: P-905).
	if gw == nil {
		return errors.New("commerce: 게이트웨이 없이 입금을 확인할 수 없습니다")
	}
	got, err := gw.Get(ctx, paymentKey)
	if err != nil {
		return err
	}
	if got == nil || got.Status != PaymentApproved {
		return nil // 아직 입금 전이다. 다음 알림이 다시 묻는다.
	}
	// 조회한 결제가 이 주문의 것인지도 본다 — 승인·결과 불명 분기와 같은 대조다.
	if got.OrderNo != ev.OrderNo {
		return fmt.Errorf("commerce: 조회한 결제의 주문 %q 가 알림의 주문 %q 와 다릅니다", got.OrderNo, ev.OrderNo)
	}
	if err := VerifyAmount(total, got.Amount); err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := s.moveOrder(ctx, tx, orderID, Status(orderStatus), StatusPaid, "P-905"); err != nil {
		return err
	}
	n, err := s.q.WithTx(tx).ApprovePendingPayment(ctx, commerceq.ApprovePendingPaymentParams{
		ID: paymentID, RawResponse: MaskCardFields(got.Raw)})
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%w: 확인하는 사이 결제 상태가 바뀌었습니다", ErrTransitionNotAllowed)
	}
	return tx.Commit(ctx)
}

// ReconcileBudget bounds one A-508 request's gateway time; reconcileCallTimeout
// bounds one lookup. 둘의 합이 CloudFront 기본 응답 시한(30초, D72 4절) 안이다 —
// 넘기면 CloudFront 가 GET 을 끊고 다시 보내 대사가 처음부터 다시 돈다. 40초였던
// 앞 판은 WriteTimeout(60초)만 봤다.
const (
	ReconcileBudget      = 20 * time.Second
	reconcileCallTimeout = 5 * time.Second
)

// WebhookPayloadHead 는 A-603 이 읽는 원문의 앞부분 길이(문자)다 — queries/webhook.sql
// WebhookHistory 의 left(…, 4096) 과 같은 값이다. 한 쪽 100행을 통째로 읽으면 큰
// 원문 몇 개로 화면 하나가 수 MiB 를 메모리에 올린다.
const WebhookPayloadHead = 4096

// WebhookRow is one row of A-603.
type WebhookRow struct {
	ID      string
	PG      string
	EventID string
	OrderNo string
	Status  string
	Payload string
	// PayloadCut 은 Payload 가 앞 WebhookPayloadHead 자에서 잘렸다는 뜻이다.
	PayloadCut bool
	Error      string
	CreatedAt  time.Time
}

// WebhookHistory is A-603's list. **`수신` 이 먼저 온다** — 처리되지 않은 채
// 남은 행이 이 화면의 목적이고, D50 이 자동 재처리를 두지 않기로 했으므로
// 사람이 그것을 봐야 한다.
//
// since·until 은 수신 시각의 범위다 (`since ≤ 수신 < until`, D19 0.6). nil 이면
// 그쪽 끝이 열려 있다.
func (s *Store) WebhookHistory(ctx context.Context, since, until *time.Time,
	limit, offset int) ([]WebhookRow, error) {

	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := s.q.WebhookHistory(ctx, commerceq.WebhookHistoryParams{
		Since: since, Until: until, Limit: int32(limit), Offset: int32(offset)})
	if err != nil {
		return nil, err
	}
	var out []WebhookRow
	for _, r := range rows {
		out = append(out, WebhookRow{ID: r.ID, PG: r.Pg, EventID: r.EventID, OrderNo: r.OrderNo,
			Status: r.Status, Payload: r.Payload, PayloadCut: r.PayloadCut, Error: r.Error, CreatedAt: r.CreatedAt})
	}
	return out, nil
}

// ReconcileRow is one line of A-508: 우리 기록과 PG 조회의 대조 결과.
type ReconcileRow struct {
	PaymentID string
	// PaymentKey 는 **PG 가 발급한 키**다. 조회 API 는 우리 PK 를 모른다 —
	// 우리 id 를 넘기면 언제나 "기록 없음" 이 나오고, 그것이 곧 "대사가
	// 늘 불일치를 보고하는" 화면이 된다.
	PaymentKey string
	OrderNo    string
	PG         string
	Kind       string
	// Ours 는 우리가 승인으로 기록한 금액이다. Theirs 는 조회 API 가 말한 것.
	OurStatus   string
	OurAmount   int
	OurRefunded int
	TheirStatus string
	TheirAmount int
	// Diff 는 사람이 읽을 차이 설명이다. 비면 일치다.
	Diff      string
	CreatedAt time.Time
}

// PaymentsToReconcile lists the payments in a window.
//
// **`대기` 로 남은 행이 가장 위험하다** (D50): 승인 API 는 성공했는데 우리
// 트랜잭션이 실패한 경우가 그 모습이고, 그 돈은 나갔는데 주문은 결제대기다.
// 그래서 기간 필터와 무관하게 먼저 온다.
func (s *Store) PaymentsToReconcile(ctx context.Context, since, until time.Time) ([]ReconcileRow, error) {
	rows, err := s.q.PaymentsToReconcile(ctx, commerceq.PaymentsToReconcileParams{Since: since, Until: until})
	if err != nil {
		return nil, err
	}
	var out []ReconcileRow
	for _, r := range rows {
		out = append(out, ReconcileRow{PaymentID: r.ID, PaymentKey: r.PaymentKey, OrderNo: r.OrderNo,
			PG: r.Pg, Kind: r.Kind, OurStatus: r.Status, OurAmount: int(r.ApprovedAmount),
			OurRefunded: int(r.RefundedAmount), CreatedAt: r.CreatedAt})
	}
	return out, nil
}

// Reconcile asks the gateway what it thinks and reports the differences.
//
// **금액은 폼에서 받지 않는다** (D19 A-508 받지 않는 필드). 근거는 조회
// 결과이고, 폼에서 받으면 대사가 아니라 수기 조작이다.
//
// **자동으로 고치지 않는다.** 조회 결과를 우리 행에 그대로 쓰면 PG 의 일시적
// 응답 하나가 우리 장부를 바꾼다 — 사람이 보고 A-506 으로 옮긴다 (D50).
// **PG 를 끈 상태에서도 이 함수가 불린다.** `pgAdapterFor` 는 등록되지 않은
// 제공자에 `("", nil)` 을 돌려주므로(handler_shop.go), 운영자가 PG 를 「사용 안
// 함」으로 바꾼 뒤 예전 `payments` 행이 조회 구간에 남아 있으면 gw 가 nil 로
// 들어온다. 호출자 쪽 `if d.Gateway != nil` 은 **클로저**를 보는데 그 클로저는
// 절대 nil 이 아니었다 — nil 인 것은 반환값이고, 그래서 가드가 한 번도 걸리지
// 않은 채 `gw.Get` 이 nil 인터페이스에서 패닉했다. 가드는 호출자마다가 아니라
// 모든 호출자가 지나는 여기 하나로 둔다.
//
// 조회하지 않은 것을 「일치」로 그리지 않는다 — 대사의 목적은 우리 장부와 PG 가
// 다른 자리를 찾는 것이고, 묻지 않은 것은 같다는 뜻이 아니다.
//
// 조회 하나에 5초, 전체에 ReconcileBudget 이다. 요청 하나가 최대 500건을 순서대로
// 묻는데, 시한이 없으면 PG 가 느린 날 WriteTimeout(60초)에 브라우저가 먼저 끊기고
// 관리자는 빈 화면을 본다. 예산을 넘긴 행은 「조회하지 않았다」로 표시한다 —
// 못 본 것을 본 것처럼 그리지 않는다.
func (s *Store) Reconcile(ctx context.Context, gw Gateway, rows []ReconcileRow) []ReconcileRow {
	out := make([]ReconcileRow, 0, len(rows))
	deadline := time.Now().Add(ReconcileBudget)
	for _, r := range rows {
		if gw == nil {
			r.Diff = "PG 가 설정되어 있지 않아 조회하지 않았다"
			out = append(out, r)
			continue
		}
		if time.Now().After(deadline) {
			r.Diff = "시간 예산을 넘겨 조회하지 않았다 — 기간을 줄여 다시 보세요"
			out = append(out, r)
			continue
		}
		callCtx, cancel := context.WithTimeout(ctx, reconcileCallTimeout)
		got, err := gw.Get(callCtx, r.PaymentKey)
		cancel()
		switch {
		case err != nil:
			r.Diff = "조회 실패: " + err.Error()
		case got == nil:
			r.Diff = "PG 에 기록이 없다"
		default:
			r.TheirStatus, r.TheirAmount = string(got.Status), got.Amount
			switch {
			case r.OurStatus == "대기" && got.Status == PaymentApproved:
				// **가장 위험한 상태다** (D50). 돈은 나갔는데 주문은 결제대기다.
				r.Diff = "PG 는 승인, 우리는 대기 — 돈이 나갔는데 주문에 반영되지 않았다"
			case got.Status == PaymentCancelled:
				// 전액·부분 취소. 우리 쪽 상태는 여전히 '승인' 이고 환불누적액이
				// 그만큼 올라 있어야 한다 — PG 잔고와 우리 잔여가 같으면 일치다.
				switch {
				case r.OurStatus != string(PaymentApproved):
					r.Diff = "상태 불일치"
				case got.Balance != r.OurAmount-r.OurRefunded:
					r.Diff = "환불액 불일치"
				}
			case string(got.Status) != r.OurStatus:
				r.Diff = "상태 불일치"
			case got.Amount != r.OurAmount:
				r.Diff = "금액 불일치"
			}
		}
		out = append(out, r)
	}
	return out
}

// ErrNothingToResolve 는 A-508 이 PG 결과로 반영할 것이 없다는 뜻이다 — 결제가
// 이미 닫혔거나, 주문결제가 아니거나, PG 도 아직 승인·실패를 말하지 않는다.
var ErrNothingToResolve = errors.New("commerce: PG 결과로 반영할 것이 없습니다")

// ResolvePendingPayment closes a '대기' order payment with what the gateway says
// (D19 A-508 「대사 결과로 상태 변경」). 「PG 는 승인, 우리는 대기」를 앱 안에서
// 닫는 유일한 길이다 — 결제대기 → 결제완료 는 그 밖에 P-408 만 할 수 있다.
//
// **근거는 조회 결과뿐이다** (D19 A-508 거부 조건 「PG 조회 실패 상태에서 추정
// 반영」). 승인이면 주문을 결제완료로, 실패면 결제 행만 '실패' 로 — 그러면 10분
// 만료가 주문과 재고를 놓는다. 그 밖(대기·취소)은 ErrNothingToResolve 다.
// 조회의 '대기' 는 READY 와 가상계좌 발급을 가르지 못해 입금대기로도 옮기지 않는다.
func (s *Store) ResolvePendingPayment(ctx context.Context, gw Gateway, paymentID string) (Status, error) {
	ctx, cancel := moneyCtx(ctx) // 조회 뒤 기록까지 끝낸다 (payment.go)
	defer cancel()
	p, err := s.q.PaymentForResolve(ctx, paymentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if p.Status != string(PaymentPending) || p.Kind != "주문결제" {
		return "", fmt.Errorf("%w: %s %s", ErrNothingToResolve, p.Kind, p.Status)
	}
	if gw == nil {
		return "", errors.New("commerce: PG 가 설정되어 있지 않아 조회할 수 없습니다")
	}
	callCtx, callCancel := context.WithTimeout(ctx, reconcileCallTimeout)
	got, err := gw.Get(callCtx, p.PaymentKey)
	callCancel()
	if err != nil {
		return "", err
	}
	if got == nil {
		return "", errors.New("commerce: PG 에 기록이 없습니다")
	}
	// 결제키만으로 묻는 조회다. 다른 주문의 답이면 그 결제로 이 주문을 닫는다.
	if got.OrderNo != p.OrderNo {
		return "", fmt.Errorf("commerce: 조회 응답이 다른 주문(%s)의 것입니다", got.OrderNo)
	}
	if got.Status != PaymentApproved && got.Status != PaymentFailed {
		return "", fmt.Errorf("%w: PG 상태 %s", ErrNothingToResolve, got.Status)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	q := s.q.WithTx(tx)
	// 조회하는 사이 웹훅이나 다른 관리자가 닫았을 수 있다 — 잠그고 다시 본다.
	p, err = q.PaymentForResolve(ctx, paymentID)
	if err != nil {
		return "", err
	}
	if p.Status != string(PaymentPending) {
		return "", fmt.Errorf("%w: 조회하는 사이 %s", ErrNothingToResolve, p.Status)
	}
	if got.Status == PaymentFailed {
		if err := q.DeclinePayment(ctx, commerceq.DeclinePaymentParams{
			ID: paymentID, RawResponse: MaskCardFields(got.Raw)}); err != nil {
			return "", err
		}
		// 입금대기 주문은 여기서 놓는다. 결제대기는 10분 만료가 집지만, 입금대기는 어느
		// 만료도 '실패' 결제를 집지 않아 재고를 잡은 채 남는다 (ExpireDepositOrders).
		st, err := q.LockOrderStatus(ctx, p.OrderID)
		if err != nil {
			return "", err
		}
		if Status(st) == StatusDepositPending {
			if err := s.releaseUnpaid(ctx, tx, p.OrderID, StatusDepositPending); err != nil {
				return "", err
			}
		}
		return "", tx.Commit(ctx)
	}
	if err := VerifyAmount(int(p.TotalAmount), got.Amount); err != nil {
		return "", err
	}
	if err := s.moveOrder(ctx, tx, p.OrderID, Status(p.OrderStatus), StatusPaid, "A-508"); err != nil {
		return "", err
	}
	if _, err := q.ApprovePendingPayment(ctx, commerceq.ApprovePendingPaymentParams{
		ID: paymentID, RawResponse: MaskCardFields(got.Raw)}); err != nil {
		return "", err
	}
	return StatusPaid, tx.Commit(ctx)
}
