package content

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/emirue/ondolith/internal/migrations"
)

const dsnEnv = "ONDOLITH_TEST_DSN"

func testStore(t *testing.T) (*Store, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		t.Skipf("%s 미설정 — 통합 테스트를 건너뜁니다 (make test-integration)", dsnEnv)
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	for _, stmt := range []string{"DROP SCHEMA public CASCADE", "CREATE SCHEMA public"} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { db.Close() })
	if err := migrations.Run(ctx, db); err != nil {
		t.Fatal(err)
	}
	return NewStore(pool), pool
}

type countingTracer struct{ n atomic.Int64 }

func (c *countingTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	c.n.Add(1)
	return ctx
}
func (c *countingTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func tracedStore(t *testing.T) (*Store, *countingTracer) {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(os.Getenv(dsnEnv))
	if err != nil {
		t.Fatal(err)
	}
	tr := &countingTracer{}
	cfg.ConnConfig.Tracer = tr
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return NewStore(pool), tr
}

// A draft must not leave the database on the public path. The filter is in the
// WHERE clause so that a caller who forgets a Go-side check cannot leak one.
func TestPublishedFilterIsInSQL(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()

	id, err := s.CreatePage(ctx, Page{Slug: "about", Title: "회사", Body: "본문"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishedPageBySlug(ctx, "about"); !errors.Is(err, ErrNotFound) {
		t.Errorf("초안이 공개 경로로 나왔다: %v", err)
	}
	// The admin path still sees it.
	if _, err := s.PageBySlug(ctx, "about"); err != nil {
		t.Errorf("관리자 경로에서 초안을 못 찾는다: %v", err)
	}

	if err := s.SetPageStatus(ctx, id, StatusPublished); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishedPageBySlug(ctx, "about"); err != nil {
		t.Errorf("발행했는데 공개 경로에 없다: %v", err)
	}
}

// Every value is a bind parameter, so a slug that looks like SQL is data.
func TestSlugLookupIsParameterBound(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	if _, err := s.CreatePage(ctx, Page{Slug: "real", Title: "t", Body: "b"}); err != nil {
		t.Fatal(err)
	}

	// If this were concatenated the table would be gone and the next call would
	// error with "relation does not exist".
	evil := "x' OR '1'='1"
	if _, err := s.PageBySlug(ctx, evil); !errors.Is(err, ErrNotFound) {
		t.Errorf("주입 문자열이 행을 반환했다: %v", err)
	}
	if _, err := s.PageBySlug(ctx, "'; DROP TABLE pages; --"); !errors.Is(err, ErrNotFound) {
		t.Errorf("주입 문자열이 행을 반환했다: %v", err)
	}
	if _, err := s.PageBySlug(ctx, "real"); err != nil {
		t.Fatalf("주입 시도 뒤 테이블이 사라졌다: %v", err)
	}
}

func TestDuplicateSlugIsRefusedByTheDatabase(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	if _, err := s.CreatePage(ctx, Page{Slug: "dup", Title: "t", Body: "b"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreatePage(ctx, Page{Slug: "dup", Title: "t2", Body: "b2"}); !errors.Is(err, ErrSlugTaken) {
		t.Errorf("중복 슬러그 err = %v, want ErrSlugTaken", err)
	}
}

// page.update must not be able to publish. If UpdatePage carried a status, the
// separation of page.update and page.publish would be decorative.
func TestUpdateDoesNotChangeStatus(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	id, err := s.CreatePage(ctx, Page{Slug: "p", Title: "t", Body: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetPageStatus(ctx, id, StatusPublished); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdatePage(ctx, id, Page{Slug: "p", Title: "t2", Body: "b2"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.PageBySlug(ctx, "p")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusPublished {
		t.Errorf("수정이 상태를 %q 로 바꿨다", got.Status)
	}
	if got.Title != "t2" {
		t.Errorf("제목이 저장되지 않았다: %q", got.Title)
	}
}

// The state graph is enforced at the database boundary too, not only in the
// pure function — the handler is not the only caller.
func TestSetPageStatusRejectsBadTransition(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	id, err := s.CreatePage(ctx, Page{Slug: "p", Title: "t", Body: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetPageStatus(ctx, id, StatusDraft); !errors.Is(err, ErrTransitionBase) {
		t.Errorf("같은 상태로의 전이가 허용됐다: %v", err)
	}
	if err := s.SetPageStatus(ctx, id, PageStatus("archived")); !errors.Is(err, ErrStatusUnknown) {
		t.Errorf("알 수 없는 상태가 허용됐다: %v", err)
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()

	if err := s.PutSettings(ctx, map[string]string{"site.name": "온돌", "site.type": "cms"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Settings(ctx, "site.name", "site.type", "site.missing")
	if err != nil {
		t.Fatal(err)
	}
	if got["site.name"] != "온돌" || got["site.type"] != "cms" {
		t.Errorf("설정 = %v", got)
	}
	if _, ok := got["site.missing"]; ok {
		t.Error("없는 키가 반환됐다")
	}

	// Upsert, not insert: saving the same screen twice must not fail.
	if err := s.PutSettings(ctx, map[string]string{"site.name": "온돌리스"}); err != nil {
		t.Fatalf("두 번째 저장 실패: %v", err)
	}
	got2, _ := s.Settings(ctx, "site.name")
	if got2["site.name"] != "온돌리스" {
		t.Errorf("갱신되지 않았다: %v", got2)
	}
}

// The theme renders the menu on every public page, so depth must not multiply
// queries. One query, whatever the shape of the tree.
func TestMenuTreeIsOneQuery(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()

	root, err := s.CreateMenuItem(ctx, MenuItem{Title: "회사", URL: "/about", Sort: 1})
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.CreateMenuItem(ctx, MenuItem{Title: "연혁", URL: "/h", ParentID: root, Sort: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateMenuItem(ctx, MenuItem{Title: "손자", URL: "/g", ParentID: child, Sort: 1}); err != nil {
		t.Fatal(err)
	}

	traced, tr := tracedStore(t)
	items, err := traced.MenuItems(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n := tr.n.Load(); n != 1 {
		t.Errorf("메뉴 조회 쿼리 %d회, want 1회 (3단 트리)", n)
	}
	if len(items) != 3 {
		t.Fatalf("항목 %d개, want 3개", len(items))
	}

	tree, err := BuildMenu(items, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tree) != 1 || len(tree[0].Children) != 1 || len(tree[0].Children[0].Children) != 1 {
		t.Errorf("트리 모양이 다르다: %+v", tree)
	}
}

// D30 menus.parent_id is ON DELETE CASCADE: removing a parent removes the
// subtree rather than leaving rows whose parent is gone.
func TestDeletingParentRemovesSubtree(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	root, _ := s.CreateMenuItem(ctx, MenuItem{Title: "부모", URL: "/p"})
	if _, err := s.CreateMenuItem(ctx, MenuItem{Title: "자식", URL: "/c", ParentID: root}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteMenuItem(ctx, root); err != nil {
		t.Fatal(err)
	}
	items, err := s.MenuItems(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Errorf("부모를 지웠는데 %d개가 남았다", len(items))
	}
}

// The CHECK in D30 is a fail-closed backstop behind the handler's validation.
// It has to actually be reachable from this path.
func TestMenuURLCheckRejectsProtocolRelative(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	if _, err := s.CreateMenuItem(ctx, MenuItem{Title: "x", URL: "//evil.com"}); err == nil {
		t.Error("프로토콜 상대 URL 이 저장됐다")
	}
}
