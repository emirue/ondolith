package app

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// **테마의 `url` 이 만든 주소는 그 이름의 화면으로 간다** (D17 함수맵).
//
// 앞 판의 product·order 는 `/product/{slug}`·`/order/{no}` 를 만들었는데 그런
// 라우트는 없다 — 상품은 `/shop/p/{slug}`(P-303), 주문은 `/orders/{no}`(P-502)다.
// 내장 테마가 그 종류를 쓰지 않아 아무도 몰랐고, 쓰는 외부 테마는 404 링크를
// 그렸다. 문자열이 아니라 실제 라우트 표에 대 본다.
func TestURLKindsLandOnTheirScreens(t *testing.T) {
	mux := http.NewServeMux()
	screenOf := map[string]string{}
	for _, rt := range buildTree(nil, nil, nil, nil, nil, nil, nil, true, noopHandler).Routes() {
		if rt.Method != http.MethodGet {
			continue
		}
		pattern := "GET " + rt.Pattern
		screenOf[pattern] = rt.Screen
		mux.HandleFunc(pattern, noopHandler)
	}
	for _, tc := range []struct {
		kind   string
		args   []string
		screen string
	}{
		{"page", []string{"about"}, "P-202"},
		{"board", []string{"free"}, "P-203"},
		{"post", []string{"free", "0b6f3c3e-0000-4000-8000-000000000000"}, "P-204"},
		{"product", []string{"tee"}, "P-303"},
		{"order", []string{"20261007-ABCDEFGHJK"}, "P-502"},
	} {
		path := urlFor(tc.kind, tc.args...)
		_, pattern := mux.Handler(httptest.NewRequest(http.MethodGet, path, nil))
		if got := screenOf[pattern]; got != tc.screen {
			t.Errorf("url %q → %q → %q(%s), want %s", tc.kind, path, pattern, got, tc.screen)
		}
	}
}
