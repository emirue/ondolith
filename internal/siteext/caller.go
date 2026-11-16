package siteext

import (
	"context"
	"database/sql"
	"encoding/json"
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
}

type callerKey struct{}

// WithCaller attaches c to ctx; the /api/site/ gate calls it.
func WithCaller(ctx context.Context, c Caller) context.Context {
	return context.WithValue(ctx, callerKey{}, c)
}

// CallerFrom reads what WithCaller attached. ok is false outside a site API request.
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
