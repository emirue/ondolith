package app

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/emirue/ondolith/internal/admin"
	"github.com/emirue/ondolith/internal/apikeys"
	"github.com/emirue/ondolith/internal/config"
	"github.com/emirue/ondolith/internal/content"
	"github.com/emirue/ondolith/internal/siteext"
	"github.com/jackc/pgx/v5/pgxpool"
)

// notesExtension is a tiny site: a site_notes table and GET/POST /v1/notes that use the DB the core hands over.
func notesExtension() siteext.Extension {
	return siteext.Extension{
		Migrations: fstest.MapFS{"00001_notes.sql": {Data: []byte("-- +goose Up\n" +
			"CREATE TABLE site_notes (id bigserial PRIMARY KEY, body text NOT NULL, app text NOT NULL);\n" +
			"-- +goose Down\nDROP TABLE site_notes;\n")}},
		API: []siteext.Route{
			{Method: "GET", Path: "/v1/notes", Handler: func(w http.ResponseWriter, r *http.Request) {
				c, _ := siteext.CallerFrom(r.Context())
				rows, err := c.DB.QueryContext(r.Context(), `SELECT body FROM site_notes ORDER BY id`)
				if err != nil {
					siteext.WriteError(w, 500, err.Error())
					return
				}
				defer rows.Close()
				notes := []string{}
				for rows.Next() {
					var b string
					_ = rows.Scan(&b)
					notes = append(notes, b)
				}
				siteext.WriteJSON(w, http.StatusOK, map[string]any{"notes": notes})
			}},
			{Method: "POST", Path: "/v1/notes", Handler: func(w http.ResponseWriter, r *http.Request) {
				c, _ := siteext.CallerFrom(r.Context())
				var in struct{ Body string }
				if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Body == "" {
					siteext.WriteError(w, http.StatusBadRequest, "body 가 필요합니다.")
					return
				}
				if _, err := c.DB.ExecContext(r.Context(), `INSERT INTO site_notes (body, app) VALUES ($1, $2)`, in.Body, c.App); err != nil {
					siteext.WriteError(w, 500, err.Error())
					return
				}
				w.WriteHeader(http.StatusCreated)
			}},
		},
	}
}

// **사이트 실행 파일 한 바퀴** (D42 1단계): 사이트 표가 생기고, 관리자가 켜고 키를 주면 앱이 그 키로 /api/site/ 를
// 부르며, 처리기는 코어가 준 DB 로 사이트 표를 읽고 쓴다. 끄면 404, 폐기한 키는 401. 사이트 API 는 본 트리 밖이라
// 세션 쿠키를 받지 않는다.
func TestSiteExtensionServesTheAppThroughTheCore(t *testing.T) {
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
	for _, s := range []string{"DROP SCHEMA public CASCADE", "CREATE SCHEMA public"} {
		if _, err := pool.Exec(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{DatabaseURL: dsn, SiteName: "사이트", UploadDir: t.TempDir()}
	h, cleanup, err := NewSite(ctx, cfg, "1.0.0", slog.New(slog.DiscardHandler), nil, notesExtension())
	if err != nil {
		t.Fatalf("기동 실패 — 부팅 검사가 사이트 라우트를 받아들여야 한다: %v", err)
	}
	t.Cleanup(cleanup)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	var tables int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_name IN ('site_notes', 'site_goose_db_version')`).Scan(&tables); err != nil || tables != 2 {
		t.Fatalf("사이트 표와 사이트 버전 표 %d개, want 2 (%v)", tables, err)
	}

	keys := apikeys.New(pool)
	raw, k, err := keys.Issue(ctx, "flutter", nil)
	if err != nil {
		t.Fatal(err)
	}
	app := func(method, path, key, body string) (int, string, http.Header) {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b), res.Header
	}

	if code, body, _ := app("GET", "/api/site/v1/notes", raw, ""); code != http.StatusNotFound {
		t.Fatalf("켜기 전: HTTP %d %s, want 404", code, body)
	}
	if err := content.NewStore(pool).PutSettings(ctx, map[string]string{admin.SettingAPIEnabled: "1"}); err != nil {
		t.Fatal(err)
	}
	if code, body, _ := app("POST", "/api/site/v1/notes", raw, `{"body":"첫 출근"}`); code != http.StatusCreated {
		t.Fatalf("쓰기: HTTP %d %s", code, body)
	}
	code, body, hdr := app("GET", "/api/site/v1/notes", raw, "")
	// 본 트리 밖이다 — 세션 미들웨어는 모든 응답에 Vary: Cookie 를 붙이고, 쿠키를 심을 수 있다.
	if code != http.StatusOK || !strings.Contains(body, "첫 출근") || hdr.Get("Set-Cookie") != "" ||
		strings.Contains(hdr.Get("Vary"), "Cookie") {
		t.Fatalf("읽기: HTTP %d %s, Set-Cookie %q, Vary %q — 세션이 붙으면 안 된다", code, body, hdr.Get("Set-Cookie"), hdr.Get("Vary"))
	}
	if hdr.Get("X-Frame-Options") == "" {
		t.Error("본 트리 밖이어도 보안 헤더는 진다")
	}
	var app2 string
	if err := pool.QueryRow(ctx, `SELECT app FROM site_notes`).Scan(&app2); err != nil || app2 != "flutter" {
		t.Errorf("처리기가 받은 앱 이름 = %q, %v", app2, err)
	}
	for _, tc := range []struct {
		name, method, path, key string
		want                    int
	}{
		{"키 없음", "GET", "/api/site/v1/notes", "", 401},
		{"없는 주소", "GET", "/api/site/v1/none", raw, 404},
		{"사이트 밖 주소는 그대로 테마 404", "GET", "/api/other", raw, 404},
	} {
		code, _, h := app(tc.method, tc.path, tc.key, "")
		json := strings.HasPrefix(h.Get("Content-Type"), "application/json")
		if code != tc.want || json != strings.HasPrefix(tc.path, "/api/site/") {
			t.Errorf("%s: HTTP %d (JSON %v), want %d — /api/site/ 아래만 JSON 이다", tc.name, code, json, tc.want)
		}
	}
	if _, err := keys.Revoke(ctx, k.ID); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := app("GET", "/api/site/v1/notes", raw, ""); code != http.StatusUnauthorized {
		t.Errorf("폐기한 키: HTTP %d, want 401", code)
	}
}
