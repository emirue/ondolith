package content

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

func readFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// **`posts` 를 읽는 SELECT 는 postColumns 를 쓰고, 읽기는 scanPost 로 한다.**
//
// 목록과 스캔 순서는 한 쌍이다. 둘이 갈라지면 컴파일은 되고, 런타임에 타입
// 오류가 나거나 — 더 나쁘게는 — 같은 타입끼리 자리가 바뀌어 **조용히 틀린 값**이
// 나온다. `is_pinned` 와 `is_secret` 이 뒤바뀌면 비밀글이 공지로 뜬다.
//
// 실제로 네 함수가 같은 15개 컬럼을 손으로 적고 있었고, 그중 셋은 공유 스캐너를
// 쓰고 있었다 — 상수를 만들어 놓고 새 함수에만 쓴 결과다.
func TestPostQueriesShareColumnsAndScanner(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "post.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}

	// 문자열 리터럴 안에서 posts 를 읽는 SELECT 를 찾는다.
	var handwritten []string
	scanned := 0
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		v := lit.Value
		if !strings.Contains(v, "SELECT") || !strings.Contains(v, "FROM posts") {
			return true
		}
		scanned++
		// postColumns 를 쓰는 조각은 `SELECT ` 로 끝난다 (뒤에 + postColumns).
		if strings.Contains(v, "p.title, p.body, p.custom_fields") {
			handwritten = append(handwritten, fset.Position(lit.Pos()).String())
		}
		return true
	})

	if scanned == 0 {
		t.Fatal("posts 를 읽는 SELECT 를 하나도 찾지 못했다 — 검사가 헛돌았다")
	}
	if len(handwritten) > 0 {
		t.Errorf("컬럼 목록을 손으로 적은 곳 — postColumns 를 쓸 것: %v", handwritten)
	}

	// 스캔도 한 곳이어야 한다. 목록만 모으고 스캔이 여럿이면 어긋남은 그대로다.
	src, err := parser.ParseFile(token.NewFileSet(), "post.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	scanFuncs := 0
	ast.Inspect(src, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if ok && sel.Sel.Name == "Scan" {
			scanFuncs++
		}
		return true
	})
	// scanPost 의 것 하나 + Comment·Board 등 다른 타입의 것들. Post 컬럼을
	// 읽는 Scan 이 하나뿐인지는 아래 문자열로 본다.
	if n := strings.Count(readFile(t, "post.go"), "&p.CommentCount, &p.HasAttachment"); n != 1 {
		t.Errorf("Post 를 읽는 Scan 이 %d 곳 — scanPost 하나여야 한다", n)
	}
}
