package app

import (
	"net/http"
	"os/exec"
	"path/filepath"
	"regexp"
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
// checks above can see it. Enforced by grep rather than by convention.
func TestNoDirectMuxRegistrationOutsideRegistry(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("grep", "-rn", "--include=*.go",
		`mux\.Handle\(\|mux\.HandleFunc(`, filepath.Join(root, "internal"), filepath.Join(root, "cmd")).Output()
	if err != nil && len(out) == 0 {
		return // grep found nothing
	}
	allowed := regexp.MustCompile(`internal/app/routes\.go|internal/install/install\.go`)
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" || allowed.MatchString(line) {
			continue
		}
		t.Errorf("Registry 를 거치지 않은 라우트 등록: %s", strings.TrimPrefix(line, root+"/"))
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
