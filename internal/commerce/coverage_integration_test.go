package commerce

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// 이 파일은 어떤 테스트도 실행한 적 없던 생성 질의를 실제 PostgreSQL 위에서
// 돌린다 (D22 7절). 기대값은 queries/*.sql 의 문장에서 읽은 것이다.

const noSuchID = "00000000-0000-0000-0000-000000000000"

// 최초 발송은 주문당 한 번이다 — 막는 것은 부분 유니크(shipments_first_idx)다.
func TestRecordShipmentIsOncePerOrder(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	orderNo, _ := seedOrder(t, s, pool, "tee", 1)
	otherNo, _ := seedOrder(t, s, pool, "cap", 1)
	order, err := s.OrderByNoUnscoped(ctx, orderNo)
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.OrderByNoUnscoped(ctx, otherNo)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)

	if err := s.RecordShipment(ctx, "NO-SUCH-ORDER", "한진", "111", at); !errors.Is(err, ErrNotFound) {
		t.Errorf("없는 주문번호 = %v, want ErrNotFound", err)
	}
	if err := s.RecordShipment(ctx, orderNo, "한진", "1234-5678", at); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordShipment(ctx, orderNo, "롯데", "9999", at.Add(time.Hour)); !errors.Is(err, ErrShipmentExists) {
		t.Errorf("두 번째 최초발송 = %v, want ErrShipmentExists", err)
	}

	got, err := s.Shipments(ctx, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("발송 %d건, want 1건", len(got))
	}
	if got[0].Kind != "최초발송" {
		t.Errorf("종류 %q, want 최초발송", got[0].Kind)
	}
	if got[0].Carrier != "한진" {
		t.Errorf("택배사 %q, want 한진", got[0].Carrier)
	}
	if got[0].TrackingNo != "1234-5678" {
		t.Errorf("송장 %q, want 1234-5678", got[0].TrackingNo)
	}
	if !got[0].ShippedAt.Equal(at) {
		t.Errorf("발송 시각 %v, want %v", got[0].ShippedAt, at)
	}
	if rest, err := s.Shipments(ctx, other.ID); err != nil || len(rest) != 0 {
		t.Errorf("다른 주문의 발송 = %d건, %v — 남의 주문에 기록됐다", len(rest), err)
	}
}

// 내 주문 목록에는 내 주문만, 최신순으로, 20건씩 나온다.
func TestMyOrdersAreMineNewestFirstAndPaged(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	me := mkUser(t, pool, "me@example.com")
	stranger := mkUser(t, pool, "stranger@example.com")
	_, variant := seedProduct(t, pool, "tee", 12000, 0, 100)

	place := func(owner CartOwner) string {
		t.Helper()
		if err := s.AddToCart(ctx, owner, variant, 1); err != nil {
			t.Fatal(err)
		}
		o, err := s.CreateOrder(ctx, owner, owner.UserID, testForm(), testShipping, 0, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return o.OrderNo
	}
	theirs := map[string]bool{
		place(CartOwner{UserID: stranger}):                true,
		place(CartOwner{GuestKey: "guest-0123456789abc"}): true,
	}
	var mine []string // 만든 순서
	for range 21 {
		mine = append(mine, place(CartOwner{UserID: me}))
	}

	page1, err := s.MyOrders(ctx, me, 1)
	if err != nil {
		t.Fatal(err)
	}
	page2, err := s.MyOrders(ctx, me, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page1) != 20 {
		t.Fatalf("1쪽 %d건, want 20건", len(page1))
	}
	if len(page2) != 1 {
		t.Fatalf("2쪽 %d건, want 1건", len(page2))
	}
	// 최신순: 만든 순서를 뒤집은 것과 같아야 한다.
	for i, o := range append(page1, page2...) {
		if theirs[o.OrderNo] {
			t.Errorf("남의 주문 %s 이 내 목록에 있다", o.OrderNo)
		}
		if want := mine[len(mine)-1-i]; o.OrderNo != want {
			t.Errorf("%d번째 = %s, want %s — 최신순이 아니다", i, o.OrderNo, want)
		}
	}
	if page1[0].Total != 15000 {
		t.Errorf("합계 %d, want 15000", page1[0].Total)
	}
	if page1[0].Status != StatusPaymentPending {
		t.Errorf("상태 %q, want %q", page1[0].Status, StatusPaymentPending)
	}
}

func paymentStatus(t *testing.T, s *Store, orderNo string) string {
	t.Helper()
	var status string
	if err := s.pool.QueryRow(context.Background(), `
		SELECT p.status FROM payments p JOIN orders o ON o.id = p.order_id
		WHERE o.order_no = $1`, orderNo).Scan(&status); err != nil {
		t.Fatal(err)
	}
	return status
}

// FailPayment 는 **그 주문의 '대기' 결제만** '실패' 로 내린다.
func TestFailPaymentFlipsOnlyThisOrdersPendingPayment(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	limbo := func(slug string) string {
		t.Helper()
		orderNo, total := seedOrder(t, s, pool, slug, 1)
		unknown := &fakeGateway{err: ErrPaymentUnknown}
		if _, err := s.ConfirmPayment(ctx, unknown, "toss", orderNo, "pk-"+slug, total, time.Now()); !errors.Is(err, ErrPaymentUnknown) {
			t.Fatalf("= %v, want ErrPaymentUnknown", err)
		}
		if got := paymentStatus(t, s, orderNo); got != "대기" {
			t.Fatalf("준비: 결제 상태 %q, want 대기", got)
		}
		return orderNo
	}
	target, bystander := limbo("tee"), limbo("cap")
	paid, _, _ := paidOrder(t, s, pool, "mug", 1)
	unpaid, _ := seedOrder(t, s, pool, "sock", 1)

	if err := s.FailPayment(ctx, target, "사용자 취소"); err != nil {
		t.Fatal(err)
	}
	if got := paymentStatus(t, s, target); got != "실패" {
		t.Errorf("대상 결제 %q, want 실패", got)
	}
	if got := paymentStatus(t, s, bystander); got != "대기" {
		t.Errorf("다른 주문의 결제 %q, want 대기 — 남의 결제를 내렸다", got)
	}
	if err := s.FailPayment(ctx, target, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("이미 실패한 결제 = %v, want ErrNotFound", err)
	}
	if err := s.FailPayment(ctx, unpaid, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("결제 시도가 없는 주문 = %v, want ErrNotFound", err)
	}
	if err := s.FailPayment(ctx, paid, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("승인된 결제 = %v, want ErrNotFound", err)
	}
	if got := paymentStatus(t, s, paid); got != "승인" {
		t.Errorf("승인된 결제가 %q 이 됐다 — 받은 돈을 실패로 적었다", got)
	}
}

func TestUpdateProductChangesOneRow(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	id := mkProduct(t, s, "mat", 12000)
	otherID := mkProduct(t, s, "cushion", 8000)

	want := Product{ID: id, Slug: "mat-2", Name: "새 매트", Description: "두꺼운 것", BasePrice: 15000, Visible: false}
	if err := s.UpdateProduct(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.ProductByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Slug != want.Slug {
		t.Errorf("slug %q, want %q", got.Slug, want.Slug)
	}
	if got.Name != want.Name {
		t.Errorf("이름 %q, want %q", got.Name, want.Name)
	}
	if got.Description != want.Description {
		t.Errorf("설명 %q, want %q", got.Description, want.Description)
	}
	if got.BasePrice != want.BasePrice {
		t.Errorf("기본가 %d, want %d", got.BasePrice, want.BasePrice)
	}
	if got.Visible {
		t.Error("노출을 껐는데 켜져 있다")
	}
	other, err := s.ProductByID(ctx, otherID)
	if err != nil {
		t.Fatal(err)
	}
	if other.Slug != "cushion" || other.BasePrice != 8000 {
		t.Errorf("다른 상품이 바뀌었다: %+v", other)
	}

	want.Slug = "cushion"
	if err := s.UpdateProduct(ctx, want); !errors.Is(err, ErrSlugTaken) {
		t.Errorf("겹치는 slug = %v, want ErrSlugTaken", err)
	}
	want.ID, want.Slug = noSuchID, "ghost"
	if err := s.UpdateProduct(ctx, want); !errors.Is(err, ErrNotFound) {
		t.Errorf("없는 상품 = %v, want ErrNotFound", err)
	}
}

// 교환 후보는 같은 상품의, 주문한 조합이 아닌, 노출 중이고 재고가 있는 조합이다.
// 차액이 작은 것부터 나온다.
func TestVariantsForExchangeAreSellableSiblings(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	// 주문한 조합: 크기 L, 차액 1000. 주문 뒤에도 재고가 남고 노출 중이다 —
	// 후보에서 빠지는 이유는 「주문한 조합」이라는 것 하나뿐이다.
	productID, ordered := seedProduct(t, pool, "tee", 12000, 1000, 10)
	owner := CartOwner{GuestKey: "guest-0123456789abc"}
	if err := s.AddToCart(ctx, owner, ordered, 1); err != nil {
		t.Fatal(err)
	}
	order, err := s.CreateOrder(ctx, owner, "", testForm(), testShipping, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	item := itemsOf(t, s, order.OrderNo)[0]
	otherProduct, _ := seedProduct(t, pool, "cap", 9000, 0, 5)

	mk := func(productID, size string, delta, stock int, visible bool) string {
		t.Helper()
		var id string
		if err := pool.QueryRow(ctx, `
			INSERT INTO product_variants (product_id, option_values, price_delta, stock, is_visible)
			VALUES ($1, jsonb_build_object('크기', $2::text), $3, $4, $5) RETURNING id`,
			productID, size, delta, stock, visible).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	dear := mk(productID, "XL", 2000, 5, true)
	cheap := mk(productID, "S", 0, 3, true)
	mk(productID, "XS", 500, 0, true)      // 품절
	mk(productID, "M", 500, 5, false)      // 미노출
	mk(otherProduct, "FREE", 500, 5, true) // 다른 상품

	got, err := s.VariantsForExchange(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, v := range got {
		ids = append(ids, v.ID)
		if v.ID == ordered {
			t.Error("주문한 조합이 교환 후보에 있다")
		}
		if v.ProductID != productID {
			t.Errorf("다른 상품의 조합 %s 이 후보에 있다", v.ID)
		}
		if !v.Visible {
			t.Errorf("미노출 조합 %v 이 후보에 있다", v.OptionValues)
		}
		if v.Stock <= 0 {
			t.Errorf("품절 조합 %v 이 후보에 있다", v.OptionValues)
		}
	}
	if fmt.Sprint(ids) != fmt.Sprint([]string{cheap, dear}) {
		t.Fatalf("후보 %v, want [S XL] = %v — 차액 오름차순", ids, []string{cheap, dear})
	}
	if got[0].OptionValues["크기"] != "S" || got[0].PriceDelta != 0 || got[0].Stock != 3 {
		t.Errorf("첫 후보 %+v, want 크기 S·차액 0·재고 3", got[0])
	}
}

// 상품 검색: 접두 일치, 미노출 제외, 순위 내림차순, 20건씩.
func TestSearchProductsPrefixRankAndPaging(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	mk := func(id, slug, name, desc string, visible bool) {
		t.Helper()
		if _, err := pool.Exec(ctx, `
			INSERT INTO products (id, slug, name, description, base_price, is_visible)
			VALUES (COALESCE(NULLIF($1,'')::uuid, gen_random_uuid()), $2, $3, $4, 1000, $5)`,
			id, slug, name, desc, visible); err != nil {
			t.Fatal(err)
		}
	}
	// id 순서가 순위와 **반대**다 — 순위 정렬이 빠지면 once 가 먼저 나온다.
	mk("00000000-0000-0000-0000-000000000001", "once", "온돌매트를 판다", "", true)
	mk("ffffffff-ffff-ffff-ffff-ffffffffffff", "thrice", "온돌매트 온돌매트", "온돌매트", true)
	mk("", "hidden", "온돌매트 숨김", "", false)
	mk("", "unrelated", "방석", "푹신하다", true)

	got, err := s.SearchProducts(ctx, "온돌", 1)
	if err != nil {
		t.Fatal(err)
	}
	var slugs []string
	for _, p := range got {
		slugs = append(slugs, p.Slug)
	}
	// "온돌" 은 저장된 토큰 "온돌매트를" 의 접두다. 미노출·무관 상품은 없다.
	if fmt.Sprint(slugs) != "[thrice once]" {
		t.Errorf("검색 결과 %v, want [thrice once]", slugs)
	}
	if none, err := s.SearchProducts(ctx, "없는말", 1); err != nil || len(none) != 0 {
		t.Errorf("맞지 않는 검색어 = %d건, %v, want 0건", len(none), err)
	}
	// tsquery 문법 문자는 검색어가 되지 못한다 — 오류가 아니라 같은 결과다.
	if safe, err := s.SearchProducts(ctx, "온돌 & !(", 1); err != nil || len(safe) != 2 {
		t.Errorf("문법 문자가 섞인 검색어 = %d건, %v, want 2건", len(safe), err)
	}

	for i := range 21 {
		mk("", fmt.Sprintf("page-%02d", i), "쪽나눔 상품", "", true)
	}
	page1, err := s.SearchProducts(ctx, "쪽나눔", 1)
	if err != nil {
		t.Fatal(err)
	}
	page2, err := s.SearchProducts(ctx, "쪽나눔", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page1) != 20 {
		t.Errorf("1쪽 %d건, want 20건", len(page1))
	}
	if len(page2) != 1 {
		t.Fatalf("2쪽 %d건, want 1건", len(page2))
	}
	for _, p := range page1 {
		if p.ID == page2[0].ID {
			t.Errorf("%s 가 두 쪽에 다 나온다", p.Slug)
		}
	}
}

func TestTermsByID(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	mkTerm(t, pool, "이용약관", true)
	id := mkTerm(t, pool, "마케팅수신", false)

	got, err := s.TermsByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != id {
		t.Errorf("id %s, want %s", got.ID, id)
	}
	if got.Kind != "마케팅수신" {
		t.Errorf("종류 %q, want 마케팅수신", got.Kind)
	}
	if got.Version != "v1" || got.Body != "본문" {
		t.Errorf("버전·본문 (%q, %q), want (v1, 본문)", got.Version, got.Body)
	}
	if got.Required {
		t.Error("선택 약관이 필수로 읽혔다")
	}
	if _, err := s.TermsByID(ctx, noSuchID); !errors.Is(err, ErrNotFound) {
		t.Errorf("없는 약관 = %v, want ErrNotFound", err)
	}
}
