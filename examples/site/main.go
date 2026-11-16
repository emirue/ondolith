// Command site is the smallest site binary built on the core ([D42](../../docs/42-site-extension.md)):
// a site_ table of its own and an app API under /api/site/v1/checkins.
//
// 사이트 저장소는 이것을 본떠 자기 go.mod 와 main.go 를 만든다. 코어 저장소 안에 두는 이유는 하나다 — 공개 패키지
// site 를 바꿔 이 예제가 깨지면 `make check`(빌드)와 `make e2e`(scripts/site-e2e.sh 한 바퀴)가 먼저 안다.
package main

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
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
