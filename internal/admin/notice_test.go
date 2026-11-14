package admin

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// **상태를 바꾼 관리자 POST 는 d.done 으로 돌아간다** (FR-724, D19 C11).
//
// 처리기가 http.Redirect 를 직접 부르면 그 화면은 저장돼도 아무 말이 없다 — 운영자가 A-604 의
// 같은 저장을 17초에 네 번 누른 것이 그것이었다. 리다이렉트는 성공 경로에만 있으므로(실패는
// 422 로 다시 그린다, D19 C7) 직접 부르는 곳이 곧 알림을 빠뜨린 곳이다. 새 처리기가 그 길로
// 가면 여기서 걸린다.
func TestAdminChangesReturnThroughDone(t *testing.T) {
	// 직접 리다이렉트해도 되는 곳과 그 이유.
	allowed := map[string]string{
		"done": "알림을 싣고 돌아가는 바로 그 장치다",
		// 입고·실사는 결과(조합·수량·장부·실측)를 주소에 실어 A-517 이 그린다 (D13 「직전 결과
		// 표시」). 스캐너로 연달아 찍는 화면이라 알림이 주소와 함께 다음 찍기까지 남는다.
		"StockReceive": "A-517 이 주소의 done= 으로 결과를 그린다",
		"Stocktake":    "A-517 이 주소의 done= 으로 결과를 그린다",
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	dones := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "http" && sel.Sel.Name == "Redirect" {
					if _, ok := allowed[fn.Name.Name]; !ok {
						t.Errorf("%s: %s 가 http.Redirect 를 직접 부른다 — 성공이면 d.done 으로 알림을 싣고 돌아간다",
							fset.Position(call.Pos()), fn.Name.Name)
					}
				}
				if sel.Sel.Name == "done" && len(call.Args) == 5 {
					dones++
					if lit, ok := call.Args[4].(*ast.BasicLit); ok && lit.Value == `""` {
						t.Errorf("%s: %s 의 알림이 비었다", fset.Position(call.Pos()), fn.Name.Name)
					}
				}
				return true
			})
		}
	}
	// 관리자 POST 의 성공 경로는 50곳이 넘는다. 이보다 적게 찾았다면 검사가 엉뚱한 것을 보고 있다.
	if dones < 50 {
		t.Fatalf("d.done 을 %d곳밖에 못 찾았다", dones)
	}
}
