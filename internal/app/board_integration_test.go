package app

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/emirue/ondolith/internal/auth"
	"github.com/emirue/ondolith/internal/content"
)

// mkBoard creates a board with the given preset, straight through the store.
func mkBoard(t *testing.T, pool *pgxpool.Pool, slug string, preset content.BoardPreset) string {
	t.Helper()
	b := content.Board{Slug: slug, Name: slug + " 게시판", AllowComments: true, PerPage: 20}
	id, err := content.NewStore(pool).CreateBoard(context.Background(), b, preset)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mkUser(t *testing.T, pool *pgxpool.Pool, email string) string {
	t.Helper()
	h, err := auth.HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	id, err := auth.NewStore(pool).CreateUser(context.Background(), email, h, email)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func login(t *testing.T, srv, email string, c *http.Client) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, srv+"/login", strings.NewReader(
		url.Values{"email": {email}, "password": {"correct horse battery"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", srv)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("로그인 HTTP %d", resp.StatusCode)
	}
}

func httpPost(t *testing.T, c *http.Client, target string, form url.Values) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	origin := target[:strings.Index(target[8:], "/")+8]
	req.Header.Set("Origin", origin)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// 공개 게시판은 익명이 읽는다. 회원전용·비공개는 404 다 — 403 이면 그 게시판이
// 있다는 사실을 알려준다 (SC-1 4항).
func TestBoardVisibilityFollowsThePreset(t *testing.T) {
	srv, pool := liveSite(t)
	mkBoard(t, pool, "free", content.PresetPublic)
	mkBoard(t, pool, "members", content.PresetMembers)
	mkBoard(t, pool, "staff", content.PresetPrivate)

	anon := client()
	for slug, want := range map[string]int{
		"free":    http.StatusOK,
		"members": http.StatusNotFound,
		"staff":   http.StatusNotFound,
		"nope":    http.StatusNotFound,
	} {
		code, _ := mustGet(t, anon, srv.URL+"/board/"+slug)
		if code != want {
			t.Errorf("익명 /board/%s → HTTP %d, want %d", slug, code, want)
		}
	}

	mkUser(t, pool, "m@example.com")
	member := client()
	login(t, srv.URL, "m@example.com", member)
	for slug, want := range map[string]int{
		"free":    http.StatusOK,
		"members": http.StatusOK,
		"staff":   http.StatusNotFound, // 회원이어도 비공개는 못 본다
	} {
		code, _ := mustGet(t, member, srv.URL+"/board/"+slug)
		if code != want {
			t.Errorf("회원 /board/%s → HTTP %d, want %d", slug, code, want)
		}
	}
}

// 글쓰기 → 목록 → 상세가 HTTP 로 돈다.
func TestWriteThenListThenView(t *testing.T) {
	srv, pool := liveSite(t)
	mkBoard(t, pool, "free", content.PresetPublic)
	mkUser(t, pool, "m@example.com")
	c := client()
	login(t, srv.URL, "m@example.com", c)

	resp := httpPost(t, c, srv.URL+"/board/free/write", url.Values{
		"title": {"첫 글"}, "body": {"본문입니다."}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("글쓰기 HTTP %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.HasPrefix(loc, "/board/free/") {
		t.Fatalf("작성 후 이동 위치 = %q", loc)
	}

	code, list := mustGet(t, c, srv.URL+"/board/free")
	if code != http.StatusOK || !strings.Contains(list, "첫 글") {
		t.Errorf("목록에 글이 없다: HTTP %d", code)
	}
	code, view := mustGet(t, c, srv.URL+loc)
	if code != http.StatusOK || !strings.Contains(view, "본문입니다.") {
		t.Errorf("상세가 안 열린다: HTTP %d", code)
	}
}

// SC-2 5항: 폼이 board_id·author_id 를 받지 않는다. 보내도 무시된다 —
// 받으면 남의 이름으로 다른 게시판에 글을 쓸 수 있다.
func TestFormIgnoresBoardAndAuthorFields(t *testing.T) {
	srv, pool := liveSite(t)
	freeID := mkBoard(t, pool, "free", content.PresetPublic)
	otherID := mkBoard(t, pool, "notice", content.PresetPublic)
	me := mkUser(t, pool, "m@example.com")
	victim := mkUser(t, pool, "v@example.com")
	c := client()
	login(t, srv.URL, "m@example.com", c)

	resp := httpPost(t, c, srv.URL+"/board/free/write", url.Values{
		"title": {"글"}, "body": {"본문"},
		"board_id": {otherID}, "author_id": {victim},
	})
	body := resp.StatusCode
	resp.Body.Close()
	// 스키마에 없는 키라 커스텀 필드 검증이 거부한다 — 조용히 버리지 않는다.
	if body != http.StatusUnprocessableEntity {
		t.Fatalf("정의되지 않은 키가 HTTP %d 로 처리됐다", body)
	}

	// 정상 작성은 경로의 게시판과 세션의 작성자로 들어간다.
	resp = httpPost(t, c, srv.URL+"/board/free/write", url.Values{"title": {"글"}, "body": {"본문"}})
	resp.Body.Close()
	var boardID, authorID string
	if err := pool.QueryRow(context.Background(),
		`SELECT board_id, author_id FROM posts`).Scan(&boardID, &authorID); err != nil {
		t.Fatal(err)
	}
	if boardID != freeID {
		t.Errorf("게시판이 폼에서 왔다: %s", boardID)
	}
	if authorID != me {
		t.Errorf("작성자가 폼에서 왔다: %s", authorID)
	}
}

// SC-3 1항: 수정은 본인 글만. 남의 글은 404 지 403 이 아니다.
func TestEditingSomebodyElsesPostIs404(t *testing.T) {
	srv, pool := liveSite(t)
	mkBoard(t, pool, "free", content.PresetPublic)
	mkUser(t, pool, "a@example.com")
	mkUser(t, pool, "b@example.com")

	author := client()
	login(t, srv.URL, "a@example.com", author)
	resp := httpPost(t, author, srv.URL+"/board/free/write", url.Values{
		"title": {"내 글"}, "body": {"본문"}})
	resp.Body.Close()
	loc := resp.Header.Get("Location")

	other := client()
	login(t, srv.URL, "b@example.com", other)
	code, _ := mustGet(t, other, srv.URL+loc+"/edit")
	if code != http.StatusNotFound {
		t.Errorf("남의 글 수정 폼이 HTTP %d — 403 도 존재를 알려준다", code)
	}
	resp = httpPost(t, other, srv.URL+loc+"/edit", url.Values{"title": {"가로채기"}, "body": {"x"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("남의 글이 HTTP %d 로 수정됐다", resp.StatusCode)
	}
	var title string
	if err := pool.QueryRow(context.Background(), `SELECT title FROM posts`).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if title != "내 글" {
		t.Errorf("제목이 바뀌었다: %q", title)
	}
}

// 폼은 스키마에서 생성된다. 필드를 추가하면 폼에 나타나고, 코드에는 필드
// 목록이 없다 (D14 3절 규칙 1).
func TestWriteFormIsGeneratedFromTheSchema(t *testing.T) {
	srv, pool := liveSite(t)
	boardID := mkBoard(t, pool, "free", content.PresetPublic)
	mkUser(t, pool, "m@example.com")
	store := content.NewStore(pool)
	if err := store.SaveBoardField(context.Background(), boardID, content.FieldSchema{
		Key: "color", Label: "색상", Type: content.FieldSelect,
		Options: []string{"빨강", "파랑"}, Required: true}); err != nil {
		t.Fatal(err)
	}

	c := client()
	login(t, srv.URL, "m@example.com", c)
	code, form := mustGet(t, c, srv.URL+"/board/free/write")
	if code != http.StatusOK {
		t.Fatalf("쓰기 폼 HTTP %d", code)
	}
	for _, want := range []string{`name="color"`, "색상", "빨강", "파랑"} {
		if !strings.Contains(form, want) {
			t.Errorf("폼에 %q 가 없다 — 스키마에서 생성되지 않았다", want)
		}
	}

	// 필수 필드를 비우면 거부되고, 채우면 저장된다.
	resp := httpPost(t, c, srv.URL+"/board/free/write", url.Values{"title": {"글"}, "body": {"본문"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("필수 커스텀 필드 없이 HTTP %d 로 저장됐다", resp.StatusCode)
	}
	resp = httpPost(t, c, srv.URL+"/board/free/write", url.Values{
		"title": {"글"}, "body": {"본문"}, "color": {"빨강"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("정상 작성이 HTTP %d", resp.StatusCode)
	}
	var v string
	if err := pool.QueryRow(context.Background(),
		`SELECT custom_fields->>'color' FROM posts`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != "빨강" {
		t.Errorf("커스텀 필드 값 = %q", v)
	}
	// 선택지 밖 값은 거부된다.
	resp = httpPost(t, c, srv.URL+"/board/free/write", url.Values{
		"title": {"글2"}, "body": {"본문"}, "color": {"초록"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("선택지 밖 값이 HTTP %d 로 통과했다", resp.StatusCode)
	}
}

// 다른 게시판의 글 id 를 공개 게시판 slug 로 열 수 없다. slug 가 권한 판정을
// 지고 있으므로, 짝이 안 맞는 쌍을 받아들이면 공개 URL 로 비공개 글을 읽는다.
func TestPostIDFromAnotherBoardIsNotServed(t *testing.T) {
	srv, pool := liveSite(t)
	mkBoard(t, pool, "free", content.PresetPublic)
	staffID := mkBoard(t, pool, "staff", content.PresetPrivate)
	store := content.NewStore(pool)
	secret, err := store.CreatePost(context.Background(), content.Post{
		BoardID: staffID, Title: "내부 문서", Body: "기밀"})
	if err != nil {
		t.Fatal(err)
	}

	code, body := mustGet(t, client(), srv.URL+"/board/free/"+secret)
	if code != http.StatusNotFound {
		t.Errorf("다른 게시판의 글이 HTTP %d 로 열렸다", code)
	}
	if strings.Contains(body, "기밀") {
		t.Error("본문이 새어 나왔다")
	}
}

// 조회수는 세션당 한 번만 는다. 새로고침으로 부풀면 숫자가 무의미해진다.
func TestViewCountCountsOncePerSession(t *testing.T) {
	srv, pool := liveSite(t)
	mkBoard(t, pool, "free", content.PresetPublic)
	store := content.NewStore(pool)
	id, err := store.CreatePost(context.Background(), content.Post{
		BoardID: mkBoardID(t, pool, "free"), Title: "글", Body: "본문"})
	if err != nil {
		t.Fatal(err)
	}

	c := client()
	for range 3 {
		if code, _ := mustGet(t, c, srv.URL+"/board/free/"+id); code != http.StatusOK {
			t.Fatalf("상세 HTTP %d", code)
		}
	}
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT view_count FROM posts WHERE id = $1`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("조회수 = %d, want 1 (같은 세션의 새로고침)", n)
	}

	// 다른 세션은 따로 센다.
	if code, _ := mustGet(t, client(), srv.URL+"/board/free/"+id); code != http.StatusOK {
		t.Fatal("다른 세션 상세 실패")
	}
	if err := pool.QueryRow(context.Background(),
		`SELECT view_count FROM posts WHERE id = $1`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("다른 세션 후 조회수 = %d, want 2", n)
	}
}

func mkBoardID(t *testing.T, pool *pgxpool.Pool, slug string) string {
	t.Helper()
	b, err := content.NewStore(pool).BoardBySlug(context.Background(), slug)
	if err != nil {
		t.Fatal(err)
	}
	return b.ID
}

// FR-512 / W2-24: 비밀글은 **목록에 제목이 나오고 본문은 404** 다.
//
// 목록에서까지 숨기면 비밀글이 존재하는 이유가 없어진다 — 자기 질문이
// 접수됐는지 보는 것이 그 기능이다. 지키는 것은 본문이다.
func TestSecretPostTitlesAreListedButBodiesAreNot(t *testing.T) {
	srv, pool := liveSite(t)
	boardID := mkBoard(t, pool, "free", content.PresetPublic)
	if _, err := pool.Exec(context.Background(),
		`UPDATE boards SET allow_secret = true WHERE id = $1`, boardID); err != nil {
		t.Fatal(err)
	}
	mkUser(t, pool, "a@example.com")
	mkUser(t, pool, "b@example.com")

	author := client()
	login(t, srv.URL, "a@example.com", author)
	resp := httpPost(t, author, srv.URL+"/board/free/write", url.Values{
		"title": {"비밀 제목"}, "body": {"비밀 본문"}, "is_secret": {"1"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("비밀글 작성 HTTP %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")

	var isSecret bool
	if err := pool.QueryRow(context.Background(),
		`SELECT is_secret FROM posts`).Scan(&isSecret); err != nil {
		t.Fatal(err)
	}
	if !isSecret {
		t.Fatal("비밀글로 저장되지 않았다")
	}

	// 제목은 누구에게나 목록에 나온다.
	for name, c := range map[string]*http.Client{"작성자": author, "익명": client()} {
		code, list := mustGet(t, c, srv.URL+"/board/free")
		if code != http.StatusOK {
			t.Fatalf("%s 목록 HTTP %d", name, code)
		}
		if !strings.Contains(list, "비밀 제목") {
			t.Errorf("%s 목록에 비밀글 제목이 없다", name)
		}
		// 목록은 본문을 그리지 않는다.
		if strings.Contains(list, "비밀 본문") {
			t.Errorf("%s 목록에 본문이 나왔다", name)
		}
	}

	// 본문은 작성자만.
	if code, body := mustGet(t, author, srv.URL+loc); code != http.StatusOK ||
		!strings.Contains(body, "비밀 본문") {
		t.Errorf("작성자가 자기 비밀글 본문을 못 본다: HTTP %d", code)
	}
	other := client()
	login(t, srv.URL, "b@example.com", other)
	if code, body := mustGet(t, other, srv.URL+loc); code != http.StatusNotFound {
		t.Errorf("남이 비밀글 본문을 HTTP %d 로 열었다", code)
		if strings.Contains(body, "비밀 본문") {
			t.Error("본문이 새어 나왔다")
		}
	}
	if code, _ := mustGet(t, client(), srv.URL+loc); code != http.StatusNotFound {
		t.Error("익명이 비밀글 본문을 열었다")
	}
}

// 게시판이 비밀글을 끄면 폼에 체크박스가 없고, 보내도 비밀글이 되지 않는다.
// 화면에서 숨기는 것은 UX 고, 서버가 거절하는 것이 규칙이다 (D15 4.3).
func TestBoardWithoutSecretPostsRefusesTheFlag(t *testing.T) {
	srv, pool := liveSite(t)
	mkBoard(t, pool, "free", content.PresetPublic) // allow_secret 기본값 false
	mkUser(t, pool, "m@example.com")
	c := client()
	login(t, srv.URL, "m@example.com", c)

	code, form := mustGet(t, c, srv.URL+"/board/free/write")
	if code != http.StatusOK {
		t.Fatalf("쓰기 폼 HTTP %d", code)
	}
	if strings.Contains(form, `name="is_secret"`) {
		t.Error("비밀글을 끈 게시판의 폼에 체크박스가 있다")
	}

	resp := httpPost(t, c, srv.URL+"/board/free/write", url.Values{
		"title": {"글"}, "body": {"본문"}, "is_secret": {"1"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("작성 HTTP %d", resp.StatusCode)
	}
	var isSecret bool
	if err := pool.QueryRow(context.Background(),
		`SELECT is_secret FROM posts`).Scan(&isSecret); err != nil {
		t.Fatal(err)
	}
	if isSecret {
		t.Error("비밀글을 끈 게시판에 비밀글이 만들어졌다")
	}
}

// FR-503 / W2-19: 관리자에서 필드를 추가하면 **코드 수정 없이** 폼과 목록에
// 나타난다. 타입별 분기는 partials/field.html 한 곳에만 있다 — 폼마다 분기하면
// 타입을 추가할 때 빠뜨린 폼이 그 필드를 조용히 텍스트로 그린다.
func TestAddingAFieldShowsUpInFormAndListWithoutCodeChanges(t *testing.T) {
	srv, pool := liveSite(t)
	boardID := mkBoard(t, pool, "free", content.PresetPublic)
	mkUser(t, pool, "m@example.com")
	store := content.NewStore(pool)
	ctx := context.Background()

	c := client()
	login(t, srv.URL, "m@example.com", c)

	// 8가지 타입을 전부 추가한다. 하나라도 분기가 빠지면 그 입력이 text 로
	// 그려지고, 아래 단언이 잡는다.
	fields := []content.FieldSchema{
		{Key: "memo", Label: "메모", Type: content.FieldText, Sort: 1},
		{Key: "detail", Label: "상세", Type: content.FieldTextarea, Sort: 2},
		{Key: "qty", Label: "수량", Type: content.FieldNumber, Sort: 3},
		{Key: "color", Label: "색상", Type: content.FieldSelect,
			Options: []string{"빨강", "파랑"}, ShowInList: true, Sort: 4},
		{Key: "agree", Label: "동의", Type: content.FieldCheckbox, Sort: 5},
		{Key: "tags", Label: "태그", Type: content.FieldMultiselect,
			Options: []string{"A", "B"}, Sort: 6},
		{Key: "due", Label: "기한", Type: content.FieldDate, Sort: 7},
		{Key: "site", Label: "링크", Type: content.FieldURL, Sort: 8},
	}
	for _, f := range fields {
		if err := store.SaveBoardField(ctx, boardID, f); err != nil {
			t.Fatalf("%s: %v", f.Key, err)
		}
	}

	_, form := mustGet(t, c, srv.URL+"/board/free/write")
	// 타입마다 다른 입력이 나와야 한다.
	wants := map[string]string{
		"memo":   `<input type="text" id="f-memo"`,
		"detail": `<textarea id="f-detail"`,
		"qty":    `<input type="number" step="any" id="f-qty"`,
		"color":  `<select id="f-color"`,
		"agree":  `<input type="checkbox" id="f-agree"`,
		"tags":   `<select id="f-tags" name="tags" multiple`,
		"due":    `<input type="date" id="f-due"`,
		"site":   `<input type="url" id="f-site"`,
	}
	for key, want := range wants {
		if !strings.Contains(form, want) {
			t.Errorf("%s 필드가 타입에 맞는 입력으로 그려지지 않았다 (%q 없음)", key, want)
		}
		if !strings.Contains(form, ">"+labelOf(fields, key)+"<") {
			t.Errorf("%s 의 라벨이 폼에 없다", key)
		}
	}

	// 값을 채워 저장하고, show_in_list 인 필드가 목록 열에 나타난다.
	resp := httpPost(t, c, srv.URL+"/board/free/write", url.Values{
		"title": {"글"}, "body": {"본문"}, "color": {"파랑"}, "memo": {"메모값"},
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("작성 HTTP %d", resp.StatusCode)
	}
	_, list := mustGet(t, c, srv.URL+"/board/free")
	if !strings.Contains(list, "<th>색상</th>") {
		t.Error("show_in_list 필드가 목록 열에 없다")
	}
	if !strings.Contains(list, "파랑") {
		t.Error("목록 열에 값이 안 나온다")
	}
	if strings.Contains(list, "<th>메모</th>") {
		t.Error("show_in_list 가 아닌 필드가 목록 열에 나왔다")
	}
}

func labelOf(fields []content.FieldSchema, key string) string {
	for _, f := range fields {
		if f.Key == key {
			return f.Label
		}
	}
	return ""
}

// 페이지 이동 링크가 실제 상태를 반영한다. "다음"이 마지막 페이지에도 있으면
// 방문자는 빈 페이지로 간다.
func TestPaginationLinksReflectWhereYouAre(t *testing.T) {
	srv, pool := liveSite(t)
	boardID := mkBoard(t, pool, "free", content.PresetPublic)
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO posts (board_id, title, body)
		SELECT $1, '글 ' || g, '본문' FROM generate_series(1, 25) g`, boardID); err != nil {
		t.Fatal(err)
	}

	// 1쪽: 이전 없음, 다음 있음.
	_, first := mustGet(t, client(), srv.URL+"/board/free?per_page=20")
	if strings.Contains(first, ">이전<") {
		t.Error("첫 페이지에 '이전'이 있다")
	}
	if !strings.Contains(first, ">다음<") {
		t.Error("첫 페이지에 '다음'이 없다")
	}
	if !strings.Contains(first, "전체 25건") {
		t.Errorf("합계가 안 나온다: %.200s", first)
	}

	// 2쪽: 이전 있음, 다음 없음 (25건 = 20 + 5).
	_, second := mustGet(t, client(), srv.URL+"/board/free?per_page=20&page=2")
	if !strings.Contains(second, ">이전<") {
		t.Error("둘째 페이지에 '이전'이 없다")
	}
	if strings.Contains(second, ">다음<") {
		t.Error("마지막 페이지에 '다음'이 있다 — 빈 페이지로 간다")
	}
}
