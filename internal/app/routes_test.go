package app

import (
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func noop(http.ResponseWriter, *http.Request) {}

func inv(pairs ...any) map[string]SecurityClass {
	m := map[string]SecurityClass{}
	for i := 0; i < len(pairs); i += 2 {
		m[pairs[i].(string)] = pairs[i+1].(SecurityClass)
	}
	return m
}

func TestCheckPassesOnAGoodRegistry(t *testing.T) {
	r := NewRegistry().
		Add(Route{Screen: "P-201", Method: "GET", Pattern: "/{$}", Class: SC1, Handler: noop}).
		Add(Route{Screen: "A-201", Method: "POST", Pattern: "/admin/settings", Class: SC5,
			Permission: "settings.update", Handler: noop})

	res := r.Check([]string{"settings.update"}, inv("P-201", SC1, "A-201", SC5))
	if err := res.Err(); err != nil {
		t.Fatalf("정상 라우트가 거부됐다: %v", err)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("경고가 났다: %v", res.Warnings)
	}
}

// Each check gets a violation, because a check nobody has seen fail is a check
// nobody knows the shape of.
func TestCheckCatchesEachViolation(t *testing.T) {
	tests := []struct {
		name  string
		build func() *Registry
		perms []string
		inv   map[string]SecurityClass
		want  string
	}{
		{
			name: "존재하지 않는 권한 키 (오타)",
			build: func() *Registry {
				return NewRegistry().Add(Route{Screen: "A-201", Method: "POST",
					Pattern: "/admin/settings", Class: SC5, Permission: "settings.updat", Handler: noop})
			},
			perms: []string{"settings.update"},
			inv:   inv("A-201", SC5),
			want:  "존재하지 않는 권한 키",
		},
		{
			name: "화면 유형이 SC 범위 밖",
			build: func() *Registry {
				return NewRegistry().Add(Route{Screen: "P-201", Method: "GET",
					Pattern: "/", Class: "SC-9", Handler: noop})
			},
			inv:  inv("P-201", SecurityClass("SC-9")),
			want: "SC-1..SC-8 이 아니다",
		},
		{
			// GET /post/123/delete is reachable by a crawler and by a browser
			// prefetching a link nobody clicked.
			name: "상태 변경 유형인데 GET",
			build: func() *Registry {
				return NewRegistry().Add(Route{Screen: "A-201", Method: "GET",
					Pattern: "/admin/settings", Class: SC5, Permission: "settings.update", Handler: noop})
			},
			perms: []string{"settings.update"},
			inv:   inv("A-201", SC5),
			want:  "상태 변경 유형인데 GET",
		},
		{
			// The screen id is real (checkdocs rejects invented ones repo-wide);
			// what makes it a violation is that the inventory passed in does
			// not carry it, which is exactly the drift this check exists for.
			name: "D11 에 없는 화면",
			build: func() *Registry {
				return NewRegistry().Add(Route{Screen: "P-202", Method: "GET",
					Pattern: "/ghost", Class: SC1, Handler: noop})
			},
			inv:  inv("P-201", SC1),
			want: "D11 인벤토리에 없는 화면",
		},
		{
			name: "화면 유형이 D11 과 다름",
			build: func() *Registry {
				return NewRegistry().Add(Route{Screen: "P-201", Method: "GET",
					Pattern: "/", Class: SC1, Handler: noop})
			},
			inv:  inv("P-201", SC4),
			want: "D11 과 다르다",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := tc.build().Check(tc.perms, tc.inv)
			err := res.Err()
			if err == nil {
				t.Fatalf("위반이 통과했다")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("메시지에 %q 가 없다: %v", tc.want, err)
			}
		})
	}
}

// A permission no route uses still shows up in the role editor: an operator
// grants it and nothing happens. Warning, not error — a phase may seed ahead.
func TestUnusedPermissionWarns(t *testing.T) {
	r := NewRegistry().Add(Route{Screen: "P-201", Method: "GET", Pattern: "/", Class: SC1, Handler: noop})
	res := r.Check([]string{"settings.update", "page.view"}, inv("P-201", SC1))
	if err := res.Err(); err != nil {
		t.Fatalf("미사용 권한이 오류가 됐다: %v", err)
	}
	if len(res.Warnings) != 2 {
		t.Errorf("경고 %d건, want 2건: %v", len(res.Warnings), res.Warnings)
	}
}

func TestDuplicateRoutePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("중복 등록이 통과했다")
		}
	}()
	NewRegistry().
		Add(Route{Screen: "P-201", Method: "GET", Pattern: "/", Class: SC1, Handler: noop}).
		Add(Route{Screen: "P-202", Method: "GET", Pattern: "/", Class: SC1, Handler: noop})
}

func TestMountServesRegisteredRoutes(t *testing.T) {
	hit := false
	r := NewRegistry().Add(Route{Screen: "P-201", Method: "GET", Pattern: "/{$}", Class: SC1,
		Handler: func(w http.ResponseWriter, _ *http.Request) { hit = true }})
	mux := http.NewServeMux()
	r.Mount(mux)

	mux.ServeHTTP(nil2(), mustReq(t, "GET", "/"))
	if !hit {
		t.Error("등록한 핸들러가 불리지 않았다")
	}
}

// D15 4.4's premise: mux.HandleFunc is not called directly anywhere, because a
// route registered that way carries no screen id and no class, so none of the
// checks above can see it.
//
// **읽기는 이 프로세스 안에서 한다.** 앞선 판은 `grep` 을 띄우고 BRE 를 넘겼는데,
// `mux\.Handle\(` 의 `\(` 는 BRE 에서 여는 괄호라 grep 이 "parentheses not
// balanced" 로 죽었다. 죽은 grep 은 빈 출력을 남기고, 바로 아래 `len(out) == 0`
// 이 그것을 "찾은 게 없다" 로 읽어 통과시켰다 — 이 가드는 한 번도 돈 적이 없고
// 그동안 실제 위반 한 건이 초록 아래에 있었다 (M4: 통과는 문다는 뜻이 아니다).
// 하위 프로세스도 정규식 방언도 쓰지 않으면 조용히 죽을 자리가 없다.
//
// **예외는 파일이 아니라 줄이다.** 파일 단위로 열어 두면 그 파일 안에서는
// 몇 줄이든 통과한다 — handler_webhook.go 에 `mux.HandleFunc("GET /admin/…")`
// 를 한 줄 더 넣어도 이 가드는 걸리지 않고, 그 라우트는 화면 ID 도 클래스도
// 권한도 없이 서비스된다. 이 검사는 그 상황을 막는 유일한 백스톱이므로,
// 백스톱 자신이 "이 파일은 통째로 믿는다" 로 넓어지면 안 된다.
func TestNoDirectMuxRegistrationOutsideRegistry(t *testing.T) {
	// 키는 `파일\t등록 줄` 이다. 줄 번호로 잡으면 위쪽을 한 줄 고칠 때마다
	// 어긋나므로 내용으로 잡는다. 각 예외는 이유를 함께 적는다 — 이유 없는
	// 예외는 다음 사람이 지우지 못한다.
	allowed := map[string]string{
		"internal/app/routes.go\tmux.HandleFunc(rt.Method+\" \"+rt.Pattern, guardID(rt))": "Registry.Mount 자신 — 모든 등록이 여기 한 곳을 지난다",

		"internal/install/install.go\tmux.HandleFunc(\"GET /install\", h.show)":                             "설치 트리는 운영 트리와 별개다 (CLAUDE.md 규칙 3)",
		"internal/install/install.go\tmux.HandleFunc(\"POST /install\", h.submit)":                          "설치 트리는 운영 트리와 별개다 (CLAUDE.md 규칙 3)",
		"internal/install/install.go\tmux.HandleFunc(\"/\", func(w http.ResponseWriter, r *http.Request) {": "설치 전에는 모든 경로가 /install 로 간다 (FR-101)",

		"internal/app/handler_webhook.go\tmux.HandleFunc(\"POST /webhooks/payment/{pg}\", d.receive)": "P-905 의 별도 서브트리 — 세션·CSRF·액터가 붙지 않는 것이 목적이다 (D15 SC-8 1항)",
	}

	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel := strings.TrimPrefix(path, root+"/")
			for i, line := range strings.Split(string(src), "\n") {
				if !strings.Contains(line, "mux.Handle(") && !strings.Contains(line, "mux.HandleFunc(") {
					continue
				}
				key := rel + "\t" + strings.TrimSpace(line)
				seen[key]++
				if _, ok := allowed[key]; !ok {
					t.Errorf("Registry 를 거치지 않은 라우트 등록: %s:%d: %s\n"+
						"    허용하려면 이 줄을 사유와 함께 allowed 에 적는다",
						rel, i+1, strings.TrimSpace(line))
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	// **예외 목록도 대조한다.** 정확히 한 번씩 나와야 한다:
	//   0 회 — 등록이 사라졌거나 읽는 쪽이 고장 났다. 후자면 위 루프가 통째로
	//          헛돌아도 초록이므로, 이 단언이 그 상태를 잡는 자리다.
	//   2 회 이상 — 허용된 줄을 복사해 붙여 예외 하나로 여러 라우트를 태웠다.
	for key, why := range allowed {
		f, line, _ := strings.Cut(key, "\t")
		switch seen[key] {
		case 1:
		case 0:
			t.Errorf("예외에 적힌 등록이 없다 — 지웠으면 예외도 지운다 (또는 검사가 헛돌았다): %s: %s", f, line)
		default:
			t.Errorf("예외 한 줄에 등록이 %d 건 걸렸다: %s: %s (%s)", seen[key], f, line, why)
		}
	}
}

func nil2() *responseRecorderStub { return &responseRecorderStub{} }

type responseRecorderStub struct{ code int }

func (r *responseRecorderStub) Header() http.Header         { return http.Header{} }
func (r *responseRecorderStub) Write(b []byte) (int, error) { return len(b), nil }
func (r *responseRecorderStub) WriteHeader(c int)           { r.code = c }

func mustReq(t *testing.T, method, target string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, "http://example.com"+target, nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

// **D11 에 있는데 라우트가 없는 화면을 경고한다.**
//
// Check 의 다른 검사는 등록된 라우트만 훑으므로, 화면 하나가 통째로 빠져
// 있어도 게이트는 전부 초록이다 — 무엇이 남았는지 아무도 모르는 상태가 된다.
func TestCheckWarnsAboutScreensWithNoRoute(t *testing.T) {
	reg := buildTree(nil, nil, nil, nil, nil, nil, nil, true, noopHandler)
	// 권한 목록은 비운다 — 이 검사가 보는 것은 Warnings 뿐이고, 없는 권한
	// 키는 Errors 로 간다.
	res := reg.Check(nil, screenInventory)

	warned := map[string]bool{}
	for _, w := range res.Warnings {
		if id, ok := strings.CutPrefix(w, "D11 에 있는데 라우트가 없는 화면: "); ok {
			warned[id] = true
		}
	}

	routed := map[string]bool{}
	for _, rt := range reg.Routes() {
		routed[rt.Screen] = true
	}
	for id := range screenInventory {
		if routed[id] || servedOutsideTree[id] {
			if warned[id] {
				t.Errorf("%s 는 서비스되는데 미구현으로 경고했다", id)
			}
			continue
		}
		if !warned[id] {
			t.Errorf("%s 는 라우트가 없는데 경고하지 않았다", id)
		}
	}

	// **P-905 는 목록에 있으니 경고되지 않아야 하고, 실제로 서비스돼야 한다.**
	// 목록에 이름을 적는 것만으로 미구현이 숨겨지면 이 검사는 구멍이 된다 —
	// 그 화면이 정말 응답하는지는 TestWebhookIsOutsideTheMainTree 가 본다.
	if !servedOutsideTree["P-905"] {
		t.Error("P-905 가 트리 밖 목록에 없다")
	}
	if warned["P-905"] {
		t.Error("P-905 를 미구현으로 경고했다 — 별도 서브트리가 서비스한다")
	}
}

// **읽기 라우트의 SC-4 완화는 SC-5·SC-6·SC-7 에만, 안전 메서드에만 적용된다**
// (D15 4.4). 넓히면 아무 화면이나 SC-4 로 등록해 검토를 건너뛸 수 있다.
func TestClassAgreesOnlyForSafeReadsOfChangeScreens(t *testing.T) {
	for _, tc := range []struct {
		name   string
		route  SecurityClass
		method string
		want   SecurityClass
		ok     bool
	}{
		{"SC-5 화면의 GET", SC4, http.MethodGet, SC5, true},
		{"SC-6 화면의 GET", SC4, http.MethodGet, SC6, true},
		{"SC-7 화면의 GET", SC4, http.MethodGet, SC7, true},
		{"SC-7 화면의 POST 를 SC-4 로", SC4, http.MethodPost, SC7, false},
		{"SC-6 화면의 POST 를 SC-4 로", SC4, http.MethodPost, SC6, false},
		{"SC-1 화면을 SC-4 로", SC4, http.MethodGet, SC1, false},
		{"SC-8 화면을 SC-4 로", SC4, http.MethodGet, SC8, false},
		{"같은 유형", SC7, http.MethodPost, SC7, true},
	} {
		got := classAgrees(Route{Class: tc.route, Method: tc.method}, tc.want)
		if got != tc.ok {
			t.Errorf("%s: classAgrees = %v, want %v", tc.name, got, tc.ok)
		}
	}
}

// **빈 이름과 nil 어댑터는 항상 함께 나온다** (A-209 「사용 안 함」).
//
// 이 불변식이 깨진 상태가 이 저장소에 한 번 있었다: 이름 쪽만 「사용 안 함」을
// 반영해서 웹훅과 `payments.pg` 라벨은 닫히고 승인 경로는 열려 있었다.
// 이름이 있는데 어댑터가 없으면 승인 시점에 패닉이고, 어댑터가 있는데 이름이
// 없으면 대사(A-508)가 어느 PG 인지 알 수 없다.
func TestPgAdapterNameAndGatewayAgree(t *testing.T) {
	for _, tc := range []struct {
		provider string
		enabled  bool
	}{
		{"toss", true},
		{"", false},
		{"stripe", false},
		{"TOSS", false},
		{" toss", false},
		{"toss ", false},
		{"../toss", false},
	} {
		name, gw := pgAdapterFor(tc.provider, "test_sk_x")
		if (name != "") != tc.enabled || (gw != nil) != tc.enabled {
			t.Errorf("provider=%q → 이름 %q · 어댑터 nil=%v, 둘 다 %v 여야 한다",
				tc.provider, name, gw == nil, tc.enabled)
		}
		if tc.enabled && name != tc.provider {
			t.Errorf("provider=%q 인데 이름이 %q — payments.pg 가 어긋난다", tc.provider, name)
		}
	}

	// **시크릿이 없어도 이름만으로 어댑터를 만든다.** 시크릿 유무로 끄면
	// A-209 의 「사용 안 함」과 「키를 아직 안 넣음」이 구분되지 않고,
	// 화면이 띄우는 경고(클라이언트 키만 있음)가 의미를 잃는다.
	if name, gw := pgAdapterFor("toss", ""); name != "toss" || gw == nil {
		t.Error("시크릿이 비었다고 어댑터를 만들지 않았다 — 그 판단은 A-209 몫이다")
	}
}
