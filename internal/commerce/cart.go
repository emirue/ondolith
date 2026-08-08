package commerce

import (
	"context"
	"errors"

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
	var id string
	var q string
	var arg any
	if o.UserID != "" {
		q = `INSERT INTO carts (user_id) VALUES ($1)
		     ON CONFLICT (user_id) WHERE user_id IS NOT NULL
		     DO UPDATE SET updated_at = now() RETURNING id`
		arg = o.UserID
	} else {
		q = `INSERT INTO carts (guest_key) VALUES ($1)
		     ON CONFLICT (guest_key) WHERE guest_key IS NOT NULL
		     DO UPDATE SET updated_at = now() RETURNING id`
		arg = o.GuestKey
	}
	err := tx.QueryRow(ctx, q, arg).Scan(&id)
	return id, err
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
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	_, sell, err := s.VariantForPurchase(ctx, variantID)
	if err != nil {
		return err
	}

	cart, err := s.cartID(ctx, tx, o)
	if err != nil {
		return err
	}

	// 같은 조합은 한 행이고 수량이 는다 (D30 cart_items_variant_uniq). 합친
	// 뒤의 수량으로 재고를 확인해야 한다 — 3개씩 두 번 담는 것과 6개를 한 번에
	// 담는 것은 같은 요청이다.
	var existing int
	err = tx.QueryRow(ctx,
		`SELECT quantity FROM cart_items WHERE cart_id = $1 AND variant_id = $2`,
		cart, variantID).Scan(&existing)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	total := existing + qty
	if err := sell.CheckAvailable(total); err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO cart_items (cart_id, variant_id, quantity) VALUES ($1, $2, $3)
		ON CONFLICT (cart_id, variant_id)
		DO UPDATE SET quantity = $3, updated_at = now()`,
		cart, variantID, total); err != nil {
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
	const q = `
		SELECT ci.id, ci.variant_id, p.id, p.slug, p.name, v.option_values,
		       p.base_price + v.price_delta, ci.quantity, v.stock,
		       (p.is_visible AND v.is_visible AND v.stock >= ci.quantity)
		FROM cart_items ci
		JOIN carts c            ON c.id = ci.cart_id
		JOIN product_variants v ON v.id = ci.variant_id
		JOIN products p         ON p.id = v.product_id
		WHERE ($1::uuid IS NOT NULL AND c.user_id = $1)
		   OR ($2::text IS NOT NULL AND c.guest_key = $2)
		ORDER BY ci.created_at, ci.id`
	rows, err := s.pool.Query(ctx, q, nullable(o.UserID), nullable(o.GuestKey))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []CartItem
	for rows.Next() {
		var it CartItem
		var raw []byte
		if err := rows.Scan(&it.ID, &it.VariantID, &it.ProductID, &it.Slug, &it.Name, &raw,
			&it.UnitPrice, &it.Quantity, &it.Stock, &it.Sellable); err != nil {
			return nil, err
		}
		if err := unmarshalOptions(raw, &it.Option); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// UpdateCartItem changes a quantity. qty 0 은 삭제다.
//
// 소유권이 WHERE 절에 있다 (SC-3). 항목 ID 만으로 지우면 남의 장바구니를 비울
// 수 있고, 먼저 SELECT 해서 주인을 확인하는 방식은 그 사이에 주인이 바뀔 수
// 있는 데다 확인을 잊은 경로가 생긴다 — 한 문장이면 잊을 곳이 없다.
func (s *Store) UpdateCartItem(ctx context.Context, o CartOwner, itemID string, qty int) error {
	if !o.Valid() {
		return ErrCartOwner
	}
	const owned = `
		ci.cart_id IN (SELECT id FROM carts
		               WHERE ($2::uuid IS NOT NULL AND user_id = $2)
		                  OR ($3::text IS NOT NULL AND guest_key = $3))`

	if qty <= 0 {
		tag, err := s.pool.Exec(ctx,
			`DELETE FROM cart_items ci WHERE ci.id = $1 AND `+owned,
			itemID, nullable(o.UserID), nullable(o.GuestKey))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	}

	// 재고를 넘는 수량은 거부한다. 화면이 이미 막지만, 폼은 브라우저가 건너뛸
	// 수 있고 이 경로가 마지막이다.
	var variantID string
	err := s.pool.QueryRow(ctx,
		`SELECT ci.variant_id FROM cart_items ci WHERE ci.id = $1 AND `+owned,
		itemID, nullable(o.UserID), nullable(o.GuestKey)).Scan(&variantID)
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

	tag, err := s.pool.Exec(ctx,
		`UPDATE cart_items ci SET quantity = $4, updated_at = now()
		 WHERE ci.id = $1 AND `+owned,
		itemID, nullable(o.UserID), nullable(o.GuestKey), qty)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
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

	var guestCart string
	err = tx.QueryRow(ctx, `SELECT id FROM carts WHERE guest_key = $1`, guestKey).Scan(&guestCart)
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

	rows, err := tx.Query(ctx, `
		SELECT gi.variant_id, gi.quantity, COALESCE(mi.quantity, 0), v.stock
		FROM cart_items gi
		JOIN product_variants v ON v.id = gi.variant_id
		LEFT JOIN cart_items mi ON mi.cart_id = $2 AND mi.variant_id = gi.variant_id
		WHERE gi.cart_id = $1`, guestCart, memberCart)
	if err != nil {
		return err
	}
	type merged struct {
		variant string
		qty     int
	}
	var plan []merged
	for rows.Next() {
		var variant string
		var guestQty, memberQty, stock int
		if err := rows.Scan(&variant, &guestQty, &memberQty, &stock); err != nil {
			rows.Close()
			return err
		}
		plan = append(plan, merged{variant, MergeQuantity(guestQty, memberQty, stock)})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, m := range plan {
		if m.qty <= 0 {
			continue
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO cart_items (cart_id, variant_id, quantity) VALUES ($1, $2, $3)
			ON CONFLICT (cart_id, variant_id)
			DO UPDATE SET quantity = $3, updated_at = now()`,
			memberCart, m.variant, m.qty); err != nil {
			return err
		}
	}
	// CASCADE 가 항목을 함께 지운다.
	if _, err := tx.Exec(ctx, `DELETE FROM carts WHERE id = $1`, guestCart); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// nullable turns "" into a SQL NULL so one query can serve both owner kinds.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
