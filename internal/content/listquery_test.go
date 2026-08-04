package content

import (
	"net/url"
	"strings"
	"testing"
	"time"
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
		clause := got.OrderBy()
		for _, bad := range []string{";", "--", "/*", "(", "'", "DROP"} {
			if strings.Contains(clause, bad) {
				t.Errorf("sort=%q → ORDER BY 에 %q 가 들어갔다: %s", s, bad, clause)
			}
		}
	}

	for key, want := range map[string]string{"created": "created_at", "views": "view_count", "title": "title"} {
		got := ParseListQuery(q("sort="+key), 20)
		if got.Sort != key {
			t.Errorf("sort=%s 가 거부됐다", key)
		}
		if !strings.Contains(got.OrderBy(), want) {
			t.Errorf("sort=%s → %q, %q 가 없다", key, got.OrderBy(), want)
		}
	}
}

// 손으로 만든 ListQuery 도 컬럼을 고르지 못한다. 이 문자열은 SQL 에 닿는다.
func TestOrderByRefusesAColumnItDidNotWrite(t *testing.T) {
	l := ListQuery{Sort: "created_at); DROP TABLE posts --", Desc: true}
	clause := l.OrderBy()
	if strings.Contains(clause, "DROP") || strings.Contains(clause, ";") {
		t.Errorf("ORDER BY 에 요청 문자열이 그대로 들어갔다: %s", clause)
	}
	if !strings.Contains(clause, "created_at") {
		t.Errorf("기본 컬럼으로 떨어지지 않았다: %s", clause)
	}
}

// 고정 글은 어떤 정렬에서도 먼저 온다. 그게 고정의 뜻이다. 그리고 id 가
// tiebreaker 로 남아야 키셋 비교가 전순서가 된다 (D30).
func TestOrderByAlwaysPinsAndTiebreaks(t *testing.T) {
	for _, s := range []string{"created", "views", "title", "-title"} {
		clause := ParseListQuery(q("sort="+s), 20).OrderBy()
		if !strings.HasPrefix(clause, "is_pinned DESC,") {
			t.Errorf("sort=%s: 고정 글이 먼저 오지 않는다: %s", s, clause)
		}
		if !strings.Contains(clause, "id ") {
			t.Errorf("sort=%s: id tiebreaker 가 없다: %s", s, clause)
		}
	}
	// 방향은 세 컬럼이 같아야 인덱스로 내려간다 (D30 측정).
	clause := ParseListQuery(q("sort=created"), 20).OrderBy()
	if strings.Count(clause, "DESC") != 3 {
		t.Errorf("정렬 방향이 섞였다: %s", clause)
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
		{"page=99999", MaxPage - 1, 20},
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

// 키셋 커서는 왕복해야 한다. 깨진 커서는 오류가 아니라 첫 페이지다 — 링크가
// 낡은 것과 구분할 수 없고, 방문자에게는 같은 일이다.
func TestCursorRoundTrips(t *testing.T) {
	c := Cursor{Pinned: true, Created: time.Unix(0, 1754400000123456789).UTC(), ID: "abc-123"}
	got := ParseListQuery(url.Values{"after": {c.String()}}, 20).After
	if got.ID != c.ID || got.Pinned != c.Pinned || !got.Created.Equal(c.Created) {
		t.Errorf("커서 왕복 실패: %+v → %+v", c, got)
	}

	for _, bad := range []string{"garbage", "1:notanumber:x", "1:5", "", ":::", "1:5:"} {
		if got := ParseListQuery(url.Values{"after": {bad}}, 20).After; !got.IsZero() {
			t.Errorf("깨진 커서 %q 가 통과했다: %+v", bad, got)
		}
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
