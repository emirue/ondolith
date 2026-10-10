package app

import (
	"context"
	"net/http"
	"net/url"
	"testing"
)

// **P-514 에 결제 키 없이 온 승인 요청은 500 이 아니라 400 이다** (D19 P-514, BUG-01).
//
// 이 화면은 결제창을 붙이지 않아 폼이 늘 빈 paymentKey 를 보낸다. 앞 판은 그것을
// 그대로 결제 행에 넣으려다 `payment_key` CHECK 에 걸려 500 이었다 — 누를 때마다
// 서버 고장으로 기록됐다. 결제 행도 생기지 않아야 한다.
func TestExchangePayWithoutAPaymentKeyIs400(t *testing.T) {
	srv, pool, variant := shopSite(t)
	ctx := context.Background()
	userID := mkUser(t, pool, "swap@example.com")
	var orderID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO orders (order_no, user_id, status, receiver_name, receiver_phone, postcode,
		                    address1, orderer_email, orderer_phone, total_amount)
		VALUES ('EX0001', $1, '차액결제대기', '받는이', '010-0000-0000', '12345', '서울',
		        'swap@example.com', '010-1111-1111', 13000) RETURNING id`, userID).Scan(&orderID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO returns (return_no, order_id, kind, status, new_variant_id, price_difference)
		VALUES ('RX0001', $1, '교환', '차액결제대기', $2, 3000)`, orderID, variant); err != nil {
		t.Fatal(err)
	}
	c := client()
	login(t, srv.URL, "swap@example.com", c)

	resp := post(t, c, srv.URL+"/orders/EX0001/exchange/RX0001/pay", url.Values{"paymentKey": {""}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("빈 결제 키 = HTTP %d, want 400", resp.StatusCode)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM payments`).Scan(&n); err != nil || n != 0 {
		t.Errorf("결제 행 %d개 (%v), want 0", n, err)
	}
}
