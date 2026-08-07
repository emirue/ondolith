package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// **auth_at 을 프로세스 시계로 찍는 곳이 없어야 한다.**
//
// withActor 는 `authAt.Before(u.SessionsValidFrom)` 로 세션을 파기한다. 오른쪽
// 값은 데이터베이스가 `now()` 로 쓴 것이다. 왼쪽을 `time.Now()` 로 찍으면 두
// 시계를 비교하게 되고, 데이터베이스가 몇 밀리초 앞서 있으면 방금 만든 계정으로
// 로그인한 세션이 다음 요청에서 파기된다 — 가입하자마자 로그인 화면으로
// 튕기고, 다시 시도해도 시계 차이는 그대로라 계속 튕긴다.
//
// 동작 테스트로 이걸 잡으려면 실행 기계에 실제 시계 차이가 있어야 한다. 차이가
// 없는 기계에서는 조용히 통과하는 검사가 된다. 그래서 규칙 자체를 본다: 이
// 패키지의 제품 코드에서 `sessAuthAt` 에 실리는 값은 `time.Now()` 계열이면
// 안 된다. 어느 기계에서든 똑같이 문다.
func TestAuthAtIsNeverStampedFromTheProcessClock(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	// putTime(sm, ctx, sessAuthAt, X) 의 X 안에서 time.Now 를 찾는다.
	var found, scanned []string
	for _, pkg := range pkgs {
		for name, file := range pkg.Files {
			if strings.HasSuffix(name, "_test.go") {
				continue
			}
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				fn, ok := call.Fun.(*ast.Ident)
				if !ok || fn.Name != "putTime" || len(call.Args) != 4 {
					return true
				}
				key, ok := call.Args[2].(*ast.Ident)
				if !ok || key.Name != sessAuthAtIdent {
					return true
				}
				scanned = append(scanned, name)
				ast.Inspect(call.Args[3], func(n ast.Node) bool {
					sel, ok := n.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					pkgName, ok := sel.X.(*ast.Ident)
					if ok && pkgName.Name == "time" && sel.Sel.Name == "Now" {
						found = append(found,
							fset.Position(sel.Pos()).String())
					}
					return true
				})
				return true
			})
		}
	}
	if len(found) > 0 {
		t.Errorf("auth_at 을 프로세스 시계로 찍는다 (auth.Store.Now 를 써야 한다): %v",
			found)
	}
	// 검사가 헛돌지 않는지: 실제로 auth_at 을 찍는 곳을 찾았는가. 상수 이름이
	// 바뀌거나 putTime 이 사라지면 위 루프는 아무것도 못 보고 통과한다.
	if len(scanned) < 3 {
		t.Fatalf("auth_at 을 찍는 곳을 %d 군데밖에 못 찾았다 — 검사가 헛돌았다: %v",
			len(scanned), scanned)
	}
}

// sessAuthAtIdent 는 위 스캔이 찾는 식별자 이름이다. 상수 자체가 아니라 이름을
// 쓰는 이유는 AST 가 값이 아니라 이름을 들고 있기 때문이다.
const sessAuthAtIdent = "sessAuthAt"
