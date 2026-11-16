package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/emirue/ondolith/internal/admin"
	"github.com/emirue/ondolith/internal/apikeys"
	"github.com/emirue/ondolith/internal/auth"
	"github.com/emirue/ondolith/internal/config"
	"github.com/emirue/ondolith/internal/content"
	"github.com/emirue/ondolith/internal/siteext"
)

// **문자 로그인 한 바퀴, 진짜 조립과 진짜 DB 로** (D42 8절). 인증번호는 응답 밖에서 나가고(사이트의 발송 함수가
// 받는다), 그 번호로 받은 토큰으로 site.Person 라우트가 사람을 안다. 관리자가 사이트 화면에서 EndSessions 를 부르면
// 그 사람의 토큰은 401 이다. 등록되지 않은 번호에는 아무것도 나가지 않고, 발송 실패는 번호를 가려 작업 로그에 남는다.
func TestPhoneLoginThroughTheCore(t *testing.T) {
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
	texts := make(chan [2]string, 10)
	ext := notesExtension()
	ext.Phone = siteext.PhoneLogin{Lookup: func(_ context.Context, phone string) (string, bool, error) {
		switch phone {
		case "01011112222":
			return "w-1", true, nil
		case "01077778888":
			return "w-broken", true, nil // 발송이 실패하는 사람
		}
		return "", false, nil
	}}
	ext.SMS = func(_ context.Context, to, text string) error {
		if to == "01077778888" {
			return errors.New("업체가 거절했다")
		}
		texts <- [2]string{to, text}
		return nil
	}
	ext.API = append(ext.API, siteext.Route{Method: "GET", Path: "/v1/me", Auth: siteext.AuthPerson,
		Handler: func(w http.ResponseWriter, r *http.Request) {
			c, _ := siteext.CallerFrom(r.Context())
			siteext.WriteJSON(w, http.StatusOK, map[string]string{"subject": c.Subject})
		}})
	ext.Permissions = []siteext.Permission{{Key: "site.workers", Name: "인부 관리"}}
	ext.Admin = []siteext.AdminPage{{Path: "/workers/{id}/leave", Permission: "site.workers",
		Handler: func(w http.ResponseWriter, r *http.Request) {
			a, _ := siteext.AdminFrom(r.Context())
			if err := a.EndSessions(r.Context(), r.PathValue("id")); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}}}
	cfg := &config.Config{DatabaseURL: dsn, SiteName: "주원산림", UploadDir: t.TempDir()}
	h, cleanup, err := NewSite(ctx, cfg, "1.0.0", "", slog.New(slog.DiscardHandler), nil, ext)
	if err != nil {
		t.Fatalf("기동 실패: %v", err)
	}
	t.Cleanup(cleanup)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	if err := content.NewStore(pool).PutSettings(ctx, map[string]string{admin.SettingAPIEnabled: "1",
		"site.name": "주원산림", "auth.email_verification_required": ""}); err != nil {
		t.Fatal(err)
	}
	key, _, err := apikeys.New(pool).Issue(ctx, "flutter", nil)
	if err != nil {
		t.Fatal(err)
	}
	app := func(method, path, body string, hdr ...string) (int, string) {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+key)
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}

	if code, body := app("POST", "/api/site/auth/code", `{"phone":"010-1111-2222"}`); code != http.StatusAccepted {
		t.Fatalf("인증번호 요청 = %d %s", code, body)
	}
	var sent [2]string
	select {
	case sent = <-texts:
	case <-time.After(5 * time.Second):
		t.Fatal("인증번호 문자가 나가지 않았다")
	}
	m := regexp.MustCompile(`^\[주원산림\] 인증번호 ([0-9]{6}) \(5분\)$`).FindStringSubmatch(sent[1])
	if sent[0] != "01011112222" || m == nil {
		t.Fatalf("보낸 문자 = %v", sent)
	}
	code, body := app("POST", "/api/site/auth/verify", `{"phone":"01011112222","code":"`+m[1]+`"}`)
	var out struct{ Token string }
	if code != 200 || json.Unmarshal([]byte(body), &out) != nil || out.Token == "" {
		t.Fatalf("확인 = %d %s", code, body)
	}
	if code, body := app("GET", "/api/site/v1/me", "", "X-Ondolith-Session", out.Token); code != 200 || !strings.Contains(body, `"subject":"w-1"`) {
		t.Fatalf("토큰으로 /v1/me = %d %s", code, body)
	}
	if code, _ := app("GET", "/api/site/v1/me", ""); code != http.StatusUnauthorized {
		t.Errorf("토큰 없이 /v1/me = %d, want 401", code)
	}

	// 등록되지 않은 번호 — 같은 응답, 문자는 없다.
	if code, _ := app("POST", "/api/site/auth/code", `{"phone":"01055556666"}`); code != http.StatusAccepted {
		t.Errorf("미등록 번호 = %d, want 202", code)
	}
	// 발송이 실패하는 번호 — 같은 응답, 실패는 번호를 가려 작업 로그에.
	if code, _ := app("POST", "/api/site/auth/code", `{"phone":"01077778888"}`); code != http.StatusAccepted {
		t.Errorf("발송 실패 번호 = %d, want 202", code)
	}
	deadline := time.Now().Add(5 * time.Second)
	var failed int
	for time.Now().Before(deadline) {
		_ = pool.QueryRow(ctx, `SELECT count(*) FROM operation_logs WHERE action = 'sms.failed' AND target_id = '010****8888'
			AND position('01077778888' in summary) = 0`).Scan(&failed)
		if failed == 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if failed != 1 {
		t.Error("발송 실패가 가린 번호로 작업 로그에 남지 않았다")
	}
	select {
	case got := <-texts:
		t.Errorf("미등록 번호에 문자가 나갔다: %v", got)
	default:
	}

	// 관리자가 사이트 화면에서 그 사람을 끊는다.
	store := auth.NewStore(pool)
	bossID := mkUser(t, pool, "boss@example.com")
	grantSuperuser(t, store, bossID)
	boss := client()
	login(t, srv.URL, "boss@example.com", boss)
	res := httpPost(t, boss, srv.URL+"/admin/site/workers/w-1/leave", url.Values{})
	res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("관리자 EndSessions = HTTP %d", res.StatusCode)
	}
	if code, _ := app("GET", "/api/site/v1/me", "", "X-Ondolith-Session", out.Token); code != http.StatusUnauthorized {
		t.Errorf("끊은 사람의 토큰 = %d, want 401", code)
	}
}
