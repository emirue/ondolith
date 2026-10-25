package commerce

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/emirue/ondolith/internal/commerce/commerceq"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	// ErrRefundExceeds 는 DB 의 `CHECK (refunded_amount <= approved_amount)` 를
	// 옮긴 이름이다. 애플리케이션이 먼저 합산해 보는 것으로는 동시 두 건을
	// 막지 못한다 — 둘이 같은 누적액을 읽고 각자 통과한다.
	ErrRefundExceeds = errors.New("commerce: 환불 누적액이 결제 금액을 넘습니다")
	// ErrRefundDuplicate 는 같은 요청 키가 이미 있다는 뜻이다. 새로고침 한 번이
	// 이중 환불이 되지 않게 하는 것이 이 키의 존재 이유다.
	ErrRefundDuplicate = errors.New("commerce: 이미 접수된 환불 요청입니다")
	// ErrNoPayment 는 승인된 결제가 없다는 뜻이다.
	ErrNoPayment = errors.New("commerce: 승인된 결제가 없습니다")
)

// Refund is one row of refunds, as P-508 draws it.
type Refund struct {
	ID        string
	Status    string
	Requester string
	Amount    int
	Reason    string
	CreatedAt time.Time
}

// RefundLine is one line of a partial refund request: which item, how many.
//
// **금액이 없다.** FR-617·FR-625 가 "환불액은 서버가 스냅샷에서 계산한다,
// 요청 금액을 믿지 않는다" 라고 적었고, 금액 필드가 있으면 그 규칙은 지킬
// 방법이 없다 — 값이 오는 순간 누군가는 그것을 쓴다.
type RefundLine struct {
	OrderItemID string
	Quantity    int
}

// RequestRefund records a refund for the given items and reserves the amount.
//
// 금액은 **서버가 계산한다**: 품목의 할인후 금액에서 소진 수량 기준으로 몫을
// 잘라낸다 (RefundableAmount). 여러 개 산 것 중 하나만 취소하는 경우가
// 정상이고, 그때 할인이 붙어 있으면 단가 × 수량은 틀린 답이다.
//
// **선점이다.** payments.refunded_amount 를 요청 시점에 올린다 (D30) — 승인
// 시점에 올리면 요청 두 건이 각각 "아직 여유가 있다" 를 보고 둘 다 접수된다.
// `거부` 로 전이할 때 같은 트랜잭션에서 되돌린다.
//
// 상한을 지키는 것은 **DB 의 CHECK** 다. 여기서 미리 합산해 보는 코드는 두지
// 않는다 — 동시 두 건이 같은 누적액을 읽고 각자 통과하므로 그 검사는 안전을
// 주지 않으면서 안전해 보이게 만든다.
func (s *Store) RequestRefund(ctx context.Context, orderNo string, lines []RefundLine,
	requester, reason, requestKey string) (id string, amount int, err error) {

	if len(lines) == 0 {
		return "", 0, errors.New("commerce: 환불할 품목을 고르세요")
	}
	if requestKey == "" {
		return "", 0, errors.New("commerce: 요청 키가 필요합니다")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", 0, err
	}
	defer tx.Rollback(ctx)
	q := s.q.WithTx(tx)

	// 같은 폼의 재전송이면 첫 결과다. 남은 수량을 다 고른 요청이면 수량 검사가 먼저 걸려, 접수된
	// 요청이 「남은 수량을 넘습니다」로 보였다.
	if taken, err := q.RefundKeyTaken(ctx, requestKey); err != nil {
		return "", 0, err
	} else if taken {
		return "", 0, ErrRefundDuplicate
	}
	ids, err := q.ApprovedPaymentForOrderNo(ctx, orderNo)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", 0, ErrNoPayment
	}
	if err != nil {
		return "", 0, err
	}
	orderID, paymentID := ids.OrderID, ids.PaymentID

	// 품목을 잠근 채 읽는다. 소진 수량을 읽고 나서 늘리므로, 잠그지 않으면 두
	// 요청이 같은 잔량을 보고 각자 통과한다 — DB CHECK 가 그중 하나를 잡지만
	// 그때 나오는 것은 제약 위반이지 "남은 수량이 없습니다" 가 아니다.
	//
	// **잠금 순서는 order_item_id 오름차순이다** (AdjustStock 과 같은 이유).
	// 폼이 준 순서대로 잠그면 `item_id=A&item_id=B` 와 `item_id=B&item_id=A`
	// 두 요청이 서로의 행을 기다린다 — 순서를 요청자가 정하는 교착이다.
	// 롤백되므로 돈은 안전하지만, 운영자에게 가는 것은 원인 없는 500 이다.
	lines = append([]RefundLine(nil), lines...)
	for i := 1; i < len(lines); i++ {
		for j := i; j > 0 && lines[j].OrderItemID < lines[j-1].OrderItemID; j-- {
			lines[j], lines[j-1] = lines[j-1], lines[j]
		}
	}
	for _, l := range lines {
		if l.Quantity < 1 {
			return "", 0, fmt.Errorf("%w: %d", ErrQuantityRange, l.Quantity)
		}
		it, err := q.LockOrderItemForRefund(ctx, commerceq.LockOrderItemForRefundParams{
			ID: l.OrderItemID, OrderID: orderID})
		if errors.Is(err, pgx.ErrNoRows) {
			// 다른 주문의 품목 ID 도 여기로 온다. order_id 술어가 그것을 막는다.
			return "", 0, ErrNotFound
		}
		if err != nil {
			return "", 0, err
		}
		// **처리 중인 반품·교환이 걸린 품목은 부분 환불로 소진하지 않는다.**
		//
		// 소진하면 그 반품의 정산이 수량 초과로 실패하는데, `반품수거` 에서
		// 나가는 화살표는 `환불` 하나뿐이라 (D14 5절) 그 건은 거부도 되돌리기도
		// 안 되고 멈춘다 — 물건은 이미 받았는데 돈을 줄 수 없는 상태다.
		//
		// 이 검사가 경합에 견디는 근거: 위에서 `order_items` 행을 FOR UPDATE 로
		// 잡았고, OpenReturn 도 같은 행을 `FOR UPDATE OF oi` 로 잡는다. 둘은
		// 서로를 기다리므로 "열린 반품 없음" 을 읽고 소진하는 사이에 반품이
		// 끼어들 수 없다.
		open, err := q.OpenReturnItemCount(ctx, l.OrderItemID)
		if err != nil {
			return "", 0, err
		}
		if open > 0 {
			return "", 0, ErrReturnInProgress
		}

		part, err := itemRefundable(ctx, q, l.OrderItemID, int(it.LineAmount), int(it.DiscountAmount),
			int(it.Quantity), int(it.SettledQuantity), l.Quantity)
		if err != nil {
			return "", 0, err
		}
		amount += part

		if err := q.SettleOrderItem(ctx, commerceq.SettleOrderItemParams{
			ID: l.OrderItemID, Quantity: int32(l.Quantity)}); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23514" {
				return "", 0, ErrRefundQuantity
			}
			return "", 0, err
		}
	}
	if amount <= 0 {
		// 할인이 커서 환불액이 0 이 되는 경우다. 0원 환불 행을 만들면 DB CHECK
		// (amount > 0) 가 막는데, 그것은 제약 위반이지 설명이 아니다.
		return "", 0, fmt.Errorf("%w: 환불할 금액이 없습니다", ErrPriceNegative)
	}

	// 선점. CHECK 가 상한을 지킨다.
	if err := q.ReserveRefundAmount(ctx, commerceq.ReserveRefundAmountParams{
		ID: paymentID, Amount: int32(amount)}); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23514" {
			return "", 0, ErrRefundExceeds
		}
		return "", 0, err
	}

	refundID, err := q.InsertRefund(ctx, commerceq.InsertRefundParams{
		OrderID: orderID, PaymentID: paymentID, Requester: requester,
		Amount: int32(amount), Reason: reason, RequestKey: requestKey})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return "", 0, ErrRefundDuplicate
	}
	if err != nil {
		return "", 0, err
	}

	// 무엇을 몇 개 환불하는지 남긴다 (refund_items). 금액만 남기면 나중에
	// "어느 품목이 소진됐나" 를 settled_quantity 로만 알게 되고, 건별 근거가
	// 사라진다.
	for _, l := range lines {
		if err := q.InsertRefundItem(ctx, commerceq.InsertRefundItemParams{
			RefundID: refundID, OrderItemID: l.OrderItemID, Quantity: int32(l.Quantity)}); err != nil {
			return "", 0, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return "", 0, err
	}
	return refundID, amount, nil
}

// itemRefundable is what returning n units of one order item pays back: 할인 몫을 나눈 값(RefundableAmount)에서
// 더 싼 조합 교환으로 이미 돌려준 차액을 뺀다(capByExchangeCredit). 부분 환불(RequestRefund)과 반품 정산
// (returnGross)이 이것을 지난다 — 한쪽에만 규칙을 넣으면 다른 쪽으로 이중 환불이 나간다.
func itemRefundable(ctx context.Context, q *commerceq.Queries, itemID string,
	lineAmount, discount, quantity, settled, n int) (int, error) {

	part, err := RefundableAmount(lineAmount, discount, quantity, settled, n)
	if err != nil {
		return 0, err
	}
	credit, err := q.ExchangeCreditForItem(ctx, itemID)
	if err != nil {
		return 0, err
	}
	return capByExchangeCredit(part, lineAmount, discount, quantity, settled, int(credit)), nil
}

// RejectRefund puts the reserved amount back.
//
// 같은 트랜잭션에서 되돌린다. 나누면 거부는 됐는데 선점이 남아 있는 상태가
// 생기고, 그 주문은 남은 금액만큼 환불받지 못한다 — 아무 오류도 나지 않으므로
// 사람이 숫자를 보고서야 안다.
func (s *Store) RejectRefund(ctx context.Context, refundID, reason string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := s.q.WithTx(tx)

	rejected, err := q.RejectRefund(ctx, commerceq.RejectRefundParams{ID: refundID, Reason: reason})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	// **잠금 순서: order_items(id 오름차순) → payments** (RequestRefund 와 같다).
	// 결제를 먼저 잡으면, 품목을 잡고 결제를 기다리는 환불 요청과 서로를
	// 기다린다 — 실측으로 교착이 났다.
	if _, err := q.LockOrderItems(ctx, rejected.OrderID); err != nil {
		return err
	}
	// 소진 수량을 되돌린다. 금액만 되돌리면 그 품목은 영영 다시 환불할 수
	// 없으면서 한도만 살아 있는 상태가 되고, 아무 오류도 나지 않는다.
	if err := q.UnsettleRefundItems(ctx, refundID); err != nil {
		return err
	}
	if err := q.ReleaseRefundAmount(ctx, commerceq.ReleaseRefundAmountParams{
		ID: rejected.PaymentID, Amount: rejected.Amount}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Refunds lists an order's requests, newest first (P-508).
func (s *Store) Refunds(ctx context.Context, orderID string) ([]Refund, error) {
	rows, err := s.q.Refunds(ctx, orderID)
	if err != nil {
		return nil, err
	}
	var out []Refund
	for _, r := range rows {
		out = append(out, Refund{ID: r.ID, Status: r.Status, Requester: r.Requester,
			Amount: int(r.Amount), Reason: r.Reason, CreatedAt: r.CreatedAt})
	}
	return out, nil
}

// RefundedTotal is what has been reserved or paid out so far.
func (s *Store) RefundedTotal(ctx context.Context, orderNo string) (approved, refunded int, err error) {
	r, err := s.q.RefundedTotal(ctx, orderNo)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, ErrNoPayment
	}
	return int(r.ApprovedAmount), int(r.RefundedAmount), err
}

// CancelOrder is P-506: cancel before dispatch, money back in full.
//
// 배송 **전**이라 물건이 움직이지 않았고 돌려줄 것은 돈뿐이라 구매자가 직접
// 일으킬 수 있다 (D14 5-1). 배송 후는 A-507 이 승인해야 한다 — 승인 없이 돈이
// 나가면 되돌릴 방법이 없다.
func (s *Store) CancelOrder(ctx context.Context, orderNo string, actor Actor,
	requestKey string) (refundID string, err error) {

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	q := s.q.WithTx(tx)

	o, err := lockOrder(ctx, q, orderNo)
	if err != nil {
		return "", err
	}
	orderID, status := o.ID, o.Status
	if err := CanTransition(Status(status), StatusCancelled, actor); err != nil {
		return "", err
	}

	if err := s.restockOrder(ctx, tx, orderID); err != nil {
		return "", err
	}

	if err := s.moveOrder(ctx, tx, orderID, Status(status), StatusCancelled, actor); err != nil {
		return "", err
	}

	// **잠금 순서: order_items(id 오름차순) → payments** (RequestRefund 와 같다).
	// 결제를 먼저 잡고 품목을 나중에 고치면, 품목을 잡고 결제를 기다리는 환불
	// 요청과 서로를 기다린다 — 실측으로 교착이 났다.
	if _, err := q.LockOrderItems(ctx, orderID); err != nil {
		return "", err
	}

	// 결제가 있었으면 전액 환불을 접수한다. 없으면(결제대기) 돌려줄 돈이 없다.
	p, err := q.LockApprovedPayment(ctx, orderID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "", tx.Commit(ctx)
	case err != nil:
		return "", err
	}
	paymentID, approved, alreadyRefunded := p.ID, int(p.ApprovedAmount), int(p.RefundedAmount)

	// 남은 수량을 전부 소진 처리해 두면, 취소된 주문에 다시 부분 환불을 넣는
	// 경로가 닫힌다 — 금액 한도만으로는 막히지 않는다.
	if err := q.SettleAllOrderItems(ctx, orderID); err != nil {
		return "", err
	}

	// **남은 몫만 돌려준다.** 부분 환불이 이미 나간 주문을 전액으로 취소하면
	// 같은 돈을 두 번 돌려주게 된다. 게다가 `refunded_amount` 에 **대입**하면
	// 앞선 선점이 지워져서 `CHECK (환불누적액 <= 승인금액)` 이 막지 못한다 —
	// 제약이 보는 값 자체가 사라지기 때문이다. 그래서 누적으로 더한다.
	remaining := approved - alreadyRefunded
	if remaining <= 0 {
		// 이미 전액이 선점됐다. 취소 상태와 수량 소진만 남기고 끝낸다.
		return "", tx.Commit(ctx)
	}
	if err := q.ReserveRefundAmount(ctx, commerceq.ReserveRefundAmountParams{
		ID: paymentID, Amount: int32(remaining)}); err != nil {
		return "", err
	}
	refundID, err = q.InsertCancelRefund(ctx, commerceq.InsertCancelRefundParams{
		OrderID: orderID, PaymentID: paymentID, Requester: requesterOf(actor),
		Amount: int32(remaining), RequestKey: requestKey})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return "", ErrRefundDuplicate
		}
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return refundID, nil
}

// restockOrder returns every item of the order to stock. 주문 생성이 차감했으므로
// 취소는 같은 만큼 푼다 — 풀지 않으면 재고가 조용히 잠긴다 (D14 「교환 재고」와
// 같은 이유). 배송 뒤 환불에는 쓰지 않는다 — 물건은 구매자에게 있다.
func (s *Store) restockOrder(ctx context.Context, tx pgx.Tx, orderID string) error {
	deltas, err := restockDeltas(ctx, tx, orderID)
	if err != nil {
		return err
	}
	return s.AdjustStock(ctx, tx, deltas)
}

// CloseRefundedOrder closes an order whose refunds are done (D14 5절, D19 A-507
// 「목표 상태는 상태머신이 정한다」) — 어디로 가는지는 refundCloseTarget 이 정한다. 배송
// 전이면 취소라 재고도 되돌린다. 닫을 곳이 없으면 아무것도 바꾸지 않고 "" 를 돌려준다.
//
// 환불을 실행한 쪽(executeAndSettle)이 PG 확정 뒤에 부르고, 그 기록이 실패해 열린 채
// 남은 주문은 A-507 「주문 종료」가 다시 부른다.
//
// A-507 이 품목 단위로 환불하므로 전액에 이르는 순간이 따로 없었다 — 돈은
// 전부 돌아갔는데 주문은 결제완료로 남아 발송 대기 줄에 서 있었다
// (2026-10-01 실측).
func (s *Store) CloseRefundedOrder(ctx context.Context, orderNo string, actor Actor) (Status, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	q := s.q.WithTx(tx)

	o, err := lockOrder(ctx, q, orderNo)
	if err != nil {
		return "", err
	}
	orderID, status := o.ID, Status(o.Status)
	// **품목을 잡은 뒤에 판정한다.** 환불 접수·거부는 주문 행을 잡지 않고 품목 → 결제로
	// 잡으므로, 잠그지 않고 읽으면 판정의 세 SELECT 사이에 거부가 커밋될 수 있다 —
	// 「전액」을 읽은 뒤 한도가 풀려도 닫고, 아래에서 품목을 다시 전부 소진한다.
	// id 오름차순이다 (RequestRefund 와 같은 순서) — 아니면 품목이 둘 이상인 주문에서
	// 환불 요청과 교착한다.
	if _, err := q.LockOrderItems(ctx, orderID); err != nil {
		return "", err
	}
	to, err := refundCloseTarget(ctx, q, orderID, status, actor)
	if err != nil || to == "" {
		return "", err
	}
	if to == StatusCancelled {
		if err := s.restockOrder(ctx, tx, orderID); err != nil {
			return "", err
		}
	}
	if err := q.SettleAllOrderItems(ctx, orderID); err != nil {
		return "", err
	}
	if err := s.moveOrder(ctx, tx, orderID, status, to, actor); err != nil {
		return "", err
	}
	return to, tx.Commit(ctx)
}

// RefundCloseTarget is where A-507 「주문 종료」 would send the order — "" 이면 단추가 없다.
// 종료 기록이 실패한 주문과, 이 판정이 생기기 전에 배송비 때문에 열린 채 남은 배송 뒤
// 주문이 여기에 걸린다.
// 잠그지 않고 읽는다. 누르면 CloseRefundedOrder 가 잠근 채 다시 판정한다.
func (s *Store) RefundCloseTarget(ctx context.Context, orderID string, status Status, actor Actor) (Status, error) {
	return refundCloseTarget(ctx, s.q, orderID, status, actor)
}

// refundCloseTarget decides where an order whose refunds are done goes — "" 이면 열어 둔다.
//
// 끝났다는 것은 남은 환불(요청·승인)이 없고, 결제액이 전부 돌아갔거나 배송 뒤 품목이
// 전부 환불된 것이다. 선점만 보면 아직 PG 로 가지 않은 건이 있어도 닫히고, 그 건이
// 거절되어 한도가 돌아가도 주문은 끝나 있다. 배송 뒤의 배송비는 돌려주지 않으므로
// (D50) 금액만 보면 그 주문은 영영 「전액」이 아니다. 배송 전이면 결제액 전부일 때만
// 닫는다 — 남은 배송비를 돌려주며 닫는 것은 전체 취소(P-506)다.
//
// 목적지는 상태머신이 정한다: 취소할 수 있으면(배송 전) 취소, 아니면 환불.
func refundCloseTarget(ctx context.Context, q *commerceq.Queries, orderID string, status Status, actor Actor) (Status, error) {
	p, err := q.ApprovedPaymentAmounts(ctx, orderID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	open, err := q.OpenRefundExists(ctx, orderID)
	if err != nil || open {
		return "", err
	}
	full := p.RefundedAmount >= p.ApprovedAmount
	switch {
	case full && CanTransition(status, StatusCancelled, actor) == nil:
		return StatusCancelled, nil
	case CanTransition(status, StatusRefunded, actor) != nil:
		return "", nil
	case full:
		return StatusRefunded, nil
	}
	left, err := q.UnsettledItemCount(ctx, orderID)
	if err != nil || left > 0 {
		return "", err
	}
	return StatusRefunded, nil
}

// requesterOf maps a screen id to refunds.requester.
//
// 화면 ID 를 그대로 넣지 않는다. `requester` 는 "누가 요청했나" 이고 그 값은
// 정산에서 읽히는데, 화면이 늘어날 때마다 값이 늘면 열거가 무의미해진다.
func requesterOf(actor Actor) string {
	if actor == "P-506" || actor == "P-507" {
		return "구매자"
	}
	return "관리자"
}

// ConfirmPurchase is P-510.
func (s *Store) ConfirmPurchase(ctx context.Context, orderNo string, actor Actor) error {
	return s.TransitionOrder(ctx, orderNo, StatusConfirmed, actor)
}

// restockDeltas is what putting an order's items back on the shelf looks like.
func restockDeltas(ctx context.Context, tx pgx.Tx, orderID string) ([]StockDelta, error) {
	rows, err := commerceq.New(tx).OrderItemStock(ctx, orderID)
	if err != nil {
		return nil, err
	}
	var deltas []StockDelta
	for _, r := range rows {
		deltas = append(deltas, StockDelta{VariantID: r.VariantID, Delta: int(r.Quantity)})
	}
	return deltas, nil
}

// ErrRefundState 는 환불 건이 실행할 수 있는 상태가 아니라는 뜻이다.
var ErrRefundState = errors.New("commerce: 실행할 수 있는 환불 건이 아닙니다")

// ErrRefundRetryExpired 는 결과 불명 환불의 멱등키가 PG 쪽에서 이미 죽었을
// 수 있다는 뜻이다. 그 뒤의 같은 키 호출은 첫 결과가 아니라 새 환불이 된다.
var ErrRefundRetryExpired = errors.New("commerce: 다시 실행할 수 있는 기간이 지났습니다")

// RefundRetryWindow 는 결과 불명 환불을 같은 키로 다시 부를 수 있는 기간이다.
// 토스는 같은 멱등키의 재요청에 15일 동안 첫 응답을 돌려준다 (D50). 하루를
// 남기고, 잴 때는 첫 호출보다 이르거나 같은 접수 시각을 쓴다 — 늘 짧게 잰다.
const RefundRetryWindow = 14 * 24 * time.Hour

// ExecuteRefund sends a reserved refund to the PG (A-507 의 순서: DB 한도 선점 →
// PG 호출 → 확정, D13). RequestRefund·CancelOrder·SettleReturn 이 만든 '요청'
// 행이 대상이다 — 그 행이 있다는 것은 한도가 이미 선점됐다는 뜻이고, 여기서
// 돈이 나간다. 이것이 없던 판에서는 어떤 경로도 PG 를 부르지 않았다: 환불은
// 우리 장부에만 있었고 돈은 토스에서 나가지 않았다.
//
// 상태의 뜻:
//
//	요청 → PG 를 부르기 전. 한도는 잡혀 있고 돈은 아직 안 나갔다
//	승인 → PG 를 **부르는 중이거나 결과 불명**. 여기서는 다시 부르지 않는다.
//	       A-507 의 「다시 실행」(RetryRefund)만 같은 멱등키로 다시 부른다 —
//	       키가 살아 있는 동안 PG 는 첫 결과를 돌려준다
//	완료 → PG 가 취소를 확정했다
//	거부 → RejectRefund 가 한도를 돌려줬다
//
// 확정된 실패(4xx)는 '요청' 으로 되돌려 다시 시도할 수 있게 한다. 결과 불명
// (타임아웃·5xx·IDEMPOTENT_REQUEST_PROCESSING)은 '승인' 에 둔다 —
// **미환불이 이중환불보다 낫다** (D13).
//
// PG 가 확정하면(이미 완료였어도) 환불이 끝났는지 보고 주문을 끝낸다 — 옮긴 상태를
// 돌려주고, 아직이면 "" 다 (CloseRefundedOrder). actor 는 그 전이를 일으키는
// 화면이다. 그 기록이 실패하면 ErrRefundSentNotSettled 다.
func (s *Store) ExecuteRefund(ctx context.Context, gw Gateway, refundID string, actor Actor) (Status, error) {
	return s.executeAndSettle(ctx, gw, refundID, time.Time{}, actor)
}

// RetryRefund is A-507's 「다시 실행」 for a stuck refund. '요청' 은 처음 실행과
// 같고, '승인'(결과 불명)은 같은 멱등키로 다시 부른다 — 접수가 RefundRetryWindow
// 안일 때만. 그 밖은 ErrRefundRetryExpired 이고 사람이 결제사 화면에서 확인한다.
func (s *Store) RetryRefund(ctx context.Context, gw Gateway, refundID string, now time.Time, actor Actor) (Status, error) {
	return s.executeAndSettle(ctx, gw, refundID, now, actor)
}

// ErrRefundSentNotSettled 는 PG 가 환불을 확정했는데 주문 종료 기록이 실패했다는
// 뜻이다. 돈은 이미 나갔다 — 호출자는 로그만 남기고 성공으로 다룬다. 주문은 열린 채
// 남고, A-507 「주문 종료」(CloseRefundedOrder)가 다시 닫는다.
var ErrRefundSentNotSettled = errors.New("commerce: 환불은 확정됐지만 주문 상태를 바꾸지 못했습니다")

// executeAndSettle 은 PG 호출과 주문 종료를 한 트랜잭션에 묶지 않는다 — PG 왕복 동안
// 주문을 잠가 두지 않고, 종료가 실패해도 나간 돈의 기록을 되돌리지 않는다.
func (s *Store) executeAndSettle(ctx context.Context, gw Gateway, refundID string, now time.Time, actor Actor) (Status, error) {
	ctx, cancel := moneyCtx(ctx) // PG 호출 뒤 기록까지 끝낸다 (payment.go)
	defer cancel()
	orderNo, err := s.executeRefund(ctx, gw, refundID, now)
	if err != nil {
		return "", err
	}
	to, err := s.CloseRefundedOrder(ctx, orderNo, actor)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrRefundSentNotSettled, err)
	}
	return to, nil
}

// executeRefund 의 now 가 영(zero)이면 '승인' 을 다시 부르지 않는다. 돌려주는 것은
// 그 환불의 주문번호다.
func (s *Store) executeRefund(ctx context.Context, gw Gateway, refundID string, now time.Time) (string, error) {
	if gw == nil {
		return "", errors.New("commerce: PG 가 설정되어 있지 않아 환불을 실행할 수 없습니다")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	q := s.q.WithTx(tx)

	r, err := q.LockRefundForExecute(ctx, refundID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	status, reason, requestKey, paymentKey, amount := r.Status, r.Reason, r.RequestKey, r.PaymentKey, int(r.Amount)
	orderNo := r.OrderNo
	switch status {
	case "완료":
		return orderNo, nil // 이미 나갔다. 재전송은 같은 결과다
	case "요청":
		if err := q.MarkRefundExecuting(ctx, refundID); err != nil {
			return "", err
		}
	case "승인":
		if now.IsZero() {
			return "", fmt.Errorf("%w: 상태 %s", ErrRefundState, status)
		}
		if now.Sub(r.CreatedAt) > RefundRetryWindow {
			return "", ErrRefundRetryExpired
		}
	default:
		return "", fmt.Errorf("%w: 상태 %s", ErrRefundState, status)
	}
	// 호출 중 표시를 커밋한다. 트랜잭션을 연 채 PG 를 부르면 잠금이 그 시간만큼
	// 유지되고, 프로세스가 도중에 죽으면 '요청' 으로 남아 두 번째 호출을 부른다.
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}

	res, err := gw.Cancel(ctx, CancelRequest{
		PaymentKey: paymentKey, Amount: amount, IdempotencyKey: requestKey,
		Reason: truncateRunes(reason, cancelReasonMax),
	})
	if err != nil {
		if errors.Is(err, ErrPaymentUnknown) {
			return "", err // '승인' 에 둔다. 사람이 조회로 확인한다
		}
		if uerr := s.q.RevertRefundToRequested(ctx, refundID); uerr != nil {
			return "", errors.Join(err, uerr)
		}
		return "", err
	}
	var raw []byte
	if res != nil {
		raw = MaskCardFields(res.Raw)
	}
	n, err := s.q.CompleteRefund(ctx, commerceq.CompleteRefundParams{ID: refundID, PgResponse: nullIfEmptyBytes(raw)})
	if err != nil {
		return "", err
	}
	if n == 0 {
		return "", fmt.Errorf("%w: 호출하는 사이 상태가 바뀌었습니다", ErrRefundState)
	}
	return orderNo, nil
}

// cancelReasonMax is Toss's limit on cancelReason (D50: ≤ 200자). refunds.reason
// allows 500.
const cancelReasonMax = 200

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// nullIfEmptyBytes keeps pg_response NULL when there is nothing to store —
// pgx 는 nil []byte 를 SQL NULL 로 보낸다.
func nullIfEmptyBytes(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	return b
}
