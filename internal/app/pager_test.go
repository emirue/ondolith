package app

import (
	"net/url"
	"testing"

	"github.com/emirue/ondolith/internal/content"
)

// **상한 쪽에서는 「다음」이 없다.** 그 뒤의 쪽 번호는 상한으로 접혀 같은 쪽이
// 나오므로, 링크를 그리면 방문자와 크롤러가 같은 쪽을 끝없이 따라간다.
func TestPagersStopAtThePageCap(t *testing.T) {
	keep := url.Values{"sort": {"name"}}
	if p := listPager("/shop", keep, content.MaxPage-1, true); p["NextURL"] != "/shop?page=100&sort=name" {
		t.Errorf("상한 앞 쪽의 다음 = %q", p["NextURL"])
	}
	if p := listPager("/shop", keep, content.MaxPage, true); p["NextURL"] != "" {
		t.Errorf("상한 쪽의 다음 = %q, want 없음", p["NextURL"])
	}
	if p := listPager("/shop", keep, 2, false); p["PrevURL"] != "/shop?sort=name" || p["NextURL"] != "" {
		t.Errorf("마지막 2쪽 = 이전 %q · 다음 %q, want /shop?sort=name · 없음", p["PrevURL"], p["NextURL"])
	}

	// 게시판·통합 검색의 쪽 이동도 같다 — 글이 상한 너머까지 있어도 100쪽이 끝이다.
	last := content.ListQuery{Page: content.MaxPage - 1, PerPage: 20}
	if p := pagerFor("/board/free", last, 1_000_000); p["HasNext"] != false {
		t.Errorf("게시판 상한 쪽의 HasNext = %v, want false", p["HasNext"])
	}
	before := content.ListQuery{Page: content.MaxPage - 2, PerPage: 20}
	if p := pagerFor("/board/free", before, 1_000_000); p["HasNext"] != true {
		t.Errorf("게시판 상한 앞 쪽의 HasNext = %v, want true", p["HasNext"])
	}
}
