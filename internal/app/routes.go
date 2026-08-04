package app

import (
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
)

// Route registration forces a permission declaration (D15 4.4).
//
// mux.HandleFunc is never called directly. It cannot be: a handler registered
// that way carries no screen id, no security class and no permission, so none
// of the boot checks below can see it. The 20-line helper DEC-1 promised
// instead of a router dependency is this file.

// SecurityClass is D15 6절's SC-1..SC-8.
type SecurityClass string

const (
	SC1 SecurityClass = "SC-1" // 읽기 전용 공개
	SC2 SecurityClass = "SC-2" // 폼이 있는 공개
	SC3 SecurityClass = "SC-3" // 본인 소유 자원
	SC4 SecurityClass = "SC-4" // 관리자 조회
	SC5 SecurityClass = "SC-5" // 관리자 변경
	SC6 SecurityClass = "SC-6" // 결제·금액
	SC7 SecurityClass = "SC-7" // 파일 업로드·다운로드
	SC8 SecurityClass = "SC-8" // 웹훅
)

var allClasses = []SecurityClass{SC1, SC2, SC3, SC4, SC5, SC6, SC7, SC8}

// isSafeMethod is P5's set: these must not change permission-bearing state.
func isSafeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// Route is one registered screen.
type Route struct {
	// Screen is the D11 id (P-101, A-201...). It is what ties a running route
	// back to the inventory, and the boot check compares the two.
	Screen string
	// Method and Pattern go to http.ServeMux.
	Method  string
	Pattern string
	// Class is the D15 6절 checklist this screen answers to.
	Class SecurityClass
	// Permission is the key the tree gate and handler require, or "" for a
	// screen open to anonymous callers. Empty is a declaration, not a default:
	// it has to be written down.
	Permission string
	Handler    http.HandlerFunc
}

// Registry collects routes so they can be checked before any of them serve.
type Registry struct {
	routes []Route
	seen   map[string]bool // method+pattern, to catch a duplicate registration
}

func NewRegistry() *Registry { return &Registry{seen: map[string]bool{}} }

// Add registers one route. Every field is required except Permission, and that
// one is required to be *decided*.
func (r *Registry) Add(rt Route) *Registry {
	key := rt.Method + " " + rt.Pattern
	if r.seen[key] {
		// Two handlers on one pattern: ServeMux would panic at registration,
		// but only for an exact duplicate. Catching it here names the screen.
		panic("app: 라우트 중복 등록: " + key + " (" + rt.Screen + ")")
	}
	r.seen[key] = true
	r.routes = append(r.routes, rt)
	return r
}

// Routes returns a copy for inspection.
func (r *Registry) Routes() []Route { return slices.Clone(r.routes) }

// Mount writes the registry into a mux. Call Check first.
func (r *Registry) Mount(mux *http.ServeMux) {
	for _, rt := range r.routes {
		mux.HandleFunc(rt.Method+" "+rt.Pattern, rt.Handler)
	}
}

// CheckResult is what the boot self-check found.
type CheckResult struct {
	// Errors stop the boot. FR-110's reasoning: a server that starts in a
	// wrong state is worse than one that refuses to start, because the wrong
	// state is discovered by a visitor.
	Errors []string
	// Warnings are printed and tolerated.
	Warnings []string
}

func (c CheckResult) Err() error {
	if len(c.Errors) == 0 {
		return nil
	}
	return fmt.Errorf("라우트 자체 점검 실패:\n  - %s", strings.Join(c.Errors, "\n  - "))
}

// Check runs D15 4.4's five checks.
//
//	knownPerms  the permission keys the database holds (seeded from code, D15 P1)
//	inventory   the screen ids D11 declares, with the class each one carries
func (r *Registry) Check(knownPerms []string, inventory map[string]SecurityClass) CheckResult {
	var res CheckResult
	known := map[string]bool{}
	for _, p := range knownPerms {
		known[p] = true
	}
	used := map[string]bool{}

	for _, rt := range r.routes {
		where := rt.Screen + " " + rt.Method + " " + rt.Pattern

		// 1. a permission key that does not exist judges either always-false or
		//    always-true depending on which side of the comparison it lands.
		if rt.Permission != "" {
			if !known[rt.Permission] {
				res.Errors = append(res.Errors,
					where+": 존재하지 않는 권한 키 "+rt.Permission)
			}
			used[rt.Permission] = true
		}

		// 2. an unclassified screen has no checklist, so nobody reviewed it.
		if !slices.Contains(allClasses, rt.Class) {
			res.Errors = append(res.Errors,
				where+": 화면 유형이 SC-1..SC-8 이 아니다 ("+string(rt.Class)+")")
		}

		// 3. P5: safe methods do not change state. A GET that deletes is
		//    reachable by a crawler and by a prefetching browser.
		if (rt.Class == SC5 || rt.Class == SC6) && rt.Method == http.MethodGet {
			res.Errors = append(res.Errors,
				where+": 상태 변경 유형인데 GET 이다 (D15 P5)")
		}

		// 5. the route table and D11 must agree.
		//
		// D11 gives a class to a SCREEN; a Route carries the class of one
		// OPERATION, and a change screen holds both its form (GET) and its
		// submission (POST). D15 4.4 fixes the one pair this allows: the read
		// route of an SC-5/SC-6 screen registers as SC-4. Anything else is a
		// disagreement and stops the boot.
		want, ok := inventory[rt.Screen]
		switch {
		case !ok:
			res.Errors = append(res.Errors, where+": D11 인벤토리에 없는 화면")
		case want == rt.Class:
		case rt.Class == SC4 && (want == SC5 || want == SC6) && isSafeMethod(rt.Method):
		default:
			res.Errors = append(res.Errors,
				fmt.Sprintf("%s: 화면 유형이 D11 과 다르다 (라우트 %s / D11 %s)", where, rt.Class, want))
		}
	}

	// 2b (warning). A permission no route uses is dead: it still appears in the
	// role editor, so an operator grants it and nothing happens.
	var dead []string
	for _, p := range knownPerms {
		if !used[p] {
			dead = append(dead, p)
		}
	}
	sort.Strings(dead)
	for _, p := range dead {
		res.Warnings = append(res.Warnings, "어떤 라우트도 쓰지 않는 권한: "+p)
	}

	return res
}
