package site

import (
	"database/sql"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/emirue/ondolith/internal/siteext"
)

// 공개 타입이 코어 쪽 타입으로 칸 하나 빠짐없이 옮겨진다 — 빠지면 사이트가 등록한 것이 조용히 사라진다.
func TestExtensionCarriesEverythingToTheCore(t *testing.T) {
	h := func(http.ResponseWriter, *http.Request) {}
	mig := fstest.MapFS{"00001_a.sql": {Data: []byte("-- +goose Up\n")}}
	got := Extension{Migrations: mig, API: []Route{{Method: "GET", Path: "/v1/a", Handler: h}, {Method: "POST", Path: "/v1/b", Handler: h}}}.internal()
	if got.Migrations == nil || len(got.API) != 2 || got.API[1].Method != "POST" || got.API[1].Path != "/v1/b" || got.API[0].Handler == nil {
		t.Fatalf("internal() = %+v", got)
	}
}

// From 은 코어가 확인한 값을 그대로 준다. 사이트 API 처리기 밖이면 빈 값이다.
func TestFromReadsWhatTheCoreChecked(t *testing.T) {
	if c := From(httptest.NewRequest("GET", "/", nil)); c.DB != nil || c.App != "" || c.Log != nil {
		t.Errorf("처리기 밖 = %+v", c)
	}
	db, log := &sql.DB{}, slog.New(slog.DiscardHandler)
	r := httptest.NewRequest("GET", "/api/site/v1/a", nil)
	r = r.WithContext(siteext.WithCaller(r.Context(), siteext.Caller{DB: db, App: "flutter", Log: log}))
	if c := From(r); c.DB != db || c.App != "flutter" || c.Log != log {
		t.Errorf("From = %+v", c)
	}
}
