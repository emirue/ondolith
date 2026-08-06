package commerce

import (
	"context"
	"errors"
	"fmt"
	"time"

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

	var orderID, paymentID string
	err = tx.QueryRow(ctx, `
		SELECT o.id, p.id FROM orders o
		JOIN payments p ON p.order_id = o.id AND p.kind = '주문결제' AND p.status = '승인'
		WHERE o.order_no = $1`, orderNo).Scan(&orderID, &paymentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", 0, ErrNoPayment
	}
	if err != nil {
		return "", 0, err
	}

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
		var lineAmount, discount, quantity, settled int
		err := tx.QueryRow(ctx, `
			SELECT line_amount, discount_amount, quantity, settled_quantity
			FROM order_items WHERE id = $1 AND order_id = $2 FOR UPDATE`,
			l.OrderItemID, orderID).Scan(&lineAmount, &discount, &quantity, &settled)
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
		var open int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM return_items WHERE order_item_id = $1 AND is_open`,
			l.OrderItemID).Scan(&open); err != nil {
			return "", 0, err
		}
		if open > 0 {
			return "", 0, ErrReturnInProgress
		}

		part, err := RefundableAmount(lineAmount, discount, quantity, settled, l.Quantity)
		if err != nil {
			return "", 0, err
		}
		amount += part

		if _, err := tx.Exec(ctx, `
			UPDATE order_items SET settled_quantity = settled_quantity + $2, updated_at = now()
			WHERE id = $1`, l.OrderItemID, l.Quantity); err != nil {
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
	if _, err := tx.Exec(ctx, `
		UPDATE payments SET refunded_amount = refunded_amount + $2, updated_at = now()
		WHERE id = $1`, paymentID, amount); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23514" {
			return "", 0, ErrRefundExceeds
		}
		return "", 0, err
	}

	var refundID string
	err = tx.QueryRow(ctx, `
		INSERT INTO refunds (order_id, payment_id, status, requester, amount, reason, request_key)
		VALUES ($1, $2, '요청', $3, $4, $5, $6) RETURNING id`,
		orderID, paymentID, requester, amount, reason, requestKey).Scan(&refundID)
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
		if _, err := tx.Exec(ctx, `
			INSERT INTO refund_items (refund_id, order_item_id, quantity) VALUES ($1,$2,$3)`,
			refundID, l.OrderItemID, l.Quantity); err != nil {
			return "", 0, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return "", 0, err
	}
	return refundID, amount, nil
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

	var paymentID string
	var amount int
	err = tx.QueryRow(ctx, `
		UPDATE refunds SET status = '거부', reason = $2, updated_at = now()
		WHERE id = $1 AND status = '요청' RETURNING payment_id, amount`,
		refundID, reason).Scan(&paymentID, &amount)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE payments SET refunded_amount = refunded_amount - $2, updated_at = now()
		WHERE id = $1`, paymentID, amount); err != nil {
		return err
	}
	// 소진 수량도 되돌린다. 금액만 되돌리면 그 품목은 영영 다시 환불할 수
	// 없으면서 한도만 살아 있는 상태가 되고, 아무 오류도 나지 않는다.
	if _, err := tx.Exec(ctx, `
		UPDATE order_items oi SET settled_quantity = oi.settled_quantity - ri.quantity,
		                          updated_at = now()
		FROM refund_items ri
		WHERE ri.refund_id = $1 AND oi.id = ri.order_item_id`, refundID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Refunds lists an order's requests, newest first (P-508).
func (s *Store) Refunds(ctx context.Context, orderID string) ([]Refund, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, status, requester, amount, reason, created_at FROM refunds
		WHERE order_id = $1 ORDER BY created_at DESC, id`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Refund
	for rows.Next() {
		var r Refund
		if err := rows.Scan(&r.ID, &r.Status, &r.Requester, &r.Amount,
			&r.Reason, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RefundedTotal is what has been reserved or paid out so far.
func (s *Store) RefundedTotal(ctx context.Context, orderNo string) (approved, refunded int, err error) {
	err = s.pool.QueryRow(ctx, `
		SELECT p.approved_amount, p.refunded_amount FROM payments p
		JOIN orders o ON o.id = p.order_id
		WHERE o.order_no = $1 AND p.kind = '주문결제' AND p.status = '승인'`,
		orderNo).Scan(&approved, &refunded)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, ErrNoPayment
	}
	return approved, refunded, err
}

// CancelOrder is P-506: cancel before dispatch, money back in full.
//
// 배송 **전**이라 물건이 움직이지 않았고 돌려줄 것은 돈뿐이라 구매자가 직접
// 일으킬 수 있다 (D14 5-1). 배송 후는 A-507 이 승인해야 한다 — 승인 없이 돈이
// 나가면 되돌릴 방법이 없다.
func (s *Store) CancelOrder(ctx context.Context, orderNo string, actor Actor,
	requestKey string) error {

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var orderID, status string
	var total int
	err = tx.QueryRow(ctx,
		`SELECT id, status, total_amount FROM orders WHERE order_no = $1 FOR UPDATE`,
		orderNo).Scan(&orderID, &status, &total)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if err := CanTransition(Status(status), StatusCancelled, actor); err != nil {
		return err
	}

	// 재고를 되돌린다. 주문 생성이 차감했으므로 취소는 같은 만큼 푼다 — 풀지
	// 않으면 재고가 조용히 잠긴다 (D14 「교환 재고」와 같은 이유).
	rows, err := tx.Query(ctx,
		`SELECT variant_id, quantity FROM order_items WHERE order_id = $1`, orderID)
	if err != nil {
		return err
	}
	var deltas []StockDelta
	for rows.Next() {
		var variantID string
		var qty int
		if err := rows.Scan(&variantID, &qty); err != nil {
			rows.Close()
			return err
		}
		deltas = append(deltas, StockDelta{VariantID: variantID, Delta: qty})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if err := s.AdjustStock(ctx, tx, deltas); err != nil {
		return err
	}

	if _, err := tx.Exec(ctx,
		`UPDATE orders SET status = $2, updated_at = now() WHERE id = $1 AND status = $3`,
		orderID, string(StatusCancelled), status); err != nil {
		return err
	}

	// 결제가 있었으면 전액 환불을 접수한다. 없으면(결제대기) 돌려줄 돈이 없다.
	var paymentID string
	var approved, alreadyRefunded int
	err = tx.QueryRow(ctx, `
		SELECT id, approved_amount, refunded_amount FROM payments
		WHERE order_id = $1 AND kind = '주문결제' AND status = '승인' FOR UPDATE`,
		orderID).Scan(&paymentID, &approved, &alreadyRefunded)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return tx.Commit(ctx)
	case err != nil:
		return err
	}

	// 남은 수량을 전부 소진 처리해 두면, 취소된 주문에 다시 부분 환불을 넣는
	// 경로가 닫힌다 — 금액 한도만으로는 막히지 않는다.
	if _, err := tx.Exec(ctx,
		`UPDATE order_items SET settled_quantity = quantity, updated_at = now()
		 WHERE order_id = $1`, orderID); err != nil {
		return err
	}

	// **남은 몫만 돌려준다.** 부분 환불이 이미 나간 주문을 전액으로 취소하면
	// 같은 돈을 두 번 돌려주게 된다. 게다가 `refunded_amount` 에 **대입**하면
	// 앞선 선점이 지워져서 `CHECK (환불누적액 <= 승인금액)` 이 막지 못한다 —
	// 제약이 보는 값 자체가 사라지기 때문이다. 그래서 누적으로 더한다.
	remaining := approved - alreadyRefunded
	if remaining <= 0 {
		// 이미 전액이 선점됐다. 취소 상태와 수량 소진만 남기고 끝낸다.
		return tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE payments SET refunded_amount = refunded_amount + $2, updated_at = now()
		WHERE id = $1`, paymentID, remaining); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO refunds (order_id, payment_id, status, requester, amount, reason, request_key)
		VALUES ($1, $2, '요청', $3, $4, '주문 취소', $5)`,
		orderID, paymentID, requesterOf(actor), remaining, requestKey); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrRefundDuplicate
		}
		return err
	}
	return tx.Commit(ctx)
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
