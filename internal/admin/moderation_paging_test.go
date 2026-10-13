package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

// 글·댓글·첨부 관리(A-307·A-308·A-309)가 최신 100·100·200행만 보여 주고 쪽이
// 없어서, 그보다 오래된 숨긴 글·댓글·첨부에는 닿는 화면이 없었다 — 숨김을 풀 수도
// 지울 수도 없었다. 한 쪽을 넘치면 다음 쪽이 있고, 마지막 쪽에 남은 행이 나온다.
func TestModerationListsPageThroughEverything(t *testing.T) {
	caller := &fakeCaller{perms: map[string]bool{"admin.access": true},
		scoped: map[string]map[string]bool{}}
	d, pool := fixture(t, caller)
	ctx := context.Background()

	var board string
	if err := pool.QueryRow(ctx,
		`INSERT INTO boards (slug, name) VALUES ('mod', '관리') RETURNING id`).Scan(&board); err != nil {
		t.Fatal(err)
	}
	caller.scoped[board] = map[string]bool{"post.moderate": true, "comment.moderate": true}
	var first string
	if err := pool.QueryRow(ctx, `
		WITH p AS (INSERT INTO posts (board_id, title, body, created_at)
		           SELECT $1, '글 ' || g, '본문', now() - g * interval '1 minute'
		           FROM generate_series(1, 101) g RETURNING id, created_at)
		SELECT id FROM p ORDER BY created_at DESC LIMIT 1`, board).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO comments (post_id, body, created_at)
		SELECT $1, '댓글 ' || g, now() - g * interval '1 minute' FROM generate_series(1, 101) g`, first); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO attachments (post_id, stored_path, original_name, mime_type, byte_size, created_at)
		SELECT $1, '2026/10/' || gen_random_uuid(), g || '.png', 'image/png', 1, now() - g * interval '1 minute'
		FROM generate_series(1, 201) g`, first); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		path    string
		rows    string
		perPage int
	}{
		{"글", d.PostList, "/admin/posts", "Posts", 100},
		{"댓글", d.CommentList, "/admin/comments", "Comments", 100},
		{"첨부", d.AttachmentList, "/admin/attachments", "Attachments", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			get := func(page string) map[string]any {
				t.Helper()
				var drawn map[string]any
				d.Render = func(w http.ResponseWriter, _ *http.Request, _ string, code int, data any) {
					drawn, _ = data.(map[string]any)
					w.WriteHeader(code)
				}
				target := tc.path + "?board=mod" + page
				rec := httptest.NewRecorder()
				tc.handler(rec, httptest.NewRequest(http.MethodGet, target, nil))
				if rec.Code != http.StatusOK || drawn == nil {
					t.Fatalf("%s = HTTP %d", target, rec.Code)
				}
				return drawn
			}
			one, two := get(""), get("&page=2")
			if n := reflect.ValueOf(one[tc.rows]).Len(); n != tc.perPage {
				t.Errorf("1쪽 %d행, want %d", n, tc.perPage)
			}
			if n := reflect.ValueOf(two[tc.rows]).Len(); n != 1 {
				t.Errorf("2쪽 %d행, want 1 — 한 쪽을 넘친 행에 닿지 못한다", n)
			}
			if one["NextURL"] != tc.path+"?board=mod&page=2" {
				t.Errorf("1쪽의 다음 = %v", one["NextURL"])
			}
			if two["PrevURL"] != tc.path+"?board=mod" || two["NextURL"] != nil {
				t.Errorf("2쪽의 이전 = %v, 다음 = %v", two["PrevURL"], two["NextURL"])
			}
		})
	}
}

// 2쪽에서 조작하면 2쪽으로 돌아온다 — 1쪽으로 튕기면 오래된 글을 하나씩 풀 때마다 다시
// 넘겨 와야 한다.
func TestModerationActionReturnsToItsPage(t *testing.T) {
	caller := &fakeCaller{perms: map[string]bool{"admin.access": true},
		scoped: map[string]map[string]bool{}}
	d, pool := fixture(t, caller)
	ctx := context.Background()
	var board, post string
	if err := pool.QueryRow(ctx,
		`INSERT INTO boards (slug, name) VALUES ('mod', '관리') RETURNING id`).Scan(&board); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO posts (board_id, title, body) VALUES ($1, '글', '본문') RETURNING id`, board).Scan(&post); err != nil {
		t.Fatal(err)
	}
	caller.scoped[board] = map[string]bool{"post.moderate": true}

	req := httptest.NewRequest(http.MethodPost, "/admin/posts", strings.NewReader(url.Values{
		"post_id": {post}, "action": {"hide"}, "page": {"2"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	d.PostModerate(rec, req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/posts?board=mod&page=2" {
		t.Errorf("= HTTP %d → %q, want 303 → /admin/posts?board=mod&page=2", rec.Code, rec.Header().Get("Location"))
	}
}
