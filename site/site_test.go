package site

import (
	"database/sql"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"testing/fstest"

	"github.com/emirue/ondolith/internal/siteext"
)

// 공개 타입이 코어 쪽 타입으로 칸 하나 빠짐없이 옮겨진다 — 빠지면 사이트가 등록한 것이 조용히 사라진다.
func TestExtensionCarriesEverythingToTheCore(t *testing.T) {
	h := func(http.ResponseWriter, *http.Request) {}
	mig := fstest.MapFS{"00001_a.sql": {Data: []byte("-- +goose Up\n")}}
	got := Extension{Migrations: mig, API: []Route{{Method: "GET", Path: "/v1/a", Auth: App, Handler: h}, {Method: "POST", Path: "/v1/b", Auth: App, Handler: h}},
		Permissions: []Permission{{Key: "site.a", Name: "가"}, {Key: "site.b", Name: "나"}},
		Admin:       []AdminPage{{Menu: "메뉴", Path: "/a", Permission: "site.a", Handler: h}, {Path: "/a/{id}", Permission: "site.b", Handler: h}},
	}.internal()
	if got.Migrations == nil || len(got.API) != 2 || got.API[1].Method != "POST" || got.API[1].Path != "/v1/b" ||
		got.API[0].Handler == nil || got.API[0].Auth != siteext.AuthApp {
		t.Fatalf("internal() = %+v", got)
	}
	if len(got.Permissions) != 2 || got.Permissions[1] != (siteext.Permission{Key: "site.b", Name: "나"}) ||
		len(got.Admin) != 2 || got.Admin[0].Menu != "메뉴" || got.Admin[0].Path != "/a" || got.Admin[1].Permission != "site.b" ||
		got.Admin[1].Handler == nil {
		t.Fatalf("internal() 관리자 몫 = %+v %+v", got.Permissions, got.Admin)
	}
	// 손으로 본 칸 밖의 칸이 생기면 여기서 걸린다 — 공개 타입에 칸을 더하고 옮기기를 빠뜨리면 사이트가 등록한 것이
	// 조용히 사라진다. 양쪽 칸 수가 같아야 하고, 다르면 위 단언과 internal()·From 을 함께 고친다.
	for _, pair := range [][2]any{{Extension{}, siteext.Extension{}}, {Route{}, siteext.Route{}}, {Context{}, siteext.Caller{}},
		{Permission{}, siteext.Permission{}}, {AdminPage{}, siteext.AdminPage{}}, {Actor{}, siteext.Actor{}}} {
		if a, b := reflect.TypeOf(pair[0]), reflect.TypeOf(pair[1]); a.NumField() != b.NumField() {
			t.Errorf("%s 의 칸 %d개, %s 의 칸 %d개 — 옮기기를 함께 고친다", a, a.NumField(), b, b.NumField())
		}
	}
}

// 공개 상수와 코어 쪽 값이 같다 — 다르면 site.App 이 부팅 검사에서 「인증 방식 없음」으로 거부된다.
func TestAuthValuesMatchTheCore(t *testing.T) {
	if siteext.Auth(App) != siteext.AuthApp {
		t.Errorf("site.App = %d, siteext.AuthApp = %d", App, siteext.AuthApp)
	}
}

// From 은 코어가 확인한 값을 그대로 준다. 사이트 API 처리기 밖이면 빈 값이다.
func TestFromReadsWhatTheCoreChecked(t *testing.T) {
	if c := From(httptest.NewRequest("GET", "/", nil)); c.DB != nil || c.App != "" || c.Log != nil {
		t.Errorf("처리기 밖 = %+v", c)
	}
	db, log := &sql.DB{}, slog.New(slog.DiscardHandler)
	r := httptest.NewRequest("GET", "/api/site/v1/a", nil)
	r = r.WithContext(siteext.WithCaller(r.Context(), siteext.Caller{DB: db, App: "flutter", Log: log,
		Actor: siteext.Actor{ID: "u1", Name: "관리자"}}))
	if c := From(r); c.DB != db || c.App != "flutter" || c.Log != log || c.Actor != (Actor{ID: "u1", Name: "관리자"}) {
		t.Errorf("From = %+v", c)
	}
}
