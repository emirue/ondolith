package app

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emirue/ondolith/internal/config"
)

// commerceRoutes is what FR-710 gates. 목록을 하드코딩하지 않고 트리에서 뽑는다
// — 새 커머스 라우트를 추가하면 자동으로 검사 대상이 된다.
func commerceRoutes(t *testing.T) []Route {
	t.Helper()
	shopOn := buildTree(nil, nil, nil, nil, nil, nil, true, noopHandler)
	shopOff := buildTree(nil, nil, nil, nil, nil, nil, false, noopHandler)

	off := map[string]bool{}
	for _, rt := range shopOff.Routes() {
		off[rt.Method+" "+rt.Pattern] = true
	}
	var only []Route
	for _, rt := range shopOn.Routes() {
		if !off[rt.Method+" "+rt.Pattern] {
			only = append(only, rt)
		}
	}
	if len(only) == 0 {
		t.Fatal("커머스 전용 라우트를 하나도 찾지 못했다 — 게이팅이 없거나 검사가 헛돌았다")
	}
	return only
}

func noopHandler(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }

// FR-710: cms 면 등록되지 않아 404 다. 숨김이 아니다.
func TestCommerceRoutesAreAbsentInCmsMode(t *testing.T) {
	only := commerceRoutes(t)

	mux := http.NewServeMux()
	buildTree(nil, nil, nil, nil, nil, nil, false, noopHandler).Mount(mux)

	for _, rt := range only {
		req, err := http.NewRequest(rt.Method, sampleURL(rt.Pattern), nil)
		if err != nil {
			t.Fatal(err)
		}
		// 단언은 "아무 것도 안 걸린다" 가 아니라 "커머스 핸들러에 닿지
		// 않는다" 이다. `/shop`·`/cart` 는 한 세그먼트라 페이지 catch-all
		// (`GET /{slug}`) 에 걸리고, 그 핸들러가 없는 페이지로 404 를 낸다 —
		// 그것이 D11 이 말하는 "등록되지 않아 404" 다.
		if _, pattern := mux.Handler(req); pattern == rt.Pattern {
			t.Errorf("cms 모드인데 %s %s 가 커머스 핸들러로 간다", rt.Method, rt.Pattern)
		}
	}
}

// shop 모드에서는 **정확히 그 패턴**이 걸린다.
//
// "무엇이든 걸린다" 로 두면 catch-all (`GET /{slug}`) 이 대신 걸린 것을 성공
// 으로 읽는다 — 실제로 그랬고, `/shop/{$}` 가 `/shop` 을 매칭하지 않는 것을
// 놓쳤다 (`{$}` 는 슬래시로 끝나는 경로만 잡는다).
func TestCommerceRoutesArePresentInShopMode(t *testing.T) {
	only := commerceRoutes(t)

	mux := http.NewServeMux()
	buildTree(nil, nil, nil, nil, nil, nil, true, noopHandler).Mount(mux)

	for _, rt := range only {
		req, err := http.NewRequest(rt.Method, sampleURL(rt.Pattern), nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, pattern := mux.Handler(req); pattern != rt.Method+" "+rt.Pattern {
			t.Errorf("shop 모드에서 %s %s 요청이 %q 로 갔다", rt.Method, rt.Pattern, pattern)
		}
	}
}

// 그리고 실제로 404 를 낸다. 라우트 표만 보면 catch-all 이 200 을 낼 수도
// 있는데, 그러면 cms 사이트의 /shop 이 빈 페이지로 열린다.
func TestCommercePathsAnswer404InCmsMode(t *testing.T) {
	// 기본 site.type 은 cms 다 (app.go). 실제 트리를 기동해서 확인한다 —
	// 라우트 표만 보면 catch-all 이 200 을 낼 수도 있다.
	srv, _ := liveSite(t)
	for _, path := range []string{"/shop", "/shop/search", "/shop/p/tee", "/cart"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("cms 모드 %s = HTTP %d, want 404", path, resp.StatusCode)
		}
	}
}

// shop 이면 열린다. 위 단언이 "무엇이든 404" 를 확인한 것이 아니라는 것.
func TestCommercePathsOpenInShopMode(t *testing.T) {
	// 먼저 한 번 기동해 스키마를 만들고, 설정을 shop 으로 바꾼 뒤 **다시**
	// 조립한다. 트리는 조립 시점에 정해지므로 (D20 「모듈 게이팅」) 이미 뜬
	// 서버는 설정만 바꿔서는 달라지지 않는다.
	_, pool := liveSite(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx,
		`INSERT INTO settings (key, value) VALUES ('site.type', 'shop')
		 ON CONFLICT (key) DO UPDATE SET value = 'shop'`); err != nil {
		t.Fatal(err)
	}

	var stored string
	if err := pool.QueryRow(ctx, `SELECT value FROM settings WHERE key = 'site.type'`).
		Scan(&stored); err != nil {
		t.Fatalf("설정이 저장되지 않았다: %v", err)
	}
	if stored != "shop" {
		t.Fatalf("site.type = %q", stored)
	}

	srv := restartOnSameSchema(t)
	for _, path := range []string{"/shop", "/shop/search", "/cart"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("shop 모드 %s = HTTP %d, want 200", path, resp.StatusCode)
		}
	}
	// 없는 상품은 shop 모드에서도 404 다.
	resp, err := http.Get(srv.URL + "/shop/p/없는상품")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("없는 상품 = HTTP %d, want 404", resp.StatusCode)
	}
}

// restartOnSameSchema starts a second app against the same database WITHOUT
// wiping it — liveSite drops the schema, which would undo the setting we just
// wrote.
func restartOnSameSchema(t *testing.T) *httptest.Server {
	t.Helper()
	cfg := &config.Config{DatabaseURL: os.Getenv(dsnEnv), SiteName: "테스트 사이트"}
	h, cleanup, err := New(context.Background(), cfg, "1.0.0",
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("재기동 실패: %v", err)
	}
	t.Cleanup(cleanup)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// sampleURL turns "/shop/p/{slug}" into a concrete path.
func sampleURL(pattern string) string {
	out := []string{}
	for _, seg := range strings.Split(strings.TrimPrefix(pattern, "/"), "/") {
		switch {
		case seg == "{$}":
			// 끝을 고정하는 표시다. 경로에는 남기지 않는다.
		case strings.HasPrefix(seg, "{"):
			out = append(out, "x")
		default:
			out = append(out, seg)
		}
	}
	return "http://example.com/" + strings.Join(out, "/")
}

// D20 「모듈 게이팅」: 핸들러 안에서 `if 커머스켜짐` 을 검사하지 않는다.
//
// 분기를 핸들러에 넣으면 새 커머스 라우트를 추가할 때마다 검사를 빠뜨릴 수
// 있고, 빠뜨리면 커머스를 끈 사이트에 결제 경로가 열린다. 조립 시점에만
// 정한다는 것이 이 검사의 내용이다.
func TestNoCommerceFlagInsideHandlers(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	// site.type 을 읽어도 되는 곳은 트리를 조립하는 app.go 와, 테마에 넘길
	// 사이트 정보를 만드는 곳뿐이다.
	allowed := map[string]bool{"app.go": true, "tree.go": true}

	found := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(src), `site.type`) && !strings.Contains(string(src), `"shop"`) {
			continue
		}
		found++
		if !allowed[name] {
			t.Errorf("%s 가 커머스 켜짐 여부를 읽는다 — 조립 시점에만 정해야 한다", name)
		}
	}
	if found == 0 {
		t.Fatal("site.type 을 읽는 파일을 하나도 찾지 못했다 — 검사가 헛돌았다")
	}
}

// 트리가 커머스를 켜고 끄는 것 말고는 같아야 한다. 게이팅이 코어 라우트를
// 함께 지우면 cms 사이트의 게시판이 사라진다.
func TestGatingOnlyAffectsCommerceRoutes(t *testing.T) {
	on := buildTree(nil, nil, nil, nil, nil, nil, true, noopHandler).Routes()
	off := buildTree(nil, nil, nil, nil, nil, nil, false, noopHandler).Routes()

	onSet := map[string]bool{}
	for _, rt := range on {
		onSet[rt.Method+" "+rt.Pattern] = true
	}
	for _, rt := range off {
		if !onSet[rt.Method+" "+rt.Pattern] {
			t.Errorf("cms 에만 있는 라우트: %s %s", rt.Method, rt.Pattern)
		}
	}
	if len(on) <= len(off) {
		t.Errorf("shop 라우트 %d개, cms %d개 — 켜도 늘지 않는다", len(on), len(off))
	}
}

// 커머스 화면이 D11 의 화면 인벤토리에 있고 보안 등급이 일치한다.
//
// screens.go 는 부팅 점검이 쓰는 표다. 새 화면을 트리에만 넣고 표에 넣지
// 않으면 그 점검이 그 화면을 모른다.
func TestCommerceScreensAreInTheInventory(t *testing.T) {
	for _, rt := range commerceRoutes(t) {
		class, ok := screenInventory[rt.Screen]
		if !ok {
			t.Errorf("%s 가 화면 인벤토리에 없다", rt.Screen)
			continue
		}
		if class != rt.Class {
			t.Errorf("%s 보안 등급: 트리 %v, 인벤토리 %v", rt.Screen, rt.Class, class)
		}
	}
}
