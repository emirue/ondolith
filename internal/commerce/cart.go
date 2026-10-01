package commerce

import (
	"context"
	"errors"

	"github.com/emirue/ondolith/internal/commerce/commerceq"
	"github.com/jackc/pgx/v5"
)

// CartOwner is who a cart belongs to — exactly one of the two (D30 carts_owner_is_one).
//
// 구조체로 묶은 이유: 두 인자를 따로 받으면 둘 다 넘기거나 둘 다 비운 호출이
// 컴파일된다. DB 의 CHECK 가 그것을 잡지만, 잡히는 곳은 화면에서 500 이다.
type CartOwner struct {
	UserID   string
	GuestKey string
}

// Valid reports whether exactly one side is set.
func (o CartOwner) Valid() bool {
	return (o.UserID == "") != (o.GuestKey == "")
}

var ErrCartOwner = errors.New("commerce: 장바구니 주인은 회원이거나 비회원이거나 하나입니다")

// CartItem is one line of a cart, joined with what the screen needs.
type CartItem struct {
	ID        string
	VariantID string
	ProductID string
	// Slug 는 상품 화면(P-303)의 주소다. ProductID 로 링크를 그리면 라우트가
	// `/shop/p/{slug}` 이므로 눌렀을 때 404 가 난다 — 실제로 그랬다.
	Slug      string
	Name      string
	Option    map[string]string
	UnitPrice int
	Quantity  int
	Stock     int
	Sellable  bool
}

// cartID finds or creates the owner's cart.
//
// ON CONFLICT DO UPDATE 로 쓰는 이유는 RETURNING 때문이다. DO NOTHING 은 충돌
// 시 아무 행도 돌려주지 않아서 "만들었으면 id, 아니면 다시 SELECT" 라는 두 번째
// 왕복이 생기고, 그 사이에 다른 요청이 지울 수 있다.
func (s *Store) cartID(ctx context.Context, tx pgx.Tx, o CartOwner) (string, error) {
	if !o.Valid() {
		return "", ErrCartOwner
	}
	if o.UserID != "" {
		return s.q.WithTx(tx).CartIDForUser(ctx, &o.UserID)
	}
	return s.q.WithTx(tx).CartIDForGuest(ctx, &o.GuestKey)
}

// AddToCart puts `qty` of a variant into the owner's cart.
//
// 재고 검사가 여기서도 일어난다 (D50: 백오더 없음). 담기 시점에 확인하지 않으면
// 품절 상품이 장바구니에 앉아 있다가 주문 화면에서야 거부되고, 사용자는 무엇이
// 문제인지 그때 안다.
//
// 담기는 재고를 **차감하지 않는다.** 차감은 주문 생성(P-406)이 한다 — 장바구니가
// 재고를 잡으면 담아 두고 안 사는 사람이 품절을 만든다.
func (s *Store) AddToCart(ctx context.Context, o CartOwner, variantID string, qty int) error {
	// 조합 읽기는 트랜잭션 **앞**이다. 안에서 풀로 읽으면 요청 하나가
	// 커넥션 둘을 잡고(풀 상한 4), 그 읽기는 어차피 트랜잭션 밖의 스냅샷이다
	// — 재고 확인은 안내용이고 차감은 주문 생성이 잠그고 한다 (stock.go).
	_, sell, err := s.VariantForPurchase(ctx, variantID)
	if err != nil {
		return err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := s.q.WithTx(tx)

	cart, err := s.cartID(ctx, tx, o)
	if err != nil {
		return err
	}

	// 같은 조합은 한 행이고 수량이 는다 (D30 cart_items_variant_uniq). 합친
	// 뒤의 수량으로 재고를 확인해야 한다 — 3개씩 두 번 담는 것과 6개를 한 번에
	// 담는 것은 같은 요청이다.
	existing, err := q.CartItemQuantity(ctx, commerceq.CartItemQuantityParams{CartID: cart, VariantID: variantID})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	total := int(existing) + qty
	if err := sell.CheckAvailable(total); err != nil {
		return err
	}

	if err := q.UpsertCartItem(ctx, commerceq.UpsertCartItemParams{
		CartID: cart, VariantID: variantID, Quantity: int32(total)}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// CartItems reads the owner's cart with everything the screen draws.
//
// 한 문장이다. 항목마다 상품을 다시 읽으면 장바구니 열 줄이 쿼리 열한 번이 된다.
func (s *Store) CartItems(ctx context.Context, o CartOwner) ([]CartItem, error) {
	if !o.Valid() {
		return nil, ErrCartOwner
	}
	rows, err := s.q.CartItems(ctx, commerceq.CartItemsParams{
		UserID: nullable(o.UserID), GuestKey: nullable(o.GuestKey)})
	if err != nil {
		return nil, err
	}
	var out []CartItem
	for _, r := range rows {
		it := CartItem{ID: r.ID, VariantID: r.VariantID, ProductID: r.ProductID, Slug: r.Slug, Name: r.Name,
			UnitPrice: int(r.UnitPrice), Quantity: int(r.Quantity), Stock: int(r.Stock), Sellable: r.Sellable}
		if err := unmarshalOptions(r.OptionValues, &it.Option); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, nil
}

// UpdateCartItem changes a quantity. qty 0 은 삭제다.
//
// 소유권이 WHERE 절에 있다 (SC-3). 항목 ID 만으로 지우면 남의 장바구니를 비울
// 수 있고, 먼저 SELECT 해서 주인을 확인하는 방식은 그 사이에 주인이 바뀔 수
// 있는 데다 확인을 잊은 경로가 생긴다 — 한 문장이면 잊을 곳이 없다. 세 질의
// (queries/cart.sql) 가 같은 소유권 조각을 각자 적고 있다.
func (s *Store) UpdateCartItem(ctx context.Context, o CartOwner, itemID string, qty int) error {
	if !o.Valid() {
		return ErrCartOwner
	}
	user, guest := nullable(o.UserID), nullable(o.GuestKey)

	if qty <= 0 {
		n, err := s.q.DeleteCartItem(ctx, commerceq.DeleteCartItemParams{ID: itemID, UserID: user, GuestKey: guest})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		return nil
	}

	// 재고를 넘는 수량은 거부한다. 화면이 이미 막지만, 폼은 브라우저가 건너뛸
	// 수 있고 이 경로가 마지막이다.
	variantID, err := s.q.CartItemVariant(ctx, commerceq.CartItemVariantParams{ID: itemID, UserID: user, GuestKey: guest})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if _, sell, err := s.VariantForPurchase(ctx, variantID); err != nil {
		return err
	} else if err := sell.CheckAvailable(qty); err != nil {
		return err
	}

	n, err := s.q.SetCartItemQuantity(ctx, commerceq.SetCartItemQuantityParams{
		ID: itemID, UserID: user, GuestKey: guest, Quantity: int32(qty)})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// MergeCarts folds a guest cart into a member's at login (D50).
//
// 더하되 재고 상한에서 자른다 (MergeQuantity). 합친 뒤 비회원 쪽은 비운다 —
// 남겨 두면 로그아웃한 같은 브라우저가 예전 것을 다시 본다.
func (s *Store) MergeCarts(ctx context.Context, guestKey, userID string) error {
	if guestKey == "" || userID == "" {
		return ErrCartOwner
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := s.q.WithTx(tx)

	guestCart, err := q.GuestCartID(ctx, &guestKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // 합칠 것이 없다
	}
	if err != nil {
		return err
	}
	memberCart, err := s.cartID(ctx, tx, CartOwner{UserID: userID})
	if err != nil {
		return err
	}

	rows, err := q.MergePlan(ctx, commerceq.MergePlanParams{GuestCart: guestCart, MemberCart: memberCart})
	if err != nil {
		return err
	}
	for _, r := range rows {
		qty := MergeQuantity(int(r.GuestQuantity), int(r.MemberQuantity), int(r.Stock))
		if qty <= 0 {
			continue
		}
		if err := q.UpsertCartItem(ctx, commerceq.UpsertCartItemParams{
			CartID: memberCart, VariantID: r.VariantID, Quantity: int32(qty)}); err != nil {
			return err
		}
	}
	// CASCADE 가 항목을 함께 지운다.
	if err := q.DeleteCart(ctx, guestCart); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// nullable turns "" into a SQL NULL so one query can serve both owner kinds.
// 생성 코드가 nullable 인자를 포인터로 받는 모양 그대로다 (content 의 strPtr).
func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
