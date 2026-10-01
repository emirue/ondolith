package content

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// **`posts` 를 읽는 질의는 같은 열 목록을 쓰고, Post 를 만드는 곳은 하나다.**
//
// 열 목록과 스캔이 한 쌍인 것은 sqlc 가 보장한다 — 생성 행 타입이 질의에서
// 나온다. 보장되지 않는 것은 **여섯 질의가 서로 같은 목록인가**다: 한 질의만
// `is_pinned, is_secret` 을 바꿔 적어도 둘 다 bool 이라 postRow 변환은
// 컴파일되고, 그 화면에서만 비밀글이 공지로 뜬다. 그래서 질의 파일을 읽어
// 목록을 비교한다.
//
// 실제로 네 함수가 같은 15개 컬럼을 손으로 적고 있었고, 그중 셋은 공유 스캐너를
// 쓰고 있었다 — 상수를 만들어 놓고 새 함수에만 쓴 결과다. 그 상수는 sqlc 로
// 옮기며 사라졌고, 이 검사가 같은 자리를 지킨다.
func TestPostQueriesShareColumnsAndConverter(t *testing.T) {
	queries := postReadingQueries(t)

	// 위반을 **먼저** 보고한다. 헛돌기 가드가 앞에 있으면, 한 질의에서 열을
	// 뺀 변이가 「목록이 다르다」가 아니라 「헛돌았다」로 실패해 겨냥한 단언이
	// 무엇인지 알 수 없게 된다 (M15).
	var first, firstName string
	for name, cols := range queries {
		if first == "" {
			first, firstName = cols, name
			continue
		}
		if cols != first {
			t.Errorf("%s 의 열 목록이 %s 와 다르다:\n  %s\n  %s", name, firstName, cols, first)
		}
	}

	// **헛돌기 방지는 대상 질의로 한다.** posts 를 Post 로 읽는 질의는 여섯이다;
	// 더 적게 찾았으면 질의를 못 본 것이지 검사가 통과한 것이 아니다.
	if len(queries) < 6 {
		t.Fatalf("posts 를 읽는 질의를 %d 개밖에 못 찾았다 — 검사가 헛돌았다: %v",
			len(queries), queries)
	}

	// **목록은 본문을 읽지 않는다.** 바깥 목록의 `''::text AS body` 만으로는
	// 부족하다 — 안쪽 질의가 p.body 를 고르면 정렬이 본문을 들고 다닌다.
	all := namedQueries(t)
	for _, name := range []string{"ListPosts", "ListPostsSearch", "ModeratePosts", "RecentPosts"} {
		if strings.Contains(all[name], "p.body") {
			t.Errorf("%s 가 p.body 를 읽는다 — 목록은 본문 없이 그린다", name)
		}
	}

	// Post 를 만드는 곳도 하나여야 한다. 행 타입이 여섯이라도 변환이 둘이면
	// custom_fields 를 푸는 규칙이 갈라진다.
	if n := postLiterals(t); n != 1 {
		t.Errorf("post.go 에서 Post{…} 를 만드는 곳이 %d 곳 — postOf 하나여야 한다", n)
	}
}

// **정렬할 수 있는 셋은 같은 ORDER BY 를 쓰고, 그 사슬은 허용 목록의 키를 전부
// 안다.** D22 6절의 CASE 사슬은 키가 사슬에 없으면 조용히 아무것도 정렬하지
// 않는다 — 허용 목록에 키를 더하고 사슬을 잊으면 그 정렬은 「되는 것처럼」
// 보이면서 고정·id 순으로 나온다.
func TestSortableQueriesShareTheCaseChain(t *testing.T) {
	sortable := []string{"ListPosts", "ListPostsSearch", "SearchPosts"}
	chains := map[string]string{}
	for name, body := range namedQueries(t) {
		i := strings.Index(body, "ORDER BY")
		if i < 0 || !strings.Contains(body, "sqlc.arg('sort')") {
			continue
		}
		chains[name] = normalizeSQL(body[i:strings.Index(body, "LIMIT")])

		// **쪽을 고르는 안쪽 질의와 그 행들을 내보내는 바깥 질의가 같은 사슬을 쓴다.**
		// 위에서 뽑은 것은 안쪽(LIMIT 앞)이다. 바깥이 다르면 쪽은 맞게 골랐는데
		// 그 안에서 순서가 섞이고, 바깥에 ORDER BY 가 없으면 조인이 정한 순서로
		// 나온다 — 둘 다 작은 게시판에서는 우연히 맞아 보인다.
		if n := strings.Count(normalizeSQL(body), chains[name]); n != 2 {
			t.Errorf("%s: ORDER BY 사슬이 %d 번 나온다 — 안쪽(쪽 선택)과 바깥(출력)에 한 번씩, 같은 글자로 있어야 한다", name, n)
		}
	}
	for _, name := range sortable {
		if chains[name] == "" {
			t.Fatalf("%s 에 sort 인자를 받는 ORDER BY 가 없다", name)
		}
		if chains[name] != chains[sortable[0]] {
			t.Errorf("%s 의 ORDER BY 가 %s 와 다르다:\n  %s\n  %s", name, sortable[0],
				chains[name], chains[sortable[0]])
		}
	}
	if len(chains) != len(sortable) {
		t.Errorf("sort 인자를 받는 질의가 %d 개 — %v 여야 한다", len(chains), sortable)
	}

	chain := chains[sortable[0]]
	if !strings.HasPrefix(chain, "ORDER BY p.is_pinned DESC,") {
		t.Errorf("고정 글이 먼저 오지 않는다: %s", chain)
	}
	for _, key := range SortKeys() {
		if !strings.Contains(chain, "= '"+key+"'") {
			t.Errorf("허용 목록의 %q 가 ORDER BY 사슬에 없다 — 그 정렬은 조용히 꺼진다", key)
		}
	}
	// id 가 타이브레이커로, 같은 방향으로 남아야 순서가 전순서가 된다 (D30).
	if !strings.HasSuffix(chain, "THEN p.id END ASC") || !strings.Contains(chain, "THEN p.id END DESC") {
		t.Errorf("id 타이브레이커가 양방향으로 없다: %s", chain)
	}
}

// postReadingQueries returns name → normalised column list for every named
// query in queries/post.sql whose SELECT reads posts into a Post. 목록 질의는
// body 자리만 ” 라, 그 자리를 p.body 로 돌려 같은 모양으로 비교한다.
//
// 비교하는 것은 **바깥 SELECT 의 목록**이다 — 행 타입을 정하는 쪽이다. 정렬되는
// 셋은 `FROM ( SELECT … FROM posts p … ) p` 로 쪽을 먼저 고르므로, 첫 FROM 이
// `posts p` 가 아니라 `(` 다.
func postReadingQueries(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for name, body := range namedQueries(t) {
		from := strings.Index(body, "\nFROM ")
		if from < 0 || !strings.HasPrefix(body, "SELECT ") || !strings.Contains(body, "FROM posts p\n") {
			continue
		}
		cols := normalizeSQL(strings.TrimPrefix(body[:from], "SELECT "))
		if !strings.Contains(cols, "p.title") {
			continue // count(*) 류
		}
		out[name] = strings.Replace(cols, "''::text AS body", "p.body", 1)
	}
	return out
}

// namedQueries splits queries/post.sql into name → SQL text.
func namedQueries(t *testing.T) map[string]string {
	t.Helper()
	b, err := os.ReadFile("queries/post.sql")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, block := range strings.Split(string(b), "-- name: ")[1:] {
		head, body, _ := strings.Cut(block, "\n")
		name, _, _ := strings.Cut(head, " ")
		out[name] = strings.TrimSpace(body)
	}
	return out
}

func normalizeSQL(s string) string { return strings.Join(strings.Fields(s), " ") }

// postLiterals counts `Post{…}` composite literals in post.go.
func postLiterals(t *testing.T) int {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "post.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	ast.Inspect(f, func(node ast.Node) bool {
		if lit, ok := node.(*ast.CompositeLit); ok {
			if id, ok := lit.Type.(*ast.Ident); ok && id.Name == "Post" {
				n++
			}
		}
		return true
	})
	return n
}
