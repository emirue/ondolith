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
	ID       string
	OrderNo  string
	Status   Status
	Goods    int
	Fee      int
	Discount int
	Total    int
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
	form OrderForm, ship Shipping, discount int, now time.Time) (*Order, error) {

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
	q := s.q.WithTx(tx)

	// (1) 장바구니를 잠근 채 읽는다. FOR UPDATE OF ci 는 장바구니 항목 행만
	// 잠근다 — 상품 행까지 잠그면 같은 상품을 보는 모든 주문이 줄을 선다.
	rows, err := q.LockCartForOrder(ctx, commerceq.LockCartForOrderParams{
		UserID: nullable(o.UserID), GuestKey: nullable(o.GuestKey)})
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
	for _, r := range rows {
		l := line{variantID: r.VariantID, productID: r.ProductID, name: r.Name, quantity: int(r.Quantity)}
		basePrice, priceDelta := int(r.BasePrice), int(r.PriceDelta)
		var opts map[string]string
		if err := unmarshalOptions(r.OptionValues, &opts); err != nil {
			return nil, err
		}
		l.optionLabel = OptionLabel(opts)

		sell := Sellable{ProductVisible: r.ProductVisible, VariantVisible: r.VariantVisible, Stock: int(r.Stock)}
		if err := sell.CheckAvailable(l.quantity); err != nil {
			return nil, fmt.Errorf("%s: %w", l.name, err)
		}
		l.unitPrice = basePrice + priceDelta

		lines = append(lines, l)
		amounts = append(amounts, Line{BasePrice: basePrice, PriceDelta: priceDelta, Quantity: l.quantity})
		deltas = append(deltas, StockDelta{VariantID: l.variantID, Delta: -l.quantity})
	}
	if len(lines) == 0 {
		return nil, ErrCartEmpty
	}

	// (2) 재고 차감. 잠금 순서가 정해져 있어 교착이 생기지 않는다.
	if err := s.AdjustStock(ctx, tx, deltas); err != nil {
		return nil, err
	}

	// (3) 금액은 서버가 계산한다. 폼에는 금액 필드가 없다 — 할인도 마찬가지다.
	// discount 는 호출자가 정한 값이고, 그 값이 어디서 오는지는 호출자의 몫이다
	// (지금은 0; 쿠폰 발급은 별도 설계다 — D50).
	goods, fee, total, err := Total(amounts, ship)
	if err != nil {
		return nil, err
	}
	// 할인을 품목에 배분해 **스냅샷으로** 저장한다 (FR-626). 환불할 때마다
	// 비례 계산을 다시 하면 반올림이 매번 달라져 마지막 품목에서 합이 안 맞는다.
	lineAmounts := make([]int, len(amounts))
	for i := range amounts {
		lineAmounts[i], _ = amounts[i].Amount()
	}
	perItem, err := Apportion(lineAmounts, discount)
	if err != nil {
		return nil, err
	}
	total -= discount
	if total < 0 {
		return nil, fmt.Errorf("%w: 총액 %d", ErrDiscountTooLarge, total)
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
		Goods: goods, Fee: fee, Discount: discount, Total: total}
	order.ID, err = q.InsertOrder(ctx, commerceq.InsertOrderParams{
		OrderNo: order.OrderNo, UserID: nullable(userID), Status: string(order.Status),
		TotalAmount: int32(total), DiscountAmount: int32(discount),
		ReceiverName: form.ReceiverName, ReceiverPhone: form.ReceiverPhone, Postcode: form.Postcode,
		Address1: form.Address1, Address2: form.Address2, DeliveryMemo: form.DeliveryMemo,
		OrdererEmail: form.OrdererEmail, OrdererPhone: form.OrdererPhone})
	if err != nil {
		return nil, err
	}

	for i, l := range lines {
		// 상품명·옵션 표기·단가는 스냅샷이다 (FR-612). FK 조인으로 대체하지
		// 않는다 — 조합이 은퇴한 뒤에도 그때 산 것이 재현돼야 한다.
		// 배분된 할인도 같은 이유로 스냅샷이다 (FR-626).
		if err := q.InsertOrderItem(ctx, commerceq.InsertOrderItemParams{
			OrderID: order.ID, ProductID: l.productID, VariantID: l.variantID, ProductName: l.name,
			OptionLabel: l.optionLabel, UnitPrice: int32(l.unitPrice), Quantity: int32(l.quantity),
			DiscountAmount: int32(perItem[i])}); err != nil {
			return nil, err
		}
	}

	// (5-b) 동의 이력. 본문을 복사하지 않는다 — terms 행이 불변이고 RESTRICT 가
	// 삭제를 막으므로 참조만으로 재현된다 (D30).
	for _, id := range form.AgreedTerms {
		if err := q.InsertOrderAgreement(ctx, commerceq.InsertOrderAgreementParams{
			OrderID: order.ID, TermsID: id}); err != nil {
			return nil, err
		}
	}

	// 장바구니를 비운다. 남겨 두면 뒤로 가기 한 번이 같은 것을 또 주문한다.
	if err := q.ClearCart(ctx, commerceq.ClearCartParams{
		UserID: nullable(o.UserID), GuestKey: nullable(o.GuestKey)}); err != nil {
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
	return commerceq.New(tx).RequiredTermIDs(ctx, now)
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
	rows, err := s.q.TermsInForce(ctx, now)
	if err != nil {
		return nil, err
	}
	var out []Term
	for _, r := range rows {
		out = append(out, Term{ID: r.ID, Kind: r.Kind, Version: r.Version, Body: r.Body, Required: r.IsRequired})
	}
	return out, nil
}

// OrderDetail is what P-410/P-502 draw, snapshots only.
type OrderDetail struct {
	ID            string
	OrderNo       string
	Status        Status
	Total         int
	Discount      int
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
	ID          string
	ProductName string
	OptionLabel string
	UnitPrice   int
	Quantity    int
	LineAmount  int
	// Discount 는 주문 생성 시 배분된 스냅샷이다 (FR-626).
	Discount int
	// Settled 는 이미 환불·반품으로 소진된 수량이다. 남은 수량은
	// Quantity - Settled 이고, 그 이상은 DB CHECK 가 막는다.
	Settled int
}

// Net is what this line is worth after its share of the discount.
func (it OrderItem) Net() int { return it.LineAmount - it.Discount }

// RemainingQty is how many **units** may still be refunded.
//
// 이름에 `Qty` 가 붙는 이유: 같은 화면(A-507)에 「남은 금액」도 있고, 둘 다
// `Remaining` 이면 금액 표시 검사가 수량까지 잡아 예외를 달게 된다 — 예외가
// 붙은 검사는 다음 화면에서 진짜 금액을 놓친다.
func (it OrderItem) RemainingQty() int { return it.Quantity - it.Settled }

// OrderByNo reads one order, scoped to who may see it.
//
// 소유권이 WHERE 절에 있다 (SC-3). userID 가 비어 있으면 비회원 경로이고,
// 그때는 주문번호만으로 열지 않고 **연락처 대조**를 함께 요구한다 (P-504) —
// 주문번호 하나로 열리면 그 번호가 곧 열쇠가 된다.
func (s *Store) OrderByNo(ctx context.Context, orderNo, userID, ordererPhone string) (*OrderDetail, error) {
	r, err := s.q.OrderByNo(ctx, commerceq.OrderByNoParams{
		OrderNo: orderNo, UserID: nullable(userID), OrdererPhone: nullable(ordererPhone)})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return s.withItems(ctx, orderDetailFromRow(commerceq.OrderByNoUnscopedRow(r)))
}

// orderDetailFromRow converts the 13-column order read. 세 질의(OrderByNo·
// OrderByNoUnscoped·GuestOrder)의 행이 같은 모양이라 구조체 변환으로 여기 온다.
func orderDetailFromRow(r commerceq.OrderByNoUnscopedRow) OrderDetail {
	return OrderDetail{ID: r.ID, OrderNo: r.OrderNo, Status: Status(r.Status),
		Total: int(r.TotalAmount), Discount: int(r.DiscountAmount),
		ReceiverName: r.ReceiverName, ReceiverPhone: r.ReceiverPhone, Postcode: r.Postcode,
		Address1: r.Address1, Address2: r.Address2, OrdererEmail: r.OrdererEmail,
		OrdererPhone: r.OrdererPhone, CreatedAt: r.CreatedAt}
}

func (s *Store) withItems(ctx context.Context, o OrderDetail) (*OrderDetail, error) {
	items, err := s.orderItems(ctx, o.ID)
	if err != nil {
		return nil, err
	}
	o.Items = items
	return &o, nil
}

// MyOrders is P-501.
func (s *Store) MyOrders(ctx context.Context, userID string, page int) ([]OrderDetail, error) {
	if userID == "" {
		return nil, ErrNotFound
	}
	limit, offset := ProductQuery{Page: page}.clamp()
	rows, err := s.q.MyOrders(ctx, commerceq.MyOrdersParams{
		UserID: &userID, Limit: int32(limit), Offset: int32(offset)})
	if err != nil {
		return nil, err
	}
	var out []OrderDetail
	for _, r := range rows {
		out = append(out, OrderDetail{OrderNo: r.OrderNo, Status: Status(r.Status),
			Total: int(r.TotalAmount), CreatedAt: r.CreatedAt})
	}
	return out, nil
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
	r, err := s.q.OrderByNoUnscoped(ctx, orderNo)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return s.withItems(ctx, orderDetailFromRow(r))
}

func (s *Store) orderItems(ctx context.Context, orderID string) ([]OrderItem, error) {
	rows, err := s.q.OrderItems(ctx, orderID)
	if err != nil {
		return nil, err
	}
	var out []OrderItem
	for _, r := range rows {
		out = append(out, OrderItem{ID: r.ID, ProductName: r.ProductName, OptionLabel: r.OptionLabel,
			UnitPrice: int(r.UnitPrice), Quantity: int(r.Quantity), LineAmount: int(r.LineAmount),
			Discount: int(r.DiscountAmount), Settled: int(r.SettledQuantity)})
	}
	return out, nil
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
	r, err := s.q.GuestOrder(ctx, commerceq.GuestOrderParams{
		OrderNo: orderNo, Phone: nullable(phone), Email: nullable(email)})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return s.withItems(ctx, orderDetailFromRow(commerceq.OrderByNoUnscopedRow(r)))
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
	rows, err := s.q.Shipments(ctx, orderID)
	if err != nil {
		return nil, err
	}
	var out []Shipment
	for _, r := range rows {
		out = append(out, Shipment{Kind: r.Kind, Carrier: r.Carrier, TrackingNo: r.TrackingNo, ShippedAt: r.ShippedAt})
	}
	return out, nil
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
	rows, err := s.q.AdminOrders(ctx, commerceq.AdminOrdersParams{
		Status: nullable(status), Limit: int32(limit), Offset: int32(offset)})
	if err != nil {
		return nil, err
	}
	var out []OrderDetail
	for _, r := range rows {
		out = append(out, OrderDetail{OrderNo: r.OrderNo, Status: Status(r.Status),
			Total: int(r.TotalAmount), OrdererEmail: r.OrdererEmail, CreatedAt: r.CreatedAt})
	}
	return out, nil
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
	q := s.q.WithTx(tx)

	row, err := q.LockOrderByNo(ctx, orderNo)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	id, from := row.ID, row.Status
	if err := CanTransition(Status(from), to, actor); err != nil {
		return err
	}
	if err := q.SetOrderStatus(ctx, commerceq.SetOrderStatusParams{ID: id, Status: string(to)}); err != nil {
		return err
	}
	// 배송완료 전이는 시각을 남긴다. A-512 의 반품 기간·자동 확정이 전부 이
	// 시각 기준이고, operation_logs 는 감사 흔적이지 운영 데이터가 아니다 (D30).
	if to == StatusDelivered {
		if err := q.MarkDelivered(ctx, id); err != nil {
			return err
		}
	}
	if to == StatusConfirmed {
		if err := q.MarkConfirmed(ctx, id); err != nil {
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
	orderID, err := s.q.OrderIDByNo(ctx, orderNo)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	err = s.q.InsertFirstShipment(ctx, commerceq.InsertFirstShipmentParams{
		OrderID: orderID, Carrier: carrier, TrackingNo: tracking, ShippedAt: at})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrShipmentExists
	}
	return err
}

// ExpirePendingOrders moves orders that can no longer be paid to 결제실패 and
// puts their stock back (D14: 결제대기 → 결제실패, 시스템 「10분 만료」).
//
// 결제대기 주문은 생성 시각 기준 AuthWindow 가 지나면 P-408 이 승인을 거부한다
// (ConfirmPayment ③). 그 뒤로는 어떤 경로로도 결제될 수 없는데, 그 주문이 차감한
// 재고는 잡힌 채였다 — 결제하지 않고 떠난 장바구니마다 재고가 하나씩 사라졌다.
//
// 살아 있는 '대기' 결제 행이 있는 주문은 건드리지 않는다: 승인 결과가 불명인
// 것(ErrPaymentUnknown)이고, 그쪽은 A-508 대사가 사람의 손으로 닫는다. 가상계좌는
// 입금대기라 여기 오지 않는다.
func (s *Store) ExpirePendingOrders(ctx context.Context, before time.Time, limit int) (int, error) {
	ids, err := s.q.ExpirablePendingOrders(ctx, commerceq.ExpirablePendingOrdersParams{
		Status: string(StatusPaymentPending), Before: before, Limit: int32(limit)})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, id := range ids {
		if err := s.expireOne(ctx, id); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func (s *Store) expireOne(ctx context.Context, orderID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := s.q.WithTx(tx)
	status, err := q.LockOrderStatus(ctx, orderID)
	if err != nil {
		return err
	}
	if Status(status) != StatusPaymentPending {
		return nil // 목록을 뽑은 뒤 결제됐다. 손대지 않는다
	}
	// A confirmation can reserve a payment after the expiry list was read.
	// Recheck under the same order lock ConfirmPayment uses for reservation.
	pending, err := q.HasPendingOrderPayment(ctx, orderID)
	if err != nil {
		return err
	}
	if pending {
		return nil
	}
	if err := CanTransition(StatusPaymentPending, StatusPaymentFailed, ActorSystem); err != nil {
		return err
	}
	deltas, err := restockDeltas(ctx, tx, orderID)
	if err != nil {
		return err
	}
	if err := s.AdjustStock(ctx, tx, deltas); err != nil {
		return err
	}
	if _, err := q.MoveOrderStatus(ctx, commerceq.MoveOrderStatusParams{
		ID: orderID, ToStatus: string(StatusPaymentFailed), FromStatus: string(StatusPaymentPending)}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// OpenOrders counts orders that have not reached a terminal state — what D13's
// FR-710 전환 규칙 calls 미완결. A-201 refuses `shop → cms` while any exist:
// unregistering the commerce routes with a buyer mid-payment strands them.
//
// 종료 상태 목록은 상태머신에서 뽑는다. 손으로 적으면 상태가 늘 때 낡는다.
func (s *Store) OpenOrders(ctx context.Context) (int, error) {
	var terminal []string
	for st := range transitions {
		if Terminal(st) {
			terminal = append(terminal, string(st))
		}
	}
	n, err := s.q.OpenOrders(ctx, terminal)
	return int(n), err
}
