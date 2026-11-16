package siteext

import (
	"context"
	"database/sql"
	"encoding/json"
	"html/template"
	"log/slog"
	"net/http"
)

// Caller is what the core has already checked about a site API request (D42 3절 From). 처리기는
// 다시 확인하지 않는다.
type Caller struct {
	// DB is database/sql, not pgx — pgx 의 주 판이 공개 약속에 들어가지 않게 (D42 3절).
	DB *sql.DB
	// App is the name of the app key the request carried.
	App string
	// Log already carries the app name.
	Log *slog.Logger
	// Actor is the administrator on a site admin page; zero on the app API.
	Actor Actor
	// Subject is who logged in by phone, on an AuthPerson route; "" elsewhere.
	Subject string
}

// Actor is who opened a site admin page — 이메일은 넣지 않는다(화면에 찍히면 캡처 한 장으로 샌다).
type Actor struct {
	ID   string
	Name string
}

// Admin is what the core lends a site admin page (D42 7절). app 이 채워 요청에 붙이고, 공개 패키지 site 가 쓴다.
type Admin struct {
	// Render draws body inside the admin layout with that status and heading.
	Render func(w http.ResponseWriter, r *http.Request, status int, title string, body template.HTML)
	// Notify carries a notice to the screen at path to (FR-724).
	Notify func(to, msg string)
	// NeedsReauth reports whether the 15-minute reauth window is closed (D15 5.3-1).
	NeedsReauth func() bool
	// ConfirmReauth checks the administrator's password — 계정당 상한을 거친다 — and opens the window.
	ConfirmReauth func(password string) bool
	// ReauthField is the core's password field that a form sends with a guarded action.
	ReauthField template.HTML
	// Record writes one op-log line (A-601).
	Record func(action, target, summary string) error
	// EndSessions ends every app token of subject (D42 8절) — 그만둔 사람.
	EndSessions func(ctx context.Context, subject string) error
}

type adminKey struct{}

// WithAdmin attaches a to ctx; the core's wrapper around a site admin page calls it.
func WithAdmin(ctx context.Context, a Admin) context.Context {
	return context.WithValue(ctx, adminKey{}, a)
}

// AdminFrom reads what WithAdmin attached. ok is false outside a site admin page.
func AdminFrom(ctx context.Context) (Admin, bool) {
	a, ok := ctx.Value(adminKey{}).(Admin)
	return a, ok
}

type callerKey struct{}

// WithCaller attaches c to ctx; the /api/site/ gate and the site admin wrapper call it.
func WithCaller(ctx context.Context, c Caller) context.Context {
	return context.WithValue(ctx, callerKey{}, c)
}

// CallerFrom reads what WithCaller attached. ok is false outside a site request.
func CallerFrom(ctx context.Context) (Caller, bool) {
	c, ok := ctx.Value(callerKey{}).(Caller)
	return c, ok
}

// WriteJSON answers with v as JSON. 앱 응답에는 사람마다 다른 값이 들 수 있어 공유 캐시에 남기지 않는다.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// WriteError is the one error shape of the site API: {"error": "사람이 읽는 문장"} (D42 5절).
func WriteError(w http.ResponseWriter, status int, msg string) {
	WriteJSON(w, status, map[string]string{"error": msg})
}
