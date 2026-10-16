package app

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// **두 상품을 함께 주문해도 두 번째 상품을 교환할 수 있다** (FR-618).
//
// 교환 폼이 첫 품목 상품의 옵션만 보여 줘서, 매트와 베개를 함께 산 사람은 매트를
// 베개 옵션으로만 요청할 수 있었고 서버는 그것을 「같은 상품만」으로 거절했다.
func TestExchangeFormOffersOptionsForEveryItem(t *testing.T) {
	srv, pool, _ := shopSite(t)
	ctx := context.Background()
	userID := mkUser(t, pool, "swap2@example.com")

	// 상품마다 산 조합과 바꿀 조합이 하나씩 있다.
	product := func(slug, name, bought, other string) (string, string, string) {
		var pid, b, o string
		if err := pool.QueryRow(ctx, `INSERT INTO products (slug,name,base_price,is_visible)
			VALUES ($1,$2,10000,true) RETURNING id`, slug, name).Scan(&pid); err != nil {
			t.Fatal(err)
		}
		for _, v := range []struct {
			opt string
			id  *string
		}{{bought, &b}, {other, &o}} {
			if err := pool.QueryRow(ctx, `INSERT INTO product_variants (product_id,option_values,price_delta,stock)
				VALUES ($1,$2,0,5) RETURNING id`, pid, v.opt).Scan(v.id); err != nil {
				t.Fatal(err)
			}
		}
		return pid, b, o
	}
	matP, matBought, matOther := product("mat", "매트", `{"색상":"회색"}`, `{"색상":"남색"}`)
	pilP, pilBought, pilOther := product("pillow", "베개", `{"색상":"회색"}`, `{"색상":"흰색"}`)

	var orderID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO orders (order_no, user_id, status, receiver_name, receiver_phone, postcode,
		                    address1, orderer_email, orderer_phone, total_amount, delivered_at)
		VALUES ('EX0002', $1, '배송완료', '받는이', '010-0000-0000', '12345', '서울',
		        'swap2@example.com', '010-1111-1111', 20000, now()) RETURNING id`, userID).Scan(&orderID); err != nil {
		t.Fatal(err)
	}
	item := func(pid, vid, name string) string {
		var id string
		if err := pool.QueryRow(ctx, `INSERT INTO order_items (order_id,product_id,variant_id,product_name,option_label,unit_price,quantity)
			VALUES ($1,$2,$3,$4,'색상: 회색',10000,1) RETURNING id`, orderID, pid, vid, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	item(matP, matBought, "매트")
	pilItem := item(pilP, pilBought, "베개")

	c := client()
	login(t, srv.URL, "swap2@example.com", c)

	got, err := c.Get(srv.URL + "/orders/EX0002/exchange")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(got.Body)
	got.Body.Close()
	form := string(b)
	for _, v := range []string{matOther, pilOther} {
		if !strings.Contains(form, `value="`+v+`"`) {
			t.Errorf("교환 폼에 조합 %s 가 없다 — 그 상품은 교환할 수 없다", v)
		}
	}

	// 숨긴 상품의 조합은 선택지에 없다 — 서버가 거절하는 선택지다.
	if _, err := pool.Exec(ctx, `UPDATE products SET is_visible = false WHERE id = $1`, matP); err != nil {
		t.Fatal(err)
	}
	got, err = c.Get(srv.URL + "/orders/EX0002/exchange")
	if err != nil {
		t.Fatal(err)
	}
	b, _ = io.ReadAll(got.Body)
	got.Body.Close()
	if strings.Contains(string(b), `value="`+matOther+`"`) {
		t.Error("숨긴 상품의 조합이 교환 선택지에 있다")
	}
	if _, err := pool.Exec(ctx, `UPDATE products SET is_visible = true WHERE id = $1`, matP); err != nil {
		t.Fatal(err)
	}

	// 두 번째 품목(베개)을 제 옵션으로 바꾼다.
	resp := post(t, c, srv.URL+"/orders/EX0002/exchange", url.Values{
		"item_id": {pilItem}, "qty_" + pilItem: {"1"}, "new_variant_id": {pilOther}, "reason": {"색이 다름"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("베개 교환 = HTTP %d, want 303", resp.StatusCode)
	}
	var newVariant string
	if err := pool.QueryRow(ctx, `SELECT new_variant_id::text FROM returns WHERE order_id = $1`, orderID).Scan(&newVariant); err != nil {
		t.Fatal(err)
	}
	if newVariant != pilOther {
		t.Errorf("교환 조합 %s, want %s", newVariant, pilOther)
	}
}
