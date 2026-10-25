package app

import (
	"github.com/emirue/ondolith/internal/commerce"
	"github.com/emirue/ondolith/internal/theme"
)

// Structured data (FR-719). 값은 map 으로 만들어 템플릿에 넘긴다 — html/template 이
// `<script type="application/ld+json">` 안의 값을 JSON 으로 내보내며 `<` 를 < 로
// 바꾸므로, 상품 이름에 `</script>` 가 들어 있어도 스크립트를 끊지 못한다. 문자열을 직접
// 이어 붙이면 그 보장이 없다.

// siteJSONLD is the home page's WebSite and Organization. 구글은 조직 정보를 홈 한 곳에서 읽고
// 필수 속성이 없다(Organization 구조화 데이터 안내, 2026-10-25 확인). name 은 사이트 이름이고,
// 상호가 다르면 legalName 이다. 사업자 정보는 입력한 항목만 싣는다. url 은 site_url 을
// 알 때만이다 — canonical 과 같은 이유다.
func siteJSONLD(site theme.Site, url, logo string) map[string]any {
	web := map[string]any{"@type": "WebSite", "name": site.Name}
	org := map[string]any{"@type": "Organization", "name": site.Name}
	if url != "" {
		web["url"], org["url"] = url, url
		// 활성 테마의 로고 (FR-310). 구조화 데이터의 주소는 절대 주소라 site_url 이 있을 때만이다.
		if logo != "" {
			org["logo"] = logo
		}
	}
	business := func(key string) string { return site.Business[commerce.BusinessLabels[key]] }
	if v := business("business.name"); v != "" && v != site.Name {
		org["legalName"] = v
	}
	for key, prop := range map[string]string{"business.address": "address", "business.phone": "telephone",
		"business.email": "email", "business.reg_no": "taxID"} {
		if v := business(key); v != "" {
			org[prop] = v
		}
	}
	return map[string]any{"@context": "https://schema.org", "@graph": []any{web, org}}
}

// productJSONLD is P-303's Product, or nil when there is nothing to offer. 구글은 Product 에
// offers(또는 리뷰·평점) 하나를 요구한다 — 파는 조합이 없는 상품(「판매 준비 중」)은 싣지 않는다.
// 가격은 파는 조합 중 가장 싼 값(목록의 「12,000원~」과 같다)이고, 재고가 하나라도 있으면
// InStock 이다.
func productJSONLD(p *commerce.Product, onSale []commerce.Variant, description, image, url string) map[string]any {
	if len(onSale) == 0 {
		return nil
	}
	low, inStock := onSale[0].PriceDelta, false
	for _, v := range onSale {
		low = min(low, v.PriceDelta)
		inStock = inStock || v.Stock > 0
	}
	availability := "https://schema.org/OutOfStock"
	if inStock {
		availability = "https://schema.org/InStock"
	}
	offer := map[string]any{"@type": "Offer", "price": p.BasePrice + low, "priceCurrency": "KRW",
		"availability": availability}
	product := map[string]any{"@context": "https://schema.org", "@type": "Product", "name": p.Name,
		"offers": offer}
	if description != "" {
		product["description"] = description
	}
	if image != "" {
		product["image"] = image
	}
	if url != "" {
		offer["url"], product["url"] = url, url
	}
	return product
}
