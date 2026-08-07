package content

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// columnMark 는 펼친 SQL 안에서 `postColumns` 가 있던 자리다. 식별자는 값이
// 아니므로 자리만 남기고, 그 자리가 있는지로 「상수를 썼다」를 판정한다.
const columnMark = "«postColumns»"

// flattenSQL 은 `"…" + ident + "…"` 로 쪼개진 문자열을 한 덩어리로 만든다.
//
// **이것이 없으면 검사가 헛돈다.** Go 소스에서 “ `SELECT ` + postColumns + `
// FROM posts p` “ 는 AST 상 세 노드이고, 어느 리터럴도 "SELECT" 와
// "FROM posts" 를 **동시에** 갖지 않는다. 리터럴 하나씩만 보던 앞 판은 대상
// 쿼리 다섯을 하나도 못 봤고, 「검사가 헛돌지 않았다」는 우연히 안 쪼개진
// COUNT 쿼리 둘로 채워지고 있었다.
func flattenSQL(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return ""
		}
		s, err := strconv.Unquote(v.Value)
		if err != nil {
			return v.Value
		}
		return s
	case *ast.Ident:
		if v.Name == "postColumns" {
			return columnMark
		}
		return " "
	case *ast.BinaryExpr:
		if v.Op != token.ADD {
			return ""
		}
		return flattenSQL(v.X) + flattenSQL(v.Y)
	case *ast.ParenExpr:
		return flattenSQL(v.X)
	}
	return ""
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
	f, err := parser.ParseFile(fset, "post.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	// 손으로 적은 컬럼 목록의 지문. 한 조각만 봐도 충분하다 — 이 문자열이
	// 나오는 이유는 목록을 베껴 적었을 때뿐이다.
	const handwritten = "p.title, p.body, p.custom_fields"

	var bad []string
	usesConstant := 0
	seen := map[token.Pos]bool{}

	ast.Inspect(f, func(n ast.Node) bool {
		var e ast.Expr
		switch v := n.(type) {
		case *ast.BinaryExpr:
			e = v
		case *ast.BasicLit:
			e = v
		default:
			return true
		}
		// 바깥 BinaryExpr 을 이미 봤으면 그 안쪽은 건너뛴다.
		if seen[e.Pos()] {
			return true
		}
		sql := flattenSQL(e)
		if !strings.Contains(sql, "SELECT") || !strings.Contains(sql, "FROM posts") {
			return true
		}
		// 이 식 안의 모든 자식을 본 것으로 표시한다.
		ast.Inspect(e, func(c ast.Node) bool {
			if c != nil {
				seen[c.Pos()] = true
			}
			return true
		})

		switch {
		case strings.Contains(sql, columnMark):
			usesConstant++
		case strings.Contains(sql, handwritten):
			bad = append(bad, fset.Position(e.Pos()).String())
		}
		return true
	})

	// 위반을 **먼저** 보고한다. 헛돌기 가드가 앞에 있으면, 컬럼 하나를 손으로
	// 되돌린 변이가 「손으로 적었다」가 아니라 「헛돌았다」로 실패해 겨냥한
	// 단언이 무엇인지 알 수 없게 된다 (M15).
	if len(bad) > 0 {
		t.Errorf("컬럼 목록을 손으로 적은 곳 — postColumns 를 쓸 것: %v", bad)
	}

	// **헛돌기 방지는 대상 쿼리로 한다.** COUNT 쿼리가 우연히 조건에 걸려
	// "무언가는 봤다" 가 되는 것으로는 이 검사가 무엇을 봤는지 알 수 없다.
	//
	// `postColumns` 를 쓰는 것과 손으로 적은 것의 **합**을 센다: 위반이 있을
	// 때도 대상 쿼리는 다섯이므로, 이 가드는 「쿼리 자체를 못 찾은 경우」에만
	// 운다.
	if usesConstant+len(bad) < 5 {
		t.Fatalf("posts 를 읽는 쿼리를 %d 개밖에 못 찾았다 — 검사가 헛돌았다",
			usesConstant+len(bad))
	}

	// 스캔도 한 곳이어야 한다. 목록만 모으고 스캔이 여럿이면 어긋남은 그대로다.
	if n := strings.Count(readFile(t, "post.go"), "&p.CommentCount, &p.HasAttachment"); n != 1 {
		t.Errorf("Post 를 읽는 Scan 이 %d 곳 — scanPost 하나여야 한다", n)
	}
}

func readFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
