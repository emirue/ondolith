// Command site is the smallest site binary built on the core ([D42](../../docs/42-site-extension.md)):
// a site_ table of its own, an app API under /api/site/v1/checkins and admin screens under /admin/site/checkins.
//
// 사이트 저장소는 이것을 본떠 자기 go.mod 와 main.go 를 만든다. 코어 저장소 안에 두는 이유는 하나다 — 공개 패키지
// site 를 바꿔 이 예제가 깨지면 `make check`(빌드)와 `make e2e`(scripts/site-e2e.sh 한 바퀴)가 먼저 안다.
package main

import (
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"io/fs"
	"net/http"
	"strconv"
	"time"

	"github.com/emirue/ondolith/site"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

func main() {
	migrations, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		panic(err) // embed 의 디렉터리 이름이 틀렸다 — 빌드한 사람의 실수다
	}
	site.Main(site.Extension{
		Migrations: migrations,
		API: []site.Route{
			{Method: "POST", Path: "/v1/checkins", Auth: site.App, Handler: checkIn},
			{Method: "GET", Path: "/v1/checkins", Auth: site.App, Handler: listCheckins},
		},
		// 역할 편집기(A-403)에서 이 권한을 준 관리자만 아래 화면을 본다. 최고 관리자는 늘 본다.
		Permissions: []site.Permission{{Key: "site.checkins", Name: "출퇴근 관리"}},
		Admin: []site.AdminPage{
			{Menu: "출퇴근", Path: "/checkins", Permission: "site.checkins", Handler: adminCheckins},
			// 메뉴 없는 상세 화면 — 목록에서 눌러 들어간다. id 는 bigserial 이다.
			{Path: "/checkins/{id}", Permission: "site.checkins", Handler: adminCheckin},
		},
	})
}

type checkin struct {
	Worker string    `json:"worker"`
	Kind   string    `json:"kind"`
	At     time.Time `json:"at"`
}

// checkIn records 출근(in)·퇴근(out). 사람 구분은 문자 인증 로그인(D42 3단계)이 들어오면 Subject 로 바뀐다 — 지금은
// 본문의 worker 를 믿는 예제다.
func checkIn(w http.ResponseWriter, r *http.Request) {
	var in checkin
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Worker == "" || len([]rune(in.Worker)) > 40 ||
		(in.Kind != "in" && in.Kind != "out") {
		site.Error(w, http.StatusBadRequest, "worker(1~40자)와 kind(in 또는 out)가 필요합니다.")
		return
	}
	c := site.From(r)
	if err := c.DB.QueryRowContext(r.Context(),
		`INSERT INTO site_example_checkins (worker, kind, app) VALUES ($1, $2, $3) RETURNING created_at`,
		in.Worker, in.Kind, c.App).Scan(&in.At); err != nil {
		c.Log.Error("출퇴근 기록", "err", err)
		site.Error(w, http.StatusInternalServerError, "기록하지 못했습니다.")
		return
	}
	site.JSON(w, http.StatusCreated, in)
}

func listCheckins(w http.ResponseWriter, r *http.Request) {
	c := site.From(r)
	rows, err := c.DB.QueryContext(r.Context(),
		`SELECT worker, kind, created_at FROM site_example_checkins ORDER BY id DESC LIMIT 100`)
	if err != nil {
		c.Log.Error("출퇴근 목록", "err", err)
		site.Error(w, http.StatusInternalServerError, "읽지 못했습니다.")
		return
	}
	defer rows.Close()
	out := []checkin{}
	for rows.Next() {
		var k checkin
		if err := rows.Scan(&k.Worker, &k.Kind, &k.At); err != nil {
			site.Error(w, http.StatusInternalServerError, "읽지 못했습니다.")
			return
		}
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		site.Error(w, http.StatusInternalServerError, "읽지 못했습니다.")
		return
	}
	site.JSON(w, http.StatusOK, map[string]any{"checkins": out})
}

// row is one check-in as the admin screens show it.
type row struct {
	ID     int64
	Worker string
	Kind   string
	App    string
	At     time.Time
}

// 관리자 화면의 템플릿. 각 묶음은 content 블록을 정의한다 — site.RenderAdmin 이 그것을 코어의 관리자 틀 안에 그린다.
// 오류 문장은 사이트가 그린다(코어의 모양을 쓴다). 지우기처럼 되돌릴 수 없는 폼은 재확인 칸(.ReauthField)과 확인 창을 단다.
var (
	listPage = template.Must(template.New("list").Parse(`{{define "content"}}
<div class="adm-card">
  <div class="adm-table-wrap">
    <table class="adm-table">
      <thead><tr><th>인부</th><th>구분</th><th>시각</th><th>앱</th></tr></thead>
      <tbody>
      {{range .Rows}}<tr><td><a href="/admin/site/checkins/{{.ID}}">{{.Worker}}</a></td><td>{{if eq .Kind "in"}}출근{{else}}퇴근{{end}}</td>
        <td>{{.At.Format "2006-01-02 15:04"}}</td><td>{{.App}}</td></tr>
      {{else}}<tr><td colspan="4">기록이 없습니다.</td></tr>{{end}}
      </tbody>
    </table>
  </div>
</div>{{end}}`))
	detailPage = template.Must(template.New("detail").Parse(`{{define "content"}}
{{with .Error}}<p class="adm-toast adm-toast--error" role="alert">{{.}}</p>{{end}}
<div class="adm-card">
  <p>{{.Row.Worker}} · {{if eq .Row.Kind "in"}}출근{{else}}퇴근{{end}} · {{.Row.At.Format "2006-01-02 15:04"}} · {{.Row.App}}</p>
  <form method="post" action="/admin/site/checkins/{{.Row.ID}}">
    {{.ReauthField}}
    <button class="adm-btn adm-btn--danger" onclick="return confirm('이 기록을 지울까요? 되돌릴 수 없습니다.')">기록 지우기</button>
  </form>
  <p><a href="/admin/site/checkins">목록으로</a></p>
</div>{{end}}`))
)

func adminCheckins(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "허용되지 않는 메서드입니다.", http.StatusMethodNotAllowed)
		return
	}
	c := site.From(r)
	rows, err := c.DB.QueryContext(r.Context(),
		`SELECT id, worker, kind, app, created_at FROM site_example_checkins ORDER BY id DESC LIMIT 100`)
	if err != nil {
		c.Log.Error("출퇴근 목록", "err", err)
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	var out []row
	for rows.Next() {
		var k row
		if err := rows.Scan(&k.ID, &k.Worker, &k.Kind, &k.App, &k.At); err != nil {
			http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
			return
		}
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	site.RenderAdmin(w, r, http.StatusOK, "출퇴근 기록", listPage, map[string]any{"Rows": out})
}

// adminCheckin shows one record (GET) and deletes it (POST). 지우기는 되돌릴 수 없어 비밀번호 재확인을 거친다.
func adminCheckin(w http.ResponseWriter, r *http.Request) {
	c := site.From(r)
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r) // 코어는 사이트 화면의 {id} 형식을 보지 않는다 — 사이트 표의 것이라 사이트가 본다
		return
	}
	var k row
	err = c.DB.QueryRowContext(r.Context(),
		`SELECT id, worker, kind, app, created_at FROM site_example_checkins WHERE id = $1`, id).
		Scan(&k.ID, &k.Worker, &k.Kind, &k.App, &k.At)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		c.Log.Error("출퇴근 기록", "err", err)
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	view := map[string]any{"Row": k, "ReauthField": site.ReauthField(r)}
	if r.Method != http.MethodPost {
		site.RenderAdmin(w, r, http.StatusOK, "출퇴근 기록", detailPage, view)
		return
	}
	if !site.Reauth(r) {
		view["Error"] = "비밀번호를 다시 입력하세요."
		site.RenderAdmin(w, r, http.StatusForbidden, "출퇴근 기록", detailPage, view)
		return
	}
	if _, err := c.DB.ExecContext(r.Context(), `DELETE FROM site_example_checkins WHERE id = $1`, id); err != nil {
		c.Log.Error("출퇴근 기록 지우기", "err", err)
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	site.OpLog(r, "site.checkin_delete", strconv.FormatInt(id, 10), k.Worker+" "+k.Kind+" 기록을 지웠다")
	site.Done(w, r, "/admin/site/checkins", "기록을 지웠습니다.")
}
