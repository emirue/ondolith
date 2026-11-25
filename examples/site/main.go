// Command site is the smallest site binary built on the core ([D42](../../docs/42-site-extension.md)):
// site_ tables of its own, an app API under /api/site/v1/, admin screens under /admin/site/, and phone login
// for its workers.
//
// 사이트 저장소는 이것을 본떠 자기 go.mod 와 main.go 를 만든다. 코어 저장소 안에 두는 이유는 하나다 — 공개 패키지
// site 를 바꿔 이 예제가 깨지면 `make check`(빌드)와 `make e2e`(scripts/site-e2e.sh 한 바퀴)가 먼저 안다.
package main

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
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
			// 로그인한 인부 자신의 것 — 사람마다 다른 데이터라 site.Person 이다.
			{Method: "GET", Path: "/v1/me", Auth: site.Person, Handler: me},
			{Method: "POST", Path: "/v1/me/checkins", Auth: site.Person, Handler: myCheckIn},
		},
		// 역할 편집기(A-403)에서 이 권한을 준 관리자만 아래 화면을 본다. 최고 관리자는 늘 본다.
		Permissions: []site.Permission{{Key: "site.checkins", Name: "출퇴근 관리"}, {Key: "site.workers", Name: "인부 관리"}},
		Admin: []site.AdminPage{
			{Menu: "출퇴근", Path: "/checkins", Permission: "site.checkins", Handler: adminCheckins},
			// 메뉴 없는 상세 화면 — 목록에서 눌러 들어간다. id 는 bigserial 이다.
			{Path: "/checkins/{id}", Permission: "site.checkins", Handler: adminCheckin},
			{Menu: "인부", Path: "/workers", Permission: "site.workers", Handler: adminWorkers},
			{Path: "/workers/{id}/leave", Permission: "site.workers", Handler: adminWorkerLeave},
		},
		// 문자 인증 로그인 (D42 8절): 등록된, 그만두지 않은 인부의 번호만 로그인한다.
		Phone: site.PhoneLogin{Lookup: lookupWorker},
		SMS:   devSMS(),
	})
}

// lookupWorker is the phone login's say: the worker's id for an active worker's number. Lookup 은 요청의
// 문맥으로 불리므로 site.FromContext 로 DB 를 꺼낸다.
func lookupWorker(ctx context.Context, phone string) (string, bool, error) {
	var id int64
	err := site.FromContext(ctx).DB.QueryRowContext(ctx,
		`SELECT id FROM site_example_workers WHERE phone = $1 AND active`, phone).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return strconv.FormatInt(id, 10), true, nil
}

// devSMS writes the login code to the log instead of sending it, when EXAMPLE_SMS=log. 그 밖에는 nil 이라
// 관리자 「설정 → 문자(SMS)」의 SENS 로 보낸다.
//
// **개발용이다. 운영 실행 파일에 넣지 않는다** — 인증번호가 로그에 남으면 로그를 읽는 누구나 남의 이름으로 로그인한다.
// 사이트 저장소가 이 파일을 본뜰 때 이 함수는 빼거나 SENS 를 쓴다.
func devSMS() func(context.Context, string, string) error {
	if os.Getenv("EXAMPLE_SMS") != "log" {
		return nil
	}
	return func(_ context.Context, to, text string) error {
		log.Printf("[EXAMPLE_SMS] to=%s text=%s", to, text)
		return nil
	}
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

// me is the logged-in worker's own record: 이름과 최근 출퇴근. Subject 는 lookupWorker 가 돌려준 id 다.
func me(w http.ResponseWriter, r *http.Request) {
	c := site.From(r)
	id, _ := strconv.ParseInt(c.Subject, 10, 64) // lookupWorker 가 만든 값이다
	var name string
	if err := c.DB.QueryRowContext(r.Context(), `SELECT name FROM site_example_workers WHERE id = $1`, id).Scan(&name); err != nil {
		c.Log.Error("인부", "err", err)
		site.Error(w, http.StatusInternalServerError, "읽지 못했습니다.")
		return
	}
	rows, err := c.DB.QueryContext(r.Context(),
		`SELECT kind, created_at FROM site_example_checkins WHERE worker_id = $1 ORDER BY id DESC LIMIT 100`, id)
	if err != nil {
		site.Error(w, http.StatusInternalServerError, "읽지 못했습니다.")
		return
	}
	defer rows.Close()
	type entry struct {
		Kind string    `json:"kind"`
		At   time.Time `json:"at"`
	}
	out := []entry{}
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.Kind, &e.At); err != nil {
			site.Error(w, http.StatusInternalServerError, "읽지 못했습니다.")
			return
		}
		out = append(out, e)
	}
	site.JSON(w, http.StatusOK, map[string]any{"name": name, "checkins": out})
}

// myCheckIn records 출근(in)·퇴근(out) for the logged-in worker — 본문의 이름을 믿지 않고 Subject 를 쓴다.
func myCheckIn(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Kind string `json:"kind"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || (in.Kind != "in" && in.Kind != "out") {
		site.Error(w, http.StatusBadRequest, "kind(in 또는 out)가 필요합니다.")
		return
	}
	c := site.From(r)
	id, _ := strconv.ParseInt(c.Subject, 10, 64)
	if _, err := c.DB.ExecContext(r.Context(), `INSERT INTO site_example_checkins (worker, kind, app, worker_id)
		SELECT name, $2, $3, id FROM site_example_workers WHERE id = $1`, id, in.Kind, c.App); err != nil {
		c.Log.Error("출퇴근 기록", "err", err)
		site.Error(w, http.StatusInternalServerError, "기록하지 못했습니다.")
		return
	}
	w.WriteHeader(http.StatusCreated)
}

type worker struct {
	ID     int64
	Name   string
	Phone  string
	Active bool
}

var workersPage = template.Must(template.New("workers").Parse(`{{define "content"}}
{{with .Error}}<p class="adm-toast adm-toast--error" role="alert">{{.}}</p>{{end}}
<div class="adm-card">
  <form method="post" action="/admin/site/workers">
    <div class="adm-field-row">
      <label class="adm-field"><span>이름</span><input type="text" name="name" required maxlength="40" value="{{.Name}}"></label>
      <label class="adm-field"><span>휴대폰 번호</span><input type="tel" name="phone" required inputmode="numeric" value="{{.Phone}}"></label>
    </div>
    <div class="adm-actions adm-actions--end"><button class="adm-btn adm-btn--primary">인부 추가</button></div>
  </form>
</div>
<div class="adm-card">
  <div class="adm-table-wrap">
    <table class="adm-table">
      <thead><tr><th>이름</th><th>휴대폰</th><th>상태</th><th></th></tr></thead>
      <tbody>
      {{range .Workers}}<tr><td>{{.Name}}</td><td>{{.Phone}}</td><td>{{if .Active}}일하는 중{{else}}그만둠{{end}}</td>
        <td>{{if .Active}}<form method="post" action="/admin/site/workers/{{.ID}}/leave">
          <button class="adm-btn adm-btn--sm adm-btn--danger" onclick="return confirm('그만둔 것으로 할까요? 앱에서 바로 로그아웃됩니다.')">그만둠</button>
        </form>{{end}}</td></tr>
      {{else}}<tr><td colspan="4">인부가 없습니다.</td></tr>{{end}}
      </tbody>
    </table>
  </div>
</div>{{end}}`))

// adminWorkers lists workers (GET) and adds one (POST). 번호는 site.NormalizePhone 을 거쳐 Lookup 이 받는 모양으로 둔다.
func adminWorkers(w http.ResponseWriter, r *http.Request) {
	c := site.From(r)
	view := map[string]any{}
	status := http.StatusOK
	if r.Method == http.MethodPost {
		name := strings.TrimSpace(r.PostFormValue("name"))
		phone, ok := site.NormalizePhone(r.PostFormValue("phone"))
		view["Name"], view["Phone"] = name, r.PostFormValue("phone")
		switch {
		case name == "" || len([]rune(name)) > 40:
			view["Error"], status = "이름은 1~40자로 넣으세요.", http.StatusUnprocessableEntity
		case !ok:
			view["Error"], status = "휴대폰 번호를 넣으세요.", http.StatusUnprocessableEntity
		default:
			var id int64
			err := c.DB.QueryRowContext(r.Context(), `INSERT INTO site_example_workers (name, phone) VALUES ($1, $2)
				ON CONFLICT (phone) DO NOTHING RETURNING id`, name, phone).Scan(&id)
			if errors.Is(err, sql.ErrNoRows) {
				view["Error"], status = "이미 등록된 번호입니다.", http.StatusUnprocessableEntity
				break
			}
			if err != nil {
				c.Log.Error("인부 추가", "err", err)
				http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
				return
			}
			site.OpLog(r, "site.worker_add", strconv.FormatInt(id, 10), name+" 인부를 추가했다")
			site.Done(w, r, "/admin/site/workers", name+" 인부를 추가했습니다.")
			return
		}
	}
	rows, err := c.DB.QueryContext(r.Context(), `SELECT id, name, phone, active FROM site_example_workers ORDER BY active DESC, id DESC LIMIT 200`)
	if err != nil {
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	var list []worker
	for rows.Next() {
		var k worker
		if err := rows.Scan(&k.ID, &k.Name, &k.Phone, &k.Active); err != nil {
			http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
			return
		}
		list = append(list, k)
	}
	view["Workers"] = list
	site.RenderAdmin(w, r, status, "인부", workersPage, view)
}

// adminWorkerLeave marks a worker as gone: Lookup 이 그 번호를 더 허락하지 않고, 이미 받은 앱 토큰도 끊는다
// (site.EndSessions). 둘 다 해야 한다 — 토큰은 쓰는 동안 계속 살아 있다.
func adminWorkerLeave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "허용되지 않는 메서드입니다.", http.StatusMethodNotAllowed)
		return
	}
	c := site.From(r)
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var name string
	err = c.DB.QueryRowContext(r.Context(), `UPDATE site_example_workers SET active = false WHERE id = $1 RETURNING name`, id).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err == nil {
		err = site.EndSessions(r.Context(), strconv.FormatInt(id, 10))
	}
	if err != nil {
		c.Log.Error("인부 그만둠", "err", err)
		http.Error(w, "일시적인 오류입니다.", http.StatusInternalServerError)
		return
	}
	site.OpLog(r, "site.worker_leave", strconv.FormatInt(id, 10), name+" 인부가 그만뒀다 — 앱 로그인을 끊었다")
	site.Done(w, r, "/admin/site/workers", name+" 인부를 그만둔 것으로 했습니다. 앱에서 로그아웃됐습니다.")
}
