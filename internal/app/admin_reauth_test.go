package app

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alexedwards/scs/v2/memstore"

	"github.com/emirue/ondolith/internal/auth"
)

// reauthAskers 는 재확인을 부르는 관리자 처리기 이름이다 — internal/admin 의 코드에서 모은다.
// 기준표(D15 5.3-1)와 맞는지는 admin 쪽 TestReauthIsAskedExactlyWhereD15Says 가 본다.
func reauthAskers(t *testing.T) map[string]bool {
	t.Helper()
	files, err := filepath.Glob("../admin/*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	out := map[string]bool{}
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
				if call, ok := n.(*ast.CallExpr); ok {
					switch f := call.Fun.(type) {
					case *ast.Ident:
						out[fn.Name.Name] = out[fn.Name.Name] || f.Name == "reauthOK"
					case *ast.SelectorExpr:
						out[fn.Name.Name] = out[fn.Name.Name] || f.Sel.Name == "guardAccountOp"
					}
				}
				return true
			})
		}
	}
	return out
}

// adminPostRoutes 는 tree.go 가 등록하는 관리자 POST 라우트의 패턴 → 처리기 이름이다.
func adminPostRoutes(t *testing.T) map[string]string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "tree.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		if id, isID := lit.Type.(*ast.Ident); !isID || id.Name != "Route" {
			return true
		}
		var method, pattern, handler string
		for _, el := range lit.Elts {
			kv, ok := el.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, _ := kv.Key.(*ast.Ident)
			if key == nil {
				continue
			}
			switch key.Name {
			case "Method", "Pattern":
				if s, ok := kv.Value.(*ast.BasicLit); ok {
					v, _ := strconv.Unquote(s.Value)
					if key.Name == "Method" {
						method = v
					} else {
						pattern = v
					}
				}
			case "Handler":
				if sel, ok := kv.Value.(*ast.SelectorExpr); ok {
					handler = sel.Sel.Name
				}
			}
		}
		if method == "POST" && strings.HasPrefix(pattern, "/admin") && handler != "" {
			out[pattern] = handler
		}
		return true
	})
	return out
}

// routeFor 는 템플릿의 폼 주소(`/admin/users/{{.ID}}/deactivate`)가 가는 라우트의 처리기다. 글자로 된 마디가
// 더 많이 맞는 패턴이 이긴다 — `/admin/categories/new` 는 `/admin/categories/{id}` 가 아니다.
func routeFor(routes map[string]string, action string) string {
	action = regexp.MustCompile(`\{\{[^}]*\}\}`).ReplaceAllString(action, "{}")
	got := strings.Split(strings.Trim(action, "/"), "/")
	best, score := "", -1
	for pattern, h := range routes {
		segs := strings.Split(strings.Trim(pattern, "/"), "/")
		if len(segs) != len(got) {
			continue
		}
		s := 0
		for i, seg := range segs {
			switch {
			case seg == got[i]:
				s++
			case strings.HasPrefix(seg, "{"):
			default:
				s = -1
			}
			if s < 0 {
				break
			}
		}
		if s > score {
			best, score = h, s
		}
	}
	return best
}

// **비밀번호 칸은 서버가 묻는 폼에만, 한 가지 모양으로 있다** (FR-726, D15 5.3-1).
//
// 칸이 늘 보이는 화면, 필요할 때만 보이는 화면, 칸은 있는데 서버가 보지 않는 화면(상품 삭제)이 섞여 운영자가 언제
// 넣어야 하는지 알 수 없었다. 폼마다 어느 처리기로 가는지 라우트에서 찾아, 칸이 있는 폼 = 서버가 묻는 폼인지 본다.
// 칸은 공용 조각(reauth-field) 하나뿐이고 루트 데이터($)를 받는다 — range·with 안에서 `.` 을 넘기면 칸을 그릴지
// 정하는 값이 보이지 않아 칸이 사라지고, 15분이 지난 운영자는 그 동작을 영영 못 한다.
func TestReauthFieldOnlyWhereTheServerAsks(t *testing.T) {
	asks := reauthAskers(t)
	routes := adminPostRoutes(t)
	// 한 처리기 안에서 동작마다 갈리는 것. false 면 그 동작의 폼에는 칸이 없어야 한다.
	byAction := map[string]bool{
		"BackupDownload:settings":    false, // A-604 자동 백업 설정
		"PaymentSettingsSave:verify": false, // A-209 연결 확인
		"RefundSave:close":           false, // 돈이 나가지 않는다
		"RefundSave:reject":          false,
		"ReturnAction:pickup":        false,
		"ReturnAction:reject":        false,
	}
	// 교환 완료는 차액이 음수일 때만 묻는다 — 칸이 {{if lt .PriceDiff 0}} 안에 있다
	// (TestCheaperExchangeFormAsksForThePassword).
	either := map[string]bool{"ReturnAction:exchange": true}
	// 주소 없이 제 화면으로 보내는 폼과, 주소를 처리기가 정해 넘기는 폼(테마 화면의 {{.Action}}).
	self := map[string]string{
		"templates/admin/refund.html":       "/admin/orders/{no}/refund",
		"templates/admin/returns.html":      "/admin/orders/{no}/returns",
		"templates/admin/reconcile.html":    "/admin/reconcile",
		"templates/admin/shipping.html":     "/admin/orders/{no}/shipping",
		"templates/admin/theme-screen.html": "/admin/theme/{key}",
	}
	names, err := fs.Glob(adminFS, "templates/admin/*.html")
	if err != nil {
		t.Fatal(err)
	}
	form := regexp.MustCompile(`(?s)<form\b[^>]*>.*?</form>`)
	actionAttr := regexp.MustCompile(`^<form\b[^>]*\baction="([^"]*)"`)
	actionValue := regexp.MustCompile(`name="action" value="([^"]*)"`)
	invoke := regexp.MustCompile(`\{\{\s*template\s+"reauth-field"\s+([^}]*?)\s*\}\}`)
	field := `{{template "reauth-field" $}}`
	checked := 0
	for _, name := range names {
		b, err := fs.ReadFile(adminFS, name)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		// 새 계정의 초기 비밀번호(A-402 만들기, autocomplete="new-password")는 재확인 칸이 아니다.
		for _, in := range regexp.MustCompile(`<input\b[^>]*name="password"[^>]*>`).FindAllString(src, -1) {
			if name != "templates/admin/layout.html" && !strings.Contains(in, `autocomplete="new-password"`) {
				t.Errorf("%s: 비밀번호 재확인 칸을 직접 그린다 — 공용 조각(reauth-field)만 쓴다: %s", name, in)
			}
		}
		for _, m := range invoke.FindAllStringSubmatch(src, -1) {
			if m[1] != "$" && name != "templates/admin/layout.html" {
				t.Errorf("%s: reauth-field 에 %q 를 넘긴다 — $ 를 넘겨야 칸을 그릴지가 보인다", name, m[1])
			}
		}
		for _, f := range form.FindAllString(src, -1) {
			if !strings.Contains(f[:strings.Index(f, ">")], `method="post"`) {
				continue
			}
			target := self[name]
			if m := actionAttr.FindStringSubmatch(f); m != nil && !regexp.MustCompile(`^\{\{[^}]*\}\}$`).MatchString(m[1]) {
				target = m[1]
			}
			if !strings.HasPrefix(target, "/admin") {
				continue // 로그아웃처럼 관리자 트리 밖으로 가는 폼
			}
			h := routeFor(routes, target)
			if h == "" {
				t.Errorf("%s: 폼이 가는 라우트를 찾지 못했다: %q", name, target)
				continue
			}
			act := ""
			if m := actionValue.FindStringSubmatch(f); m != nil {
				act = m[1]
			}
			key := h + ":" + act
			if either[key] {
				continue
			}
			want := asks[h]
			if v, ok := byAction[key]; ok {
				want = v
			}
			if h == "SettingsSave" {
				// A-201 은 유형을 바꿀 때만 묻는다 — 유형 칸이 있는 폼만 비밀번호 칸을 갖는다.
				want = strings.Contains(f, `name="site.type"`)
			}
			if has := strings.Contains(f, field); has != want {
				t.Errorf("%s: %s(%s) 로 가는 폼의 비밀번호 칸 = %v, 서버가 묻는가 = %v", name, h, act, has, want)
			}
			checked++
		}
	}
	if checked < 40 {
		t.Fatalf("관리자 POST 폼을 %d개밖에 못 봤다 — 검사가 폼을 못 읽고 있다", checked)
	}
}

// **칸은 15분 안에 확인하지 않았을 때만, 필수로 그린다** (FR-726, D15 5.3-1). 보이면 반드시 넣고, 보이지 않으면 넣을
// 것이 없다. 그릴지는 요청 끝의 세션이 정한다 — 같은 요청에서 방금 확인했으면(ConfirmReauth 가 세션에 찍는다) 422 로
// 다시 그린 폼이 또 묻지 않는다.
func TestReauthFieldShowsOnlyWhenTheWindowIsClosed(t *testing.T) {
	sm := newSessionManager(memstore.New(), false)
	ui, err := newAdminRenderer(func() string { return "s" }, nil, "", false, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	ui.sm = sm
	now := time.Now()
	const required = `<input name="password" type="password" required autocomplete="current-password">`
	for _, tc := range []struct {
		name          string
		actorReauth   time.Time // 요청이 들어올 때 세션에 있던 시각
		confirmedHere bool      // 이 요청이 비밀번호를 확인했다
		want          bool
	}{
		{"한 시간 전에 확인", now.Add(-time.Hour), false, true},
		{"1분 전에 확인", now.Add(-time.Minute), false, false},
		{"창은 닫혔지만 이 요청에서 방금 확인", now.Add(-time.Hour), true, false},
	} {
		ctx, err := sm.Load(context.Background(), "")
		if err != nil {
			t.Fatal(err)
		}
		putTime(sm, ctx, sessReauth, tc.actorReauth)
		a := &Actor{User: &auth.User{ID: "u1"}, AuthAt: tc.actorReauth, ReauthAt: tc.actorReauth}
		if tc.confirmedHere {
			putTime(sm, ctx, sessReauth, now)
		}
		rec := httptest.NewRecorder()
		ui.Render(rec, httptest.NewRequest(http.MethodGet, "/admin/themes/upload", nil).WithContext(withActorValue(ctx, a)),
			"admin/theme-upload.html", http.StatusOK, map[string]any{})
		if has := strings.Contains(rec.Body.String(), required); has != tc.want {
			t.Errorf("%s: 필수 비밀번호 칸 = %v, want %v", tc.name, has, tc.want)
		}
		if !tc.want && strings.Contains(rec.Body.String(), `name="password"`) {
			t.Errorf("%s: 묻지 않을 때도 비밀번호 칸이 있다", tc.name)
		}
	}
}
