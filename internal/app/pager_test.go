package app

import (
	"bytes"
	"net/url"
	"strings"
	"testing"

	"github.com/emirue/ondolith/internal/content"
	"github.com/emirue/ondolith/internal/theme"
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

}

// **게시판·통합 검색은 쪽 번호로 어디든 간다** — 그누보드처럼 열 개씩 번호를 보이고
// 처음·이전·다음·마지막이 있다. 앞 판은 「이전·다음」만 있고 100쪽에서 끝나서, 한 쪽
// 20건이면 최근 2,000건 너머의 글에는 쪽으로 닿을 수 없었다. 주소에는 정렬과 검색어가
// 실린다 — 빠지면 다음 쪽에서 정렬이 풀린다.
func TestBoardPagerNumbersEveryPageAndKeepsTheQuery(t *testing.T) {
	q := content.ParseListQuery(url.Values{"page": {"12"}, "sort": {"views"}, "q": {"공지"}}, 20)
	p := pagerFor("/board/free", q, 1000) // 50쪽

	var nos []int
	for _, l := range p["Pages"].([]pageLink) {
		nos = append(nos, l.No)
		if l.Current != (l.No == 12) {
			t.Errorf("%d쪽 Current = %v", l.No, l.Current)
		}
	}
	if len(nos) != 10 || nos[0] != 11 || nos[9] != 20 {
		t.Errorf("12쪽의 번호 = %v, want 11~20", nos)
	}
	for key, want := range map[string]string{
		"FirstURL": "/board/free?q=%EA%B3%B5%EC%A7%80&sort=views",
		"PrevURL":  "/board/free?page=11&q=%EA%B3%B5%EC%A7%80&sort=views",
		"NextURL":  "/board/free?page=13&q=%EA%B3%B5%EC%A7%80&sort=views",
		"LastURL":  "/board/free?page=50&q=%EA%B3%B5%EC%A7%80&sort=views",
	} {
		if p[key] != want {
			t.Errorf("%s = %q, want %q", key, p[key], want)
		}
	}

	// 마지막 묶음은 마지막 쪽에서 끝나고, 마지막 쪽에는 다음이 없다.
	end := pagerFor("/board/free", content.ParseListQuery(url.Values{"page": {"50"}}, 20), 1000)
	if links := end["Pages"].([]pageLink); links[0].No != 41 || links[len(links)-1].No != 50 {
		t.Errorf("50쪽의 번호 = %v", links)
	}
	if end["NextURL"] != "" || end["LastURL"] != "" {
		t.Errorf("마지막 쪽의 다음 = %q · 마지막 = %q, want 없음", end["NextURL"], end["LastURL"])
	}
	// 100쪽 너머도 쪽이다 — 2,000건 너머의 글에 닿는다.
	deep := pagerFor("/board/free", content.ParseListQuery(url.Values{"page": {"150"}}, 20), 1_000_000)
	if deep["PageNo"] != 150 || deep["NextURL"] != "/board/free?page=151" {
		t.Errorf("150쪽 = %v · 다음 %q", deep["PageNo"], deep["NextURL"])
	}
}

// 쪽 수는 올림이다 — 1,001건이면 51쪽에 한 건이 남는다. 빈 목록도 1쪽이다.
func TestLastPageRoundsUp(t *testing.T) {
	q := content.ParseListQuery(url.Values{}, 20)
	for _, tc := range []struct {
		total int64
		want  int
	}{{0, 1}, {1, 1}, {20, 1}, {21, 2}, {1000, 50}, {1001, 51}} {
		if got := lastPage(tc.total, q); got != tc.want {
			t.Errorf("lastPage(%d) = %d, want %d", tc.total, got, tc.want)
		}
	}
	// 검색은 SearchCountCap 까지만 센다 — 쪽도 거기까지다(20건이면 500쪽).
	sq := content.ParseListQuery(url.Values{"q": {"공지"}}, 20)
	if got := lastPage(1_000_000, sq); got != content.SearchCountCap/20 {
		t.Errorf("검색의 마지막 쪽 = %d, want %d", got, content.SearchCountCap/20)
	}
}

// 주소가 고른 쪽 크기는 쪽 링크에도 실린다 — 빠지면 50건씩 보던 4쪽의 「다음」이 20건 기준
// 5쪽이 된다. 게시판 설정 그대로면 싣지 않는다. 없는 쪽(빈 목록의 5쪽)은 마지막 쪽으로 그린다.
func TestPagerKeepsAskedPageSizeAndStaysInRange(t *testing.T) {
	q := content.ParseListQuery(url.Values{"page": {"4"}, "per_page": {"50"}}, 20)
	if p := pagerFor("/board/free", q, 1000); p["NextURL"] != "/board/free?page=5&per_page=50" {
		t.Errorf("다음 = %q", p["NextURL"])
	}
	if p := pagerFor("/board/free", content.ParseListQuery(url.Values{"page": {"2"}}, 20), 1000); p["NextURL"] != "/board/free?page=3" {
		t.Errorf("설정 그대로의 다음 = %q", p["NextURL"])
	}
	empty := pagerFor("/search", content.ParseListQuery(url.Values{"page": {"5"}}, 20), 0)
	if empty["PageNo"] != 1 || empty["PrevURL"] != "" || empty["FirstURL"] != "" {
		t.Errorf("빈 목록의 5쪽 = %v · 이전 %q · 처음 %q", empty["PageNo"], empty["PrevURL"], empty["FirstURL"])
	}
}

// 검색은 SearchCountCap 까지만 센다 — 넘으면 「10,000건 이상」이고, 쪽도 거기까지다(20건이면 500쪽).
func TestSearchPagerStopsAtTheCountCap(t *testing.T) {
	q := content.ParseListQuery(url.Values{"q": {"공지"}}, 20)
	p := pagerFor("/search", q, content.SearchCountCap+1)
	if p["Capped"] != true || p["LastPage"] != 500 {
		t.Errorf("상한을 넘은 검색 = Capped %v · 마지막 %v, want true · 500", p["Capped"], p["LastPage"])
	}
	if p := pagerFor("/search", q, content.SearchCountCap); p["Capped"] != false {
		t.Errorf("상한 그대로의 검색이 「이상」으로 그려진다")
	}
	if p := pagerFor("/board/free", content.ParseListQuery(url.Values{}, 20), 50_000); p["Capped"] != false {
		t.Errorf("검색어 없는 목록이 「이상」으로 그려진다 — 그 수는 정확하다")
	}
}

// 상한을 넘은 검색의 쪽 넘김이 **끝까지 그려진다.** 상한 값이 int 로 들어가 테마 함수
// number(int64)에서 렌더링이 「전체 」 뒤에서 끊긴 적이 있다 — 200 이 나간 뒤라 잘린 화면이
// 그대로 갔다. 손으로 만든 데이터가 아니라 pagerFor 가 낸 것을 그린다.
func TestCappedSearchPagerRendersWhole(t *testing.T) {
	l := theme.New(theme.Builtin(), "", false, theme.FuncMap(theme.Deps{}))
	q := content.ParseListQuery(url.Values{"q": {"공지"}}, 20)
	for total, want := range map[int64]string{
		content.SearchCountCap + 1: "전체 10,000건 이상",
		1234:                       "전체 1,234건",
	} {
		tpl, err := l.Template("search.html")
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if err := tpl.ExecuteTemplate(&buf, "partials/pagination.html", pagerFor("/search", q, total)); err != nil {
			t.Fatalf("total %d: %v", total, err)
		}
		if !strings.Contains(buf.String(), want) || !strings.Contains(buf.String(), "</nav>") {
			t.Errorf("total %d: %q 가 없거나 끝까지 그려지지 않았다: %s", total, want, buf.String())
		}
	}
}
