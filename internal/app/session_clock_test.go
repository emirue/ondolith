package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// authAtHome 은 auth_at 키를 만져도 되는 유일한 파일이다: 상수 선언, 유일한
// 기록자 stampAuthAt, 그리고 판정하는 withActor 가 전부 여기 있다.
const authAtHome = "middleware_auth.go"

// **auth_at 을 stampAuthAt 말고 아무도 쓰지 않는다.**
//
// 값이 데이터베이스 시계에서 왔다는 것은 타입이 보장한다 — stampAuthAt 은
// auth.DBTime 을 받고, DBTime 의 필드는 비공개라 auth 패키지 밖에서는 만들 수
// 없다. `time.Now()` 를 넣으면 컴파일이 안 된다.
//
// 타입이 못 막는 것은 **그 함수를 우회해서 키를 직접 쓰는 것**이다.
// `sm.Put(ctx, sessAuthAt, …)` 한 줄이면 장벽 밖으로 나간다. 그것을 여기서 본다.
//
// 앞선 판(이 검사의 첫 형태)은 `putTime(…, sessAuthAt, X)` 의 X 안에서
// `time.Now` 를 찾았다. 헛것이었다 — 올바른 코드도 값을 변수로 넘기므로,
// 버그를 가장 자연스럽게 쓴 `at := time.Now()` 두 줄 위에서는 그냥 통과했다.
// 실제로 그렇게 되돌려 보고 확인했다. 인자 표현식을 들여다보는 검사는 값을
// 한 번만 옮겨도 빠져나간다. 그래서 값이 아니라 **키가 어디서 쓰이는지**를 본다.
func TestAuthAtIsWrittenOnlyByStampAuthAt(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var outside, home []string
	for _, pkg := range pkgs {
		for name, file := range pkg.Files {
			if strings.HasSuffix(name, "_test.go") {
				continue
			}
			ast.Inspect(file, func(n ast.Node) bool {
				var hit bool
				switch v := n.(type) {
				case *ast.Ident:
					hit = v.Name == sessAuthAtIdent
				case *ast.BasicLit:
					// 상수를 우회해 값을 직접 적는 것도 같은 우회다.
					hit = v.Kind == token.STRING && v.Value == `"`+sessAuthAtKey+`"`
				}
				if !hit {
					return true
				}
				where := fset.Position(n.Pos()).String()
				if strings.HasSuffix(name, authAtHome) {
					home = append(home, where)
					return true
				}
				outside = append(outside, where)
				return true
			})
		}
	}
	if len(outside) > 0 {
		t.Errorf("auth_at 을 %s 밖에서 만진다 — stampAuthAt 을 거쳐야 한다: %v",
			authAtHome, outside)
	}
	// 검사가 헛돌지 않는지: 상수 이름이나 키가 바뀌면 위 루프는 아무것도 못
	// 보고 조용히 통과한다. 선언·기록·판독 세 군데는 반드시 잡혀야 한다.
	if len(home) < 3 {
		t.Fatalf("%s 안에서 auth_at 사용을 %d 군데밖에 못 찾았다 — 검사가 헛돌았다",
			authAtHome, len(home))
	}
}

// AST 는 값이 아니라 이름과 리터럴을 들고 있으므로 둘 다 이름으로 적는다.
// 상수 자체와 어긋나면 위의 「헛돌았다」 가드가 잡는다.
const (
	sessAuthAtIdent = "sessAuthAt"
	sessAuthAtKey   = "auth_at"
)
