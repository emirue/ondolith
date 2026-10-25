package app

import (
	"net/url"
	"testing"
)

// **canonical 은 경로와 쪽 번호만 남긴다** (FR-511). 정렬·검색어·utm_* 를 남기면 같은 글이 주소
// 여러 개로 나뉘어 색인된다. 둘째 쪽 뒤는 다른 목록이라 쪽 번호는 남기고, 1쪽은 붙이지 않는다.
func TestCanonicalURLKeepsOnlyThePage(t *testing.T) {
	for raw, want := range map[string]string{
		"/board/free?page=2&utm_source=kakao&sort=views": "https://site.example/board/free?page=2",
		"/board/free?page=1":                             "https://site.example/board/free",
		"/shop/p/tee?ref=ad":                             "https://site.example/shop/p/tee",
		"/":                                              "https://site.example/",
		"/%ED%95%9C%EA%B8%80":                            "https://site.example/%ED%95%9C%EA%B8%80",
		"/board/free?page=abc":                           "https://site.example/board/free",
		"/board/free?page=99999":                         "https://site.example/board/free?page=100",
	} {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if got := canonicalURL("https://site.example/", u); got != want {
			t.Errorf("%s → %s, want %s", raw, got, want)
		}
	}
}
