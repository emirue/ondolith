package app

import (
	"testing"

	"github.com/emirue/ondolith/internal/commerce"
)

// **파는 조합이 없는 상품은 Product 를 싣지 않고, 재고가 모두 0 이면 OutOfStock 이다** (FR-719).
// 구글은 Product 에 offers(또는 리뷰·평점)를 요구한다 — 「판매 준비 중」 상품에 값 없는 Offer 를
// 실으면 경고가 쌓인다. 가격은 파는 조합 중 가장 싼 값이다.
func TestProductJSONLDOffers(t *testing.T) {
	p := &commerce.Product{Name: "티", BasePrice: 10000}
	if ld := productJSONLD(p, nil, "", "", ""); ld != nil {
		t.Errorf("조합 없는 상품 = %v, want nil", ld)
	}
	ld := productJSONLD(p, []commerce.Variant{{PriceDelta: 3000}, {PriceDelta: 500}}, "", "", "")
	offer := ld["offers"].(map[string]any)
	if offer["price"] != 10500 || offer["availability"] != "https://schema.org/OutOfStock" {
		t.Errorf("offer = %v, want 10500·OutOfStock", offer)
	}
	if _, ok := ld["url"]; ok {
		t.Error("site_url 이 없는데 url 을 실었다")
	}
}
