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
	// ErrReturnInProgress 는 같은 품목에 이미 처리 중인 반품·교환이 있다는
	// 뜻이다. DB 의 부분 유니크(`return_items (order_item_id) WHERE is_open`)
	// 가 막고, 이 이름은 그것을 옮긴 것이다.
	ErrReturnInProgress = errors.New("commerce: 이미 처리 중인 반품·교환이 있습니다")
	// ErrExchangeVariant 는 교환 대상이 같은 상품의 조합이 아니라는 뜻이다.
	ErrExchangeVariant = errors.New("commerce: 교환은 같은 상품의 다른 조합으로만 됩니다")
	// ErrPickupRequired 는 수거 확인 전에 환불하려는 경우다.
	ErrPickupRequired = errors.New("commerce: 수거 확인 전에는 환불할 수 없습니다")
	// ErrReturnKind 는 반품에 교환 전용 인자를, 교환에 반품 인자를 준 경우다.
	ErrReturnKind = errors.New("commerce: 반품·교환 종류에 맞지 않는 요청입니다")
)

// ReturnKind is 반품 or 교환.
type ReturnKind string

const (
	KindReturn   ReturnKind = "반품"
	KindExchange ReturnKind = "교환"
)

// Return is one row of returns, as P-513/A-511 draw it.
type Return struct {
	ID           string
	ReturnNo     string
	Kind         ReturnKind
	Status       Status
	Reason       string
	RejectReason string
	Fault        string
	FeePolicy    string
	FeeAmount    int
	NewVariantID string
	PriceDiff    int
	CreatedAt    time.Time
	Items        []ReturnItem
}

// ReturnItem is one line of a return request.
type ReturnItem struct {
	OrderItemID string
	ProductName string
	OptionLabel string
	Quantity    int
	IsOpen      bool
}

// ReturnRequest is what P-511/P-512 post, minus everything the server decides.
//
// **금액이 없다** (D19 P-511 「받지 않는 필드」). 환불액도 반품 배송비도 서버가
// `order_items` 스냅샷과 A-512 정책에서 계산한다 — 폼에 금액이 오면 그 값이
// 계산에 닿을 수 있고, FR-607 의 대조가 자기 자신과의 대조가 된다.
type ReturnRequest struct {
	Kind   ReturnKind
	Lines  []RefundLine
	Reason string
	// NewVariantID 는 교환에만 쓴다. 반품이면 무시하는 것이 아니라 **거부**
	// 한다 — 무시하면 폼을 잘못 쓴 것이 조용히 넘어가고, 그 폼은 다음에도
	// 잘못 쓰인다.
	NewVariantID string
}

// OpenReturn is P-511·P-512's body.
//
// 순서:
//
//  1. 주문을 잠근 채 읽고 상태를 확인한다 — `배송완료` 에서만 시작한다.
//     구매확정 이후에는 받지 않는다 (FR-617, FR-618). 상태머신이 그것을 안다.
//  2. 품목이 이 주문 소속인지, 남은 수량이 있는지 확인한다.
//  3. 교환이면 새 조합이 **같은 상품**인지 확인하고 재고를 예약한다 (FR-618).
//  4. returns·return_items 를 만든다. `is_open` 부분 유니크가 같은 품목의
//     두 번째 처리 중 건을 막는다.
//
// **환불은 여기서 나가지 않는다.** 수거 확인(A-511)을 거쳐야 한다 —
// 물건을 못 받고 돈만 나가는 것을 상태로 막는 것이 D14 의 「수거 우선」이다.
func (s *Store) OpenReturn(ctx context.Context, orderNo string, req ReturnRequest,
	actor Actor, now time.Time) (*Return, error) {

	if req.Kind != KindReturn && req.Kind != KindExchange {
		return nil, fmt.Errorf("%w: %q", ErrReturnKind, req.Kind)
	}
	if req.Kind == KindReturn && req.NewVariantID != "" {
		return nil, fmt.Errorf("%w: 반품에는 새 조합이 없습니다", ErrReturnKind)
	}
	if req.Kind == KindExchange && req.NewVariantID == "" {
		return nil, fmt.Errorf("%w: 교환할 조합을 고르세요", ErrReturnKind)
	}
	if len(req.Lines) == 0 {
		return nil, errors.New("commerce: 반품·교환할 품목을 고르세요")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var orderID, status string
	err = tx.QueryRow(ctx,
		`SELECT id, status FROM orders WHERE order_no = $1 FOR UPDATE`, orderNo).
		Scan(&orderID, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	// 상태 판정은 상태머신이 한다. `배송완료` 에서만, 구매확정 뒤에는 안 된다는
	// 규칙이 D14 5절 표에 이미 있고, 여기서 다시 적으면 두 벌이 된다.
	target := StatusReturnOpen
	if req.Kind == KindExchange {
		target = StatusExchangeOpen
	}
	if err := CanTransition(Status(status), target, actor); err != nil {
		return nil, err
	}

	// 품목 확인. 남은 수량은 settled_quantity 기준이다 — 부분 환불과 반품이
	// 같은 잔량을 나눠 쓴다.
	type line struct {
		id                     string
		productID              string
		quantity, settled, qty int
	}
	var lines []line
	for _, l := range req.Lines {
		if l.Quantity < 1 {
			return nil, fmt.Errorf("%w: %d", ErrQuantityRange, l.Quantity)
		}
		var got line
		got.id, got.qty = l.OrderItemID, l.Quantity
		err := tx.QueryRow(ctx, `
			SELECT product_id, quantity, settled_quantity FROM order_items
			WHERE id = $1 AND order_id = $2 FOR UPDATE`,
			l.OrderItemID, orderID).Scan(&got.productID, &got.quantity, &got.settled)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		if got.settled+got.qty > got.quantity {
			return nil, fmt.Errorf("%w: 소진 %d + 요청 %d > 주문 %d",
				ErrRefundQuantity, got.settled, got.qty, got.quantity)
		}
		lines = append(lines, got)
	}

	// 교환 대상은 **같은 상품의 다른 조합**이다 (FR-618). 다른 상품을 허용하면
	// 교환이 곧 교환 가장한 재주문이 되고, 차액 계산의 근거가 사라진다.
	priceDiff := 0
	if req.Kind == KindExchange {
		var newProduct string
		var newDelta int
		err := tx.QueryRow(ctx, `
			SELECT product_id, price_delta FROM product_variants WHERE id = $1`,
			req.NewVariantID).Scan(&newProduct, &newDelta)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		for _, l := range lines {
			if l.productID != newProduct {
				return nil, ErrExchangeVariant
			}
		}
		// 차액은 조합 차액의 수량 배다. 스냅샷 단가와 새 조합 단가의 차이를
		// 쓰지 않는 이유: 기본가가 그 사이 바뀌었을 수 있고, 교환은 상품을
		// 다시 사는 것이 아니라 조합만 바꾸는 것이다.
		var oldDelta int
		if err := tx.QueryRow(ctx, `
			SELECT v.price_delta FROM order_items oi
			JOIN product_variants v ON v.id = oi.variant_id
			WHERE oi.id = $1`, lines[0].id).Scan(&oldDelta); err != nil {
			return nil, err
		}
		for _, l := range lines {
			priceDiff += (newDelta - oldDelta) * l.qty
		}

		// 재고 예약 (D14 「교환 재고」). 잡아 두지 않으면 수거하는 동안 그
		// 조합이 팔려 나가고, 재발송할 물건이 없어진다.
		qty := 0
		for _, l := range lines {
			qty += l.qty
		}
		deltas, err := Reserve(req.NewVariantID, qty)
		if err != nil {
			return nil, err
		}
		if err := s.AdjustStock(ctx, tx, deltas); err != nil {
			return nil, err
		}
	}

	ret := &Return{ReturnNo: NewReturnNo(now), Kind: req.Kind, Status: target,
		Reason: req.Reason, NewVariantID: req.NewVariantID, PriceDiff: priceDiff}

	var newVariant, diff any
	if req.Kind == KindExchange {
		newVariant, diff = req.NewVariantID, priceDiff
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO returns (return_no, order_id, kind, status, reason,
		                     new_variant_id, price_difference)
		VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
		ret.ReturnNo, orderID, string(req.Kind), string(target), req.Reason,
		newVariant, diff).Scan(&ret.ID)
	if err != nil {
		return nil, err
	}

	for _, l := range lines {
		if _, err := tx.Exec(ctx, `
			INSERT INTO return_items (return_id, order_item_id, quantity) VALUES ($1,$2,$3)`,
			ret.ID, l.id, l.qty); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				// 부분 유니크가 잡았다. 같은 품목에 처리 중인 건이 이미 있다.
				return nil, ErrReturnInProgress
			}
			return nil, err
		}
	}

	// 주문 상태도 옮긴다. 비교-교환이라 그 사이 상태가 바뀌었으면 실패한다.
	tag, err := tx.Exec(ctx,
		`UPDATE orders SET status = $2, updated_at = now() WHERE id = $1 AND status = $3`,
		orderID, string(target), status)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, fmt.Errorf("%w: 접수하는 사이 주문 상태가 바뀌었습니다", ErrTransitionNotAllowed)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return ret, nil
}

// ConfirmPickup is A-511's 수거 확인.
//
// **여기서 배송비 스냅샷이 찍힌다** (D30 returns_snapshot_after_pickup). A-512
// 정책을 나중에 바꿔도 이미 수거한 건의 환불액이 달라지지 않아야 하고, 참조로
// 두면 정책 변경이 곧 과거 환불액 변경이 된다.
//
// 귀책이 판매자면 배송비는 0 이다 — 하자 상품의 반품비를 구매자가 물지 않는다
// (DB CHECK 도 같은 것을 막는다).
func (s *Store) ConfirmPickup(ctx context.Context, returnNo, fault, feePolicy string,
	feeAmount int, actor Actor) error {

	if fault != "구매자" && fault != "판매자" {
		return fmt.Errorf("commerce: 귀책은 구매자 또는 판매자입니다: %q", fault)
	}
	if fault == "판매자" {
		// 화면이 무엇을 보냈든 0 으로 만든다. DB CHECK 가 잡기도 하지만
		// 거기서 나오는 것은 제약 위반이지 정책이 아니다.
		feeAmount = 0
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var id, kind, status, orderID string
	err = tx.QueryRow(ctx, `
		SELECT r.id, r.kind, r.status, r.order_id FROM returns r
		WHERE r.return_no = $1 FOR UPDATE`, returnNo).Scan(&id, &kind, &status, &orderID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}

	target := StatusReturnPickedUp
	if ReturnKind(kind) == KindExchange {
		target = StatusExchangePicked
	}
	if err := CanTransition(Status(status), target, actor); err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, `
		UPDATE returns SET status = $2, fault = $3,
		       shipping_fee_policy = $4, shipping_fee_amount = $5, updated_at = now()
		WHERE id = $1 AND status = $6`,
		id, string(target), fault, feePolicy, feeAmount, status); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE orders SET status = $2, updated_at = now() WHERE id = $1`,
		orderID, string(target)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SettleReturn is A-511's 환불 확정 — the money finally moves.
//
// **수거 확인을 거치지 않으면 여기 올 수 없다.** 상태머신이 `반품접수 → 환불`
// 화살표를 갖고 있지 않고, 그것이 D14 「수거 우선」의 전부다 — 물건을 못 받고
// 돈만 나가는 것을 상태로 막는다.
//
// 환불액은 **스냅샷에서 서버가 계산**한다 (FR-617): 품목별 할인후 금액의 몫에서
// 수거 시점에 찍은 배송비 스냅샷을 뺀다. 요청 금액은 받지 않는다.
func (s *Store) SettleReturn(ctx context.Context, returnNo string, actor Actor,
	requestKey string) (amount int, err error) {

	if requestKey == "" {
		return 0, errors.New("commerce: 요청 키가 필요합니다")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	var id, kind, status, orderID string
	var feeAmount int
	err = tx.QueryRow(ctx, `
		SELECT id, kind, status, order_id, COALESCE(shipping_fee_amount, 0)
		FROM returns WHERE return_no = $1 FOR UPDATE`, returnNo).
		Scan(&id, &kind, &status, &orderID, &feeAmount)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	if ReturnKind(kind) != KindReturn {
		return 0, fmt.Errorf("%w: 교환 건은 환불로 정산하지 않습니다", ErrReturnKind)
	}
	// 수거 전이면 상태머신이 거부한다. 그 거부가 이 함수의 존재 이유다.
	if err := CanTransition(Status(status), StatusRefunded, actor); err != nil {
		if Status(status) == StatusReturnOpen {
			return 0, ErrPickupRequired
		}
		return 0, err
	}

	var paymentID string
	err = tx.QueryRow(ctx, `
		SELECT id FROM payments
		WHERE order_id = $1 AND kind = '주문결제' AND status = '승인'`, orderID).Scan(&paymentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNoPayment
	}
	if err != nil {
		return 0, err
	}

	// 품목별 몫을 스냅샷에서 계산한다.
	rows, err := tx.Query(ctx, `
		SELECT ri.order_item_id, ri.quantity,
		       oi.line_amount, oi.discount_amount, oi.quantity, oi.settled_quantity
		FROM return_items ri
		JOIN order_items oi ON oi.id = ri.order_item_id
		WHERE ri.return_id = $1 AND ri.is_open
		ORDER BY ri.order_item_id
		FOR UPDATE OF oi`, id)
	if err != nil {
		return 0, err
	}
	type settle struct {
		itemID string
		qty    int
	}
	var toSettle []settle
	for rows.Next() {
		var itemID string
		var retQty, lineAmount, discount, quantity, settled int
		if err := rows.Scan(&itemID, &retQty, &lineAmount, &discount, &quantity, &settled); err != nil {
			rows.Close()
			return 0, err
		}
		part, perr := RefundableAmount(lineAmount, discount, quantity, settled, retQty)
		if perr != nil {
			rows.Close()
			return 0, perr
		}
		amount += part
		toSettle = append(toSettle, settle{itemID, retQty})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(toSettle) == 0 {
		return 0, fmt.Errorf("%w: 정산할 품목이 없습니다", ErrNotFound)
	}

	// 반품 배송비를 뺀다 (A-512 스냅샷). 정책이 `별도청구` 면 환불에서 빼지
	// 않고 따로 청구하므로 0 을 뺀 것과 같다 — 그 청구 경로는 아직 없다.
	amount -= feeAmount
	if amount <= 0 {
		return 0, fmt.Errorf("%w: 배송비를 빼면 환불할 금액이 없습니다", ErrPriceNegative)
	}

	for _, st := range toSettle {
		if _, err := tx.Exec(ctx, `
			UPDATE order_items SET settled_quantity = settled_quantity + $2, updated_at = now()
			WHERE id = $1`, st.itemID, st.qty); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23514" {
				return 0, ErrRefundQuantity
			}
			return 0, err
		}
	}

	// 선점. 부분 환불과 **같은 한도**를 나눠 쓴다 — 각각 한도까지 쓰면
	// 결제액을 넘는 사고가 난다 (D19 P-511).
	if _, err := tx.Exec(ctx, `
		UPDATE payments SET refunded_amount = refunded_amount + $2, updated_at = now()
		WHERE id = $1`, paymentID, amount); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23514" {
			return 0, ErrRefundExceeds
		}
		return 0, err
	}

	var refundID string
	err = tx.QueryRow(ctx, `
		INSERT INTO refunds (order_id, payment_id, return_id, status, requester,
		                     amount, reason, request_key)
		VALUES ($1,$2,$3,'요청','관리자',$4,'반품 환불',$5) RETURNING id`,
		orderID, paymentID, id, amount, requestKey).Scan(&refundID)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return 0, ErrRefundDuplicate
	}
	if err != nil {
		return 0, err
	}
	// return_id 가 있는 refunds 는 refund_items 를 만들지 않는다 — 품목·수량이
	// 이미 return_items 에 있고, 양쪽에 넣으면 settled_quantity 가 이중
	// 계상된다 (D30).

	// 처리 중 표시를 내린다. returns 가 종결로 가는 트랜잭션에서 함께 내려야
	// 부분 인덱스가 다음 반품을 허용한다.
	if _, err := tx.Exec(ctx,
		`UPDATE return_items SET is_open = false, updated_at = now() WHERE return_id = $1`,
		id); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE returns SET status = $2, updated_at = now() WHERE id = $1`,
		id, string(StatusRefunded)); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE orders SET status = $2, updated_at = now() WHERE id = $1`,
		orderID, string(StatusRefunded)); err != nil {
		return 0, err
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return amount, nil
}

// RejectReturn sends the order back to 배송완료 and releases what was held.
//
// 교환이면 예약한 재고를 **반드시** 푼다 (D14 「교환 재고」). 풀지 않으면 재고가
// 조용히 잠기고, 잠긴 재고는 오류를 내지 않으므로 사람이 숫자를 보고서야 안다.
func (s *Store) RejectReturn(ctx context.Context, returnNo, reason string, actor Actor) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var id, kind, status, orderID, newVariant string
	err = tx.QueryRow(ctx, `
		SELECT id, kind, status, order_id, COALESCE(new_variant_id::text, '')
		FROM returns WHERE return_no = $1 FOR UPDATE`, returnNo).
		Scan(&id, &kind, &status, &orderID, &newVariant)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if err := CanTransition(Status(status), StatusDelivered, actor); err != nil {
		return err
	}

	if ReturnKind(kind) == KindExchange && newVariant != "" {
		var qty int
		if err := tx.QueryRow(ctx,
			`SELECT COALESCE(sum(quantity), 0) FROM return_items WHERE return_id = $1`,
			id).Scan(&qty); err != nil {
			return err
		}
		if qty > 0 {
			// reserved 는 예약한 수량 그대로다. Release 가 그것을 넘겨 푸는
			// 요청을 거부하므로, 여기서 같은 값을 준다.
			deltas, err := Release(newVariant, qty, qty)
			if err != nil {
				return err
			}
			if err := s.AdjustStock(ctx, tx, deltas); err != nil {
				return err
			}
		}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE returns SET status = '거부', reject_reason = $2, updated_at = now()
		WHERE id = $1`, id, reason); err != nil {
		return err
	}
	// 처리 중 표시를 내려야 그 품목에 다시 반품·교환을 걸 수 있다.
	if _, err := tx.Exec(ctx,
		`UPDATE return_items SET is_open = false, updated_at = now() WHERE return_id = $1`,
		id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE orders SET status = $2, updated_at = now() WHERE id = $1`,
		orderID, string(StatusDelivered)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Returns lists an order's return/exchange history (P-513).
func (s *Store) Returns(ctx context.Context, orderID string) ([]Return, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, return_no, kind, status, reason, reject_reason,
		       COALESCE(fault, ''), COALESCE(shipping_fee_policy, ''),
		       COALESCE(shipping_fee_amount, 0), COALESCE(new_variant_id::text, ''),
		       COALESCE(price_difference, 0), created_at
		FROM returns WHERE order_id = $1 ORDER BY created_at DESC, id`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Return
	for rows.Next() {
		var r Return
		var kind, status string
		if err := rows.Scan(&r.ID, &r.ReturnNo, &kind, &status, &r.Reason, &r.RejectReason,
			&r.Fault, &r.FeePolicy, &r.FeeAmount, &r.NewVariantID,
			&r.PriceDiff, &r.CreatedAt); err != nil {
			return nil, err
		}
		r.Kind, r.Status = ReturnKind(kind), Status(status)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i := range out {
		items, err := s.returnItems(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Items = items
	}
	return out, nil
}

func (s *Store) returnItems(ctx context.Context, returnID string) ([]ReturnItem, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT ri.order_item_id, oi.product_name, oi.option_label, ri.quantity, ri.is_open
		FROM return_items ri JOIN order_items oi ON oi.id = ri.order_item_id
		WHERE ri.return_id = $1 ORDER BY oi.created_at, oi.id`, returnID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReturnItem
	for rows.Next() {
		var it ReturnItem
		if err := rows.Scan(&it.OrderItemID, &it.ProductName, &it.OptionLabel,
			&it.Quantity, &it.IsOpen); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// ReturnByNo reads one return.
func (s *Store) ReturnByNo(ctx context.Context, returnNo string) (*Return, error) {
	var r Return
	var kind, status string
	err := s.pool.QueryRow(ctx, `
		SELECT id, return_no, kind, status, reason, reject_reason,
		       COALESCE(fault, ''), COALESCE(shipping_fee_policy, ''),
		       COALESCE(shipping_fee_amount, 0), COALESCE(new_variant_id::text, ''),
		       COALESCE(price_difference, 0), created_at
		FROM returns WHERE return_no = $1`, returnNo).
		Scan(&r.ID, &r.ReturnNo, &kind, &status, &r.Reason, &r.RejectReason,
			&r.Fault, &r.FeePolicy, &r.FeeAmount, &r.NewVariantID, &r.PriceDiff, &r.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	r.Kind, r.Status = ReturnKind(kind), Status(status)
	items, err := s.returnItems(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	r.Items = items
	return &r, nil
}

// VariantsForExchange lists the other combinations of the same product.
//
// **같은 상품만** 이다 (FR-618). 화면이 그 목록만 보여주면 사용자가 없는
// 선택지를 고르지 않는다 — 거부하는 것은 여전히 OpenReturn 이다 (D15 4.3:
// 숨기는 것은 보안이 아니다).
func (s *Store) VariantsForExchange(ctx context.Context, orderItemID string) ([]Variant, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT v.id, v.product_id, v.option_values, COALESCE(v.sku, ''),
		       v.price_delta, v.stock, v.is_visible
		FROM product_variants v
		WHERE v.product_id = (SELECT product_id FROM order_items WHERE id = $1)
		  AND v.id <> (SELECT variant_id FROM order_items WHERE id = $1)
		  AND v.is_visible AND v.stock > 0
		ORDER BY v.price_delta, v.id`, orderItemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Variant
	for rows.Next() {
		var v Variant
		var raw []byte
		if err := rows.Scan(&v.ID, &v.ProductID, &raw, &v.SKU,
			&v.PriceDelta, &v.Stock, &v.Visible); err != nil {
			return nil, err
		}
		if err := unmarshalOptions(raw, &v.OptionValues); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
