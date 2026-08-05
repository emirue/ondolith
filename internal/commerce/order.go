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
	ErrCartEmpty      = errors.New("commerce: 장바구니가 비었습니다")
	ErrTermsRequired  = errors.New("commerce: 필수 약관에 동의해야 합니다")
	ErrOrdererContact = errors.New("commerce: 주문자 이메일과 연락처가 필요합니다")
)

// OrderForm is P-406's whole input surface.
//
// **금액·할인·합계 필드가 없다.** 있으면 클라이언트가 보낸 값이 계산에 닿을 수
// 있고, FR-607 의 대조는 그 순간 자기 자신과의 대조가 된다. 서버가 장바구니와
// 상품 행에서 계산한다 (D19 P-405 「받지 않는 필드」).
//
// 품목 목록도 받지 않는다. 무엇을 사는지는 장바구니가 정한다 — 폼에서 받으면
// 담지 않은 것을 주문할 수 있다.
type OrderForm struct {
	ReceiverName  string
	ReceiverPhone string
	Postcode      string
	Address1      string
	Address2      string
	DeliveryMemo  string
	OrdererEmail  string
	OrdererPhone  string
	// AgreedTerms 는 동의한 약관 ID 다. 필수 약관이 빠지면 거부한다 (FR-619).
	AgreedTerms []string
}

// Order is the created row, as the confirmation screen needs it.
type Order struct {
	ID      string
	OrderNo string
	Status  Status
	Goods   int
	Fee     int
	Total   int
}

// CreateOrder is P-406's body: one transaction, and nothing outside it.
//
// 순서가 중요하다.
//
//  1. 장바구니를 **잠근 채** 읽는다. 잠그지 않으면 금액을 계산한 뒤 항목이
//     바뀌어, 저장되는 총액이 저장되는 품목과 다른 주문이 생긴다.
//  2. 재고를 `FOR UPDATE` 로 차감한다. 여기서 실패하면 아무것도 남지 않는다.
//  3. 금액을 **서버가** 계산한다.
//  4. orders·order_items 를 스냅샷으로 기록한다 (FR-612).
//  5. 필수 약관 동의를 기록한다 (FR-619).
//
// 어느 단계가 실패해도 재고가 줄지 않는다 — 전부 한 트랜잭션이고, 롤백이
// 되돌린다. 재고를 먼저 커밋하고 주문을 나중에 쓰는 구조는 "재고는 줄었는데
// 주문이 없는" 상태를 만들고, 그것을 되돌리는 화면은 없다.
func (s *Store) CreateOrder(ctx context.Context, o CartOwner, userID string,
	form OrderForm, ship Shipping, now time.Time) (*Order, error) {

	if !o.Valid() {
		return nil, ErrCartOwner
	}
	if form.OrdererEmail == "" || form.OrdererPhone == "" {
		// 둘 다 NOT NULL 이다 (D30 orders). 회원 계정이 지워져도 주문서를
		// 보낼 곳과 비회원 조회의 대조 키가 남아야 한다.
		//
		// DB 도 막지만 거기서 나오는 것은 제약 위반이고, 화면은 그것을 500 으로
		// 그린다. 폼 오류로 말하려면 여기서 잡아야 한다.
		return nil, ErrOrdererContact
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	// (1) 장바구니를 잠근 채 읽는다. FOR UPDATE OF ci 는 장바구니 항목 행만
	// 잠근다 — 상품 행까지 잠그면 같은 상품을 보는 모든 주문이 줄을 선다.
	rows, err := tx.Query(ctx, `
		SELECT ci.variant_id, ci.quantity, p.id, p.name, v.option_values,
		       p.base_price, v.price_delta, p.is_visible, v.is_visible, v.stock
		FROM cart_items ci
		JOIN carts c            ON c.id = ci.cart_id
		JOIN product_variants v ON v.id = ci.variant_id
		JOIN products p         ON p.id = v.product_id
		WHERE ($1::uuid IS NOT NULL AND c.user_id = $1)
		   OR ($2::text IS NOT NULL AND c.guest_key = $2)
		ORDER BY ci.created_at, ci.id
		FOR UPDATE OF ci`, nullable(o.UserID), nullable(o.GuestKey))
	if err != nil {
		return nil, err
	}

	type line struct {
		variantID, productID, name string
		optionLabel                string
		unitPrice, quantity        int
	}
	var lines []line
	var amounts []Line
	var deltas []StockDelta
	for rows.Next() {
		var l line
		var raw []byte
		var basePrice, priceDelta, stock int
		var productVisible, variantVisible bool
		if err := rows.Scan(&l.variantID, &l.quantity, &l.productID, &l.name, &raw,
			&basePrice, &priceDelta, &productVisible, &variantVisible, &stock); err != nil {
			rows.Close()
			return nil, err
		}
		var opts map[string]string
		if err := unmarshalOptions(raw, &opts); err != nil {
			rows.Close()
			return nil, err
		}
		l.optionLabel = OptionLabel(opts)

		sell := Sellable{ProductVisible: productVisible, VariantVisible: variantVisible, Stock: stock}
		if err := sell.CheckAvailable(l.quantity); err != nil {
			rows.Close()
			return nil, fmt.Errorf("%s: %w", l.name, err)
		}
		l.unitPrice = basePrice + priceDelta

		lines = append(lines, l)
		amounts = append(amounts, Line{BasePrice: basePrice, PriceDelta: priceDelta, Quantity: l.quantity})
		deltas = append(deltas, StockDelta{VariantID: l.variantID, Delta: -l.quantity})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return nil, ErrCartEmpty
	}

	// (2) 재고 차감. 잠금 순서가 정해져 있어 교착이 생기지 않는다.
	if err := s.AdjustStock(ctx, tx, deltas); err != nil {
		return nil, err
	}

	// (3) 금액은 서버가 계산한다. 폼에는 금액 필드가 없다.
	goods, fee, total, err := Total(amounts, ship)
	if err != nil {
		return nil, err
	}

	// (5-a) 필수 약관을 먼저 확인한다. 주문 행을 쓴 뒤에 거부하면 롤백으로
	// 사라지지만, 주문번호는 이미 소비된 뒤다.
	required, err := requiredTermIDs(ctx, tx, now)
	if err != nil {
		return nil, err
	}
	agreed := map[string]bool{}
	for _, id := range form.AgreedTerms {
		agreed[id] = true
	}
	for _, id := range required {
		if !agreed[id] {
			return nil, ErrTermsRequired
		}
	}

	// (4) 주문 기록. 상태는 상태머신의 시작점이다.
	order := &Order{OrderNo: NewOrderNo(now), Status: StatusPaymentPending,
		Goods: goods, Fee: fee, Total: total}
	err = tx.QueryRow(ctx, `
		INSERT INTO orders (order_no, user_id, status, total_amount,
		                    receiver_name, receiver_phone, postcode, address1, address2,
		                    delivery_memo, orderer_email, orderer_phone)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING id`,
		order.OrderNo, nullable(userID), string(order.Status), total,
		form.ReceiverName, form.ReceiverPhone, form.Postcode, form.Address1, form.Address2,
		form.DeliveryMemo, form.OrdererEmail, form.OrdererPhone).Scan(&order.ID)
	if err != nil {
		return nil, err
	}

	for _, l := range lines {
		// 상품명·옵션 표기·단가는 스냅샷이다 (FR-612). FK 조인으로 대체하지
		// 않는다 — 조합이 은퇴한 뒤에도 그때 산 것이 재현돼야 한다.
		if _, err := tx.Exec(ctx, `
			INSERT INTO order_items (order_id, product_id, variant_id, product_name,
			                         option_label, unit_price, quantity)
			VALUES ($1,$2,$3,$4,$5,$6,$7)`,
			order.ID, l.productID, l.variantID, l.name, l.optionLabel,
			l.unitPrice, l.quantity); err != nil {
			return nil, err
		}
	}

	// (5-b) 동의 이력. 본문을 복사하지 않는다 — terms 행이 불변이고 RESTRICT 가
	// 삭제를 막으므로 참조만으로 재현된다 (D30).
	for _, id := range form.AgreedTerms {
		if _, err := tx.Exec(ctx,
			`INSERT INTO order_agreements (order_id, terms_id) VALUES ($1, $2)
			 ON CONFLICT DO NOTHING`, order.ID, id); err != nil {
			return nil, err
		}
	}

	// 장바구니를 비운다. 남겨 두면 뒤로 가기 한 번이 같은 것을 또 주문한다.
	if _, err := tx.Exec(ctx, `
		DELETE FROM cart_items ci
		WHERE ci.cart_id IN (SELECT id FROM carts
		                     WHERE ($1::uuid IS NOT NULL AND user_id = $1)
		                        OR ($2::text IS NOT NULL AND guest_key = $2))`,
		nullable(o.UserID), nullable(o.GuestKey)); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return order, nil
}

// requiredTermIDs lists the terms in force at `now` that must be agreed to.
//
// 종류마다 가장 최근 시행본 하나다. 여러 버전을 다 요구하면 개정할 때마다
// 과거 버전에도 동의해야 한다.
func requiredTermIDs(ctx context.Context, tx pgx.Tx, now time.Time) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT ON (kind) id FROM terms
		WHERE is_required AND effective_at <= $1
		ORDER BY kind, effective_at DESC`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// OptionLabel renders "색상: 검정 / 사이즈: L" for the order-item snapshot.
//
// 키 순서를 정렬한다. map 의 순회 순서는 Go 가 매번 섞으므로, 정렬하지 않으면
// 같은 조합의 주문 두 건이 서로 다른 표기를 갖는다.
func OptionLabel(opts map[string]string) string {
	if len(opts) == 0 {
		return ""
	}
	keys := make([]string, 0, len(opts))
	for k := range opts {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	out := ""
	for i, k := range keys {
		if i > 0 {
			out += " / "
		}
		out += k + ": " + opts[k]
	}
	return out
}

// Term is one row of terms, as the checkout screen shows it.
type Term struct {
	ID       string
	Kind     string
	Version  string
	Body     string
	Required bool
}

// TermsInForce lists the newest effective version of each kind at `now`.
//
// 화면이 보여주는 것과 서버가 요구하는 것이 **같은 함수에서** 나와야 한다.
// 두 곳에서 고르면 화면은 v2 를 보여주고 서버는 v1 을 요구하는 일이 생기고,
// 그때 사용자는 체크했는데도 거부당한다.
func (s *Store) TermsInForce(ctx context.Context, now time.Time) ([]Term, error) {
	const q = `
		SELECT DISTINCT ON (kind) id, kind, version, body, is_required
		FROM terms WHERE effective_at <= $1
		ORDER BY kind, effective_at DESC`
	rows, err := s.pool.Query(ctx, q, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Term
	for rows.Next() {
		var t Term
		if err := rows.Scan(&t.ID, &t.Kind, &t.Version, &t.Body, &t.Required); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// OrderDetail is what P-410/P-502 draw, snapshots only.
type OrderDetail struct {
	ID            string
	OrderNo       string
	Status        Status
	Total         int
	ReceiverName  string
	ReceiverPhone string
	Postcode      string
	Address1      string
	Address2      string
	OrdererEmail  string
	OrdererPhone  string
	CreatedAt     time.Time
	Items         []OrderItem
}

// OrderItem is one line, from the snapshot columns only.
//
// 상품 표를 조인하지 않는다. 조인해 현재 이름·가격을 보여주면 FR-612 가
// 깨진다 — 주문서는 그때 산 것을 재현해야 한다.
type OrderItem struct {
	ProductName string
	OptionLabel string
	UnitPrice   int
	Quantity    int
	LineAmount  int
}

// OrderByNo reads one order, scoped to who may see it.
//
// 소유권이 WHERE 절에 있다 (SC-3). userID 가 비어 있으면 비회원 경로이고,
// 그때는 주문번호만으로 열지 않고 **연락처 대조**를 함께 요구한다 (P-504) —
// 주문번호 하나로 열리면 그 번호가 곧 열쇠가 된다.
func (s *Store) OrderByNo(ctx context.Context, orderNo, userID, ordererPhone string) (*OrderDetail, error) {
	const q = `
		SELECT id, order_no, status, total_amount, receiver_name, receiver_phone,
		       postcode, address1, address2, orderer_email, orderer_phone, created_at
		FROM orders
		WHERE order_no = $1
		  AND ( ($2::uuid IS NOT NULL AND user_id = $2)
		     OR ($3::text IS NOT NULL AND orderer_phone = $3) )`
	var o OrderDetail
	var status string
	err := s.pool.QueryRow(ctx, q, orderNo, nullable(userID), nullable(ordererPhone)).
		Scan(&o.ID, &o.OrderNo, &status, &o.Total, &o.ReceiverName, &o.ReceiverPhone,
			&o.Postcode, &o.Address1, &o.Address2, &o.OrdererEmail, &o.OrdererPhone, &o.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	o.Status = Status(status)

	rows, err := s.pool.Query(ctx, `
		SELECT product_name, option_label, unit_price, quantity, line_amount
		FROM order_items WHERE order_id = $1 ORDER BY created_at, id`, o.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var it OrderItem
		if err := rows.Scan(&it.ProductName, &it.OptionLabel, &it.UnitPrice,
			&it.Quantity, &it.LineAmount); err != nil {
			return nil, err
		}
		o.Items = append(o.Items, it)
	}
	return &o, rows.Err()
}

// MyOrders is P-501.
func (s *Store) MyOrders(ctx context.Context, userID string, page int) ([]OrderDetail, error) {
	if userID == "" {
		return nil, ErrNotFound
	}
	limit, offset := ProductQuery{Page: page}.clamp()
	rows, err := s.pool.Query(ctx, `
		SELECT order_no, status, total_amount, created_at
		FROM orders WHERE user_id = $1
		ORDER BY created_at DESC, id LIMIT $2 OFFSET $3`, userID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OrderDetail
	for rows.Next() {
		var o OrderDetail
		var status string
		if err := rows.Scan(&o.OrderNo, &status, &o.Total, &o.CreatedAt); err != nil {
			return nil, err
		}
		o.Status = Status(status)
		out = append(out, o)
	}
	return out, rows.Err()
}

// OrderByNoUnscoped reads an order without an ownership predicate.
//
// **호출자가 소유권을 이미 판정했을 때만 쓴다.** 지금 그런 곳은 하나뿐이다:
// 세션이 방금 만든 주문의 결제 화면(P-407·P-408·P-410). 그 경로에는 사용자
// 입력이 끼어들 자리가 없고, 주문번호는 세션에서 온다.
//
// 이름에 Unscoped 를 박아 둔 이유는 grep 으로 찾기 위해서다 — 소유권 없는
// 읽기가 늘어나는 것을 눈에 보이게 한다.
func (s *Store) OrderByNoUnscoped(ctx context.Context, orderNo string) (*OrderDetail, error) {
	const q = `
		SELECT id, order_no, status, total_amount, receiver_name, receiver_phone,
		       postcode, address1, address2, orderer_email, orderer_phone, created_at
		FROM orders WHERE order_no = $1`
	var o OrderDetail
	var status string
	err := s.pool.QueryRow(ctx, q, orderNo).
		Scan(&o.ID, &o.OrderNo, &status, &o.Total, &o.ReceiverName, &o.ReceiverPhone,
			&o.Postcode, &o.Address1, &o.Address2, &o.OrdererEmail, &o.OrdererPhone, &o.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	o.Status = Status(status)
	items, err := s.orderItems(ctx, o.ID)
	if err != nil {
		return nil, err
	}
	o.Items = items
	return &o, nil
}

func (s *Store) orderItems(ctx context.Context, orderID string) ([]OrderItem, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT product_name, option_label, unit_price, quantity, line_amount
		FROM order_items WHERE order_id = $1 ORDER BY created_at, id`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OrderItem
	for rows.Next() {
		var it OrderItem
		if err := rows.Scan(&it.ProductName, &it.OptionLabel, &it.UnitPrice,
			&it.Quantity, &it.LineAmount); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// GuestOrder is P-504's read: order number AND a matching contact.
//
// 대조가 **쿼리 안**에 있다 (D19 P-504). 조회한 뒤 Go 에서 비교하면 그 시점에
// 이미 남의 주문이 프로세스 안에 들어와 있고, 그 뒤의 실수 하나가 곧 유출이다.
//
// 회원 주문은 열리지 않는다 — `user_id IS NULL` 이 그것을 막는다. 회원이
// 자기 주문을 비회원 경로로 여는 길을 남기면, 그 길은 남의 회원 주문에도
// 열려 있다.
func (s *Store) GuestOrder(ctx context.Context, orderNo, phone, email string) (*OrderDetail, error) {
	if orderNo == "" || (phone == "" && email == "") {
		return nil, ErrNotFound
	}
	const q = `
		SELECT id, order_no, status, total_amount, receiver_name, receiver_phone,
		       postcode, address1, address2, orderer_email, orderer_phone, created_at
		FROM orders
		WHERE order_no = $1
		  AND user_id IS NULL
		  AND ( ($2::text IS NOT NULL AND orderer_phone = $2)
		     OR ($3::text IS NOT NULL AND orderer_email = $3) )`
	var o OrderDetail
	var status string
	err := s.pool.QueryRow(ctx, q, orderNo, nullable(phone), nullable(email)).
		Scan(&o.ID, &o.OrderNo, &status, &o.Total, &o.ReceiverName, &o.ReceiverPhone,
			&o.Postcode, &o.Address1, &o.Address2, &o.OrdererEmail, &o.OrdererPhone, &o.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	o.Status = Status(status)
	items, err := s.orderItems(ctx, o.ID)
	if err != nil {
		return nil, err
	}
	o.Items = items
	return &o, nil
}

// Shipment is one row of shipments, as P-505 draws it.
type Shipment struct {
	Kind       string
	Carrier    string
	TrackingNo string
	ShippedAt  time.Time
}

// Shipments lists an order's dispatches, newest first.
func (s *Store) Shipments(ctx context.Context, orderID string) ([]Shipment, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT kind, carrier, tracking_no, shipped_at FROM shipments
		WHERE order_id = $1 ORDER BY shipped_at DESC, id`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Shipment
	for rows.Next() {
		var sh Shipment
		if err := rows.Scan(&sh.Kind, &sh.Carrier, &sh.TrackingNo, &sh.ShippedAt); err != nil {
			return nil, err
		}
		out = append(out, sh)
	}
	return out, rows.Err()
}

var ErrShipmentExists = errors.New("commerce: 이미 최초 발송이 기록된 주문입니다")

// AllStatuses is the full vocabulary, for A-504's filter.
//
// **드롭다운이 아니다.** A-506 의 선택지는 Next() 가 낸다 — 이것은 목록을
// 거르는 필터이고, 필터에 없는 상태를 고르는 것은 아무것도 못 찾는 일이지
// 규칙 위반이 아니다.
func AllStatuses() []Status {
	out := make([]Status, 0, len(transitions))
	for s := range transitions {
		out = append(out, s)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// AdminOrders is A-504's read. status "" means every order.
func (s *Store) AdminOrders(ctx context.Context, status string, page int) ([]OrderDetail, error) {
	if status != "" && !Known(Status(status)) {
		return nil, fmt.Errorf("%w: %q", ErrUnknownStatus, status)
	}
	limit, offset := ProductQuery{Page: page}.clamp()
	rows, err := s.pool.Query(ctx, `
		SELECT order_no, status, total_amount, orderer_email, created_at
		FROM orders
		WHERE ($1::text IS NULL OR status = $1)
		ORDER BY created_at DESC, id LIMIT $2 OFFSET $3`, nullable(status), limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OrderDetail
	for rows.Next() {
		var o OrderDetail
		var st string
		if err := rows.Scan(&o.OrderNo, &st, &o.Total, &o.OrdererEmail, &o.CreatedAt); err != nil {
			return nil, err
		}
		o.Status = Status(st)
		out = append(out, o)
	}
	return out, rows.Err()
}

// TransitionOrder is A-506's write: the state machine decides, then the row moves.
//
// 잠그고 읽는다. 두 관리자가 동시에 서로 다른 전이를 하면 하나만 성공해야
// 하는데 (D14 「동시성」), 잠그지 않으면 둘 다 같은 현재 상태를 읽고 각자
// 합법인 전이를 해서 나중 것이 먼저 것을 덮는다.
func (s *Store) TransitionOrder(ctx context.Context, orderNo string, to Status, actor Actor) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var id, from string
	err = tx.QueryRow(ctx,
		`SELECT id, status FROM orders WHERE order_no = $1 FOR UPDATE`, orderNo).Scan(&id, &from)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if err := CanTransition(Status(from), to, actor); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE orders SET status = $2, updated_at = now() WHERE id = $1`, id, string(to)); err != nil {
		return err
	}
	// 배송완료 전이는 시각을 남긴다. A-512 의 반품 기간·자동 확정이 전부 이
	// 시각 기준이고, operation_logs 는 감사 흔적이지 운영 데이터가 아니다 (D30).
	if to == StatusDelivered {
		if _, err := tx.Exec(ctx,
			`UPDATE orders SET delivered_at = now() WHERE id = $1 AND delivered_at IS NULL`,
			id); err != nil {
			return err
		}
	}
	if to == StatusConfirmed {
		if _, err := tx.Exec(ctx,
			`UPDATE orders SET confirmed_at = now() WHERE id = $1 AND confirmed_at IS NULL`,
			id); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// RecordShipment is A-510's write — the first dispatch only.
//
// 상태를 함께 옮기지 않는다. 옮기면 `배송준비 → 배송중` 을 일으키는 화면이
// 둘이 되고, FR-623 이 A-516 에 대해 지적한 것과 같은 문제가 된다.
func (s *Store) RecordShipment(ctx context.Context, orderNo, carrier, tracking string,
	at time.Time) error {
	var orderID string
	err := s.pool.QueryRow(ctx, `SELECT id FROM orders WHERE order_no = $1`, orderNo).Scan(&orderID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO shipments (order_id, kind, carrier, tracking_no, shipped_at)
		VALUES ($1, '최초발송', $2, $3, $4)`, orderID, carrier, tracking, at)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrShipmentExists
	}
	return err
}
