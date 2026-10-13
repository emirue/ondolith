package content

import (
	"net/url"
	"strings"
	"testing"
)

func q(s string) url.Values {
	v, err := url.ParseQuery(s)
	if err != nil {
		panic(err)
	}
	return v
}

// D22 6절: 정렬 컬럼은 허용 목록으로 검사한다. 이스케이프가 아니라 목록이다 —
// 이 값은 실제로 SQL 에 이어 붙는다.
func TestSortColumnIsAnAllowList(t *testing.T) {
	injections := []string{
		"created; DROP TABLE posts",
		"created_at",  // 내부 컬럼명은 URL 어휘가 아니다
		"1",           // ORDER BY 서수
		"(SELECT 1)",  // 서브쿼리
		"title--",     // 주석
		"title/**/,1", // 공백 우회
		"views'",      // 따옴표
		"",            // 빈 값
	}
	for _, s := range injections {
		got := ParseListQuery(q("sort="+url.QueryEscape(s)), 20)
		if got.Sort != "created" {
			t.Errorf("sort=%q 가 %q 로 통과했다", s, got.Sort)
		}
	}

	for _, key := range SortKeys() {
		got := ParseListQuery(q("sort="+key), 20)
		if got.Sort != key || got.sortKey() != key {
			t.Errorf("sort=%s 가 거부됐다", key)
		}
	}
}

// 손으로 만든 ListQuery 도 정렬을 끄지 못한다: 사슬에 없는 키는 CASE 에 하나도
// 걸리지 않아 고정·id 순으로만 나오는데, 그것을 아무도 눈치채지 못한다.
func TestSortKeyFallsBackForAKeyItDidNotWrite(t *testing.T) {
	l := ListQuery{Sort: "created_at); DROP TABLE posts --", Desc: true}
	if got := l.sortKey(); got != "created" {
		t.Errorf("기본 키로 떨어지지 않았다: %q", got)
	}
}

// NFR-105: 상한을 넘는 요청은 **오류가 아니라 상한으로 절삭**된다.
func TestOutOfRangeValuesAreClampedNotRefused(t *testing.T) {
	tests := []struct {
		query   string
		page    int
		perPage int
	}{
		{"page=1&per_page=20", 0, 20},
		{"page=0", 0, 20},
		{"page=-5", 0, 20},
		// 개수를 세는 목록(게시판·통합 검색)에 100쪽 상한은 없다 — 오래된 글도 쪽으로 닿는다.
		// 남은 상한은 OFFSET 이 int32 를 넘지 않게 하는 것뿐이다.
		{"page=99999", 99998, 20},
		{"page=99999999", MaxListPage - 1, 20},
		// 검색은 SearchCountCap 까지만 세므로 쪽도 거기까지다 (ListQuery.MaxPages).
		{"page=99999&q=x", SearchCountCap/20 - 1, 20},
		{"page=99999&q=x&per_page=30", (SearchCountCap+29)/30 - 1, 30},
		{"page=abc", 0, 20},
		{"per_page=10000", 0, MaxPerPage},
		{"per_page=0", 0, 20},
		{"per_page=-1", 0, 20},
		{"per_page=abc", 0, 20},
		{"page=3&per_page=50", 2, 50},
	}
	for _, tc := range tests {
		got := ParseListQuery(q(tc.query), 20)
		if got.Page != tc.page || got.PerPage != tc.perPage {
			t.Errorf("%s → page=%d perPage=%d, want %d/%d",
				tc.query, got.Page, got.PerPage, tc.page, tc.perPage)
		}
	}
}

// 게시판 설정이 기본값이고, 요청이 그것을 넘겨도 하드 상한에 걸린다.
func TestBoardSettingIsTheDefaultAndTheCeilingStillApplies(t *testing.T) {
	if got := ParseListQuery(q(""), 30); got.PerPage != 30 {
		t.Errorf("게시판 설정이 안 쓰였다: %d", got.PerPage)
	}
	if got := ParseListQuery(q("per_page=999"), 30); got.PerPage != MaxPerPage {
		t.Errorf("하드 상한이 안 걸렸다: %d", got.PerPage)
	}
	// 설정 자체가 이상해도 상한이 이긴다.
	if got := ParseListQuery(q(""), 5000); got.PerPage != MaxPerPage {
		t.Errorf("게시판 설정이 상한을 넘겼다: %d", got.PerPage)
	}
}

func TestSearchTermIsBounded(t *testing.T) {
	long := strings.Repeat("가", MaxSearchRunes+50)
	got := ParseListQuery(url.Values{"q": {long}}, 20)
	if n := len([]rune(got.Search)); n != MaxSearchRunes {
		t.Errorf("검색어 %d자, want %d자", n, MaxSearchRunes)
	}
	// 룬 단위로 잘라야 한다. 바이트로 자르면 한글이 깨진다.
	if !strings.HasPrefix(got.Search, "가") {
		t.Errorf("검색어가 바이트 단위로 잘렸다: %q", got.Search)
	}
	if got := ParseListQuery(url.Values{"q": {"  검색어  "}}, 20); got.Search != "검색어" {
		t.Errorf("앞뒤 공백이 남았다: %q", got.Search)
	}
}

// 쪽을 넘겨도 정렬이 풀리지 않는다 — 쪽 주소에 싣는 값이 다시 읽혀 같은 정렬이 된다.
// 앞 판의 쪽 링크는 `page` 와 `q` 만 실어서, 조회순으로 보다 다음 쪽을 누르면 최신순이 됐다.
func TestSortParamRoundTrips(t *testing.T) {
	for _, s := range []string{"", "views", "title", "-title"} {
		a := ParseListQuery(url.Values{"sort": {s}}, 20)
		b := ParseListQuery(url.Values{"sort": {a.SortParam()}}, 20)
		if a.Sort != b.Sort || a.Desc != b.Desc {
			t.Errorf("sort=%q → %q → %s/%v, want %s/%v", s, a.SortParam(), b.Sort, b.Desc, a.Sort, a.Desc)
		}
	}
	if got := ParseListQuery(url.Values{}, 20).SortParam(); got != "" {
		t.Errorf("기본 정렬이 주소에 실렸다: %q", got)
	}
}

func TestOffsetFollowsPageAndPerPage(t *testing.T) {
	if got := ParseListQuery(q("page=4&per_page=25"), 20).Offset(); got != 75 {
		t.Errorf("offset = %d, want 75", got)
	}
	if got := ParseListQuery(q("page=1"), 20).Offset(); got != 0 {
		t.Errorf("첫 페이지 offset = %d, want 0", got)
	}
}
