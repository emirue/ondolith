package app

import (
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// malformedID 는 경로 조각과 폼 값에 넣어 볼 값이다.
//
// **형식이 깨진 값과 없는 값은 다르다.** `00000000-…-000000000000` 은 UUID 로서
// 멀쩡하고 그저 존재하지 않을 뿐이라 `WHERE id = $1` 이 0행을 낸다 — 핸들러는
// 그것을 404 로 옮긴다. 반면 `not-a-uuid` 는 `uuid` 컬럼과 비교되는 순간
// PostgreSQL 이 **22P02** 로 터지고, 그 오류는 어느 도메인 오류와도 일치하지
// 않아 500 이 된다. 존재하지 않는 UUID 만으로 시험하면 이 갈래는 영영 검사되지
// 않는다.
//
// **하나만 쓴다.** `1`·`'`·`..` 도 같은 22P02 를 내므로 같은 코드 경로다.
// 여러 값을 돌리면 요청 수가 관리자 트리의 분당 상한(D15 4.3-2, 60/분)을 넘어
// 429 가 되고, **429 는 그 라우트를 검사하지 않았다는 뜻**이다. 값의 다양성은
// 화면별 테스트가 갖는다.
const malformedID = "not-a-uuid"

// sentinelID 는 **한때 관문이 통과시키던 값**이다.
//
// 만들기 화면이 `{id}` 를 함께 쓰던 동안 관문은 `new` 를 모든 `{id}` 라우트에서
// 통과시켰고, 그 값을 만들기로 다루지 않는 나머지는 그대로 `uuid` 컬럼과
// 비교하다 22P02 로 500 이 났다 — `DELETE /cart/items/new` 는 로그인 없이도
// 낼 수 있었다. 만들기가 자기 주소를 갖게 된 지금은 예외가 없지만, **되살아나면
// 여기서 운다.** 스윕이 이 값을 한 번도 넣어 보지 않아 그 구멍을 놓쳤다.
const sentinelID = "new"

// pathParam 은 `{name}` 을 찾는다. `{$}` 와 `{x...}` 는 자리표시자가 아니다.
var pathParam = regexp.MustCompile(`\{([a-zA-Z]\w*)\}`)

// shopAdminSite 는 **커머스 라우트가 실제로 떠 있는** 서버와 관리자 클라이언트를
// 준다.
//
// **재조립이 필요하다.** 커머스 라우트는 조립 시점에 정해지므로 (FR-710),
// 설정을 넣기만 하고 서버를 다시 세우지 않으면 트리에는 커머스가 없다 — 그러면
// 라우트 표에서 꺼낸 커머스 주소가 전부 404 로 돌아오고, 5xx 를 보는 검사는
// **아무것도 검사하지 않은 채 통과한다.** 실제로 이 검사의 첫 판이 그랬다.
func shopAdminSite(t *testing.T) (*httptest.Server, *pgxpool.Pool, *http.Client) {
	t.Helper()
	_, pool := liveSite(t)
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO settings (key, value) VALUES ('site.type','shop'), ('pg.provider','toss')
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`); err != nil {
		t.Fatal(err)
	}
	srv := restartOnSameSchema(t)
	c, _ := adminSession(t, srv, pool)

	// 헛돌기 방지: 커머스 라우트가 실제로 떠 있는지 하나 확인한다.
	resp, err := c.Get(srv.URL + "/admin/categories")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/admin/categories = HTTP %d — 커머스 트리가 뜨지 않았다", resp.StatusCode)
	}
	return srv, pool, c
}

// checkAnswer 는 한 응답을 판정한다.
//
// **429 도 실패다.** 관리자 트리는 분당 60건이라(D15 4.3-2) 요청이 많으면 그
// 상한에 걸리는데, 429 는 「이 라우트는 괜찮다」가 아니라 **「이 라우트를 보지
// 못했다」**이다. 실제로 이 검사의 첫 판은 429 를 통과로 세어 관리자 라우트
// 대부분을 검사하지 않은 채 초록이었다.
func checkAnswer(t *testing.T, method, pattern string, code int) {
	t.Helper()
	switch {
	case code == http.StatusTooManyRequests:
		t.Fatalf("%s %s → 429 — 요청 상한에 걸렸다. 이후 라우트가 검사되지 않으므로 "+
			"검사가 보내는 요청 수를 줄여야 한다", method, pattern)
	case code >= 500:
		t.Errorf("%s %s → HTTP %d — 잘못된 입력이 서버 오류가 됐다", method, pattern, code)
	}
}

// **경로 조각이 깨져 있어도 500 이 나오지 않는다.**
//
// 사용자가 준 문자열이 `uuid` 컬럼과 비교되면 PostgreSQL 은 22P02 로 터진다.
// 그것은 서버 고장이 아니라 잘못된 입력이므로 404 나 422 로 끝나야 한다 —
// 500 은 운영자에게 「서버가 아프다」고 말하고, 로그를 오염시키며, 경보를 울린다.
//
// 이 저장소에서 같은 부류가 세 번 나왔다: 반품 폼의 빈 `item_id`, 그것을 고친
// **바로 그 커밋에서 새로 만든** 카테고리 삭제, 그리고 장바구니의 `variant_id`.
// 한 곳을 고치는 것으로는 부족하다는 뜻이라 라우트 표 전체를 훑는다.
func TestNoRouteAnswers500ToAMalformedPathValue(t *testing.T) {
	srv, _, c := shopAdminSite(t)

	routes := buildTree(nil, nil, nil, nil, nil, nil, nil, true, noopHandler).Routes()
	if len(routes) < 100 {
		t.Fatalf("라우트를 %d 개밖에 못 찾았다 — 검사가 헛돌았다", len(routes))
	}

	checked := 0
	for _, rt := range routes {
		names := pathParam.FindAllStringSubmatch(rt.Pattern, -1)
		if len(names) == 0 {
			continue
		}
		// 본 트리 밖의 문(웹훅)은 서명으로 판정하므로 여기 대상이 아니다.
		if strings.HasPrefix(rt.Pattern, "/webhooks/") {
			continue
		}
		path := rt.Pattern
		for _, n := range names {
			path = strings.Replace(path, n[0], malformedID, 1)
		}
		checked++

		var resp *http.Response
		var err error
		if rt.Method == http.MethodGet {
			resp, err = c.Get(srv.URL + path)
		} else {
			// 폼 값은 비워 둔다. 검증에 걸려 422 가 나는 것은 옳은 답이고,
			// 여기서 보는 것은 **경로 조각**이 500 을 만들지 않는 것이다.
			resp, err = c.PostForm(srv.URL+path, url.Values{})
		}
		if err != nil {
			t.Fatalf("%s %s: %v", rt.Method, path, err)
		}
		resp.Body.Close()
		checkAnswer(t, rt.Method, rt.Pattern, resp.StatusCode)
	}
	if checked < 25 {
		t.Fatalf("경로 조각이 있는 라우트를 %d 개밖에 못 찾았다 — 검사가 헛돌았다", checked)
	}
	t.Logf("경로 조각이 있는 라우트 %d 개를 확인했다", checked)
}

// formIDFields 는 화면이 uuid 를 실어 보내는 폼 필드다.
//
// 경로 조각과 **같은 부류**다: 폼에서 온 문자열이 `uuid` 컬럼과 비교되면 역시
// 22P02 다. 경로는 라우트 관문이 막지만(guardID) 폼 값은 지나가지 못하므로
// 여기서 따로 본다. 실제로 A-509 의 `parent_id` 와 P-401 의 `variant_id` 가
// 그랬다.
// **목록은 소스에서 확인된다** — TestFormIDFieldsCoversEveryHandler 가 핸들러가
// 실제로 읽는 이름과 대조한다. 손으로 적은 첫 판은 `attachment_id`·`board_id`·
// `new_variant_id` 를 빠뜨리고 아무도 읽지 않는 `category_id`·`role_id` 를
// 적어 두었다 — 즉 그 세 필드는 이 스윕이 한 번도 건드리지 않았다.
var formIDFields = []string{
	"attachment_id", "board_id", "comment_id", "id", "item_id",
	"new_variant_id", "parent_id", "post_id", "user_id", "variant_id",
}

// endsTheSession 은 **호출하면 이후 요청이 전부 로그인 화면으로 밀리는** 라우트다.
//
// 스윕이 이것을 밟으면 그 뒤의 관리자 라우트는 303 만 돌려주고, 5xx 를 보는
// 검사는 **아무것도 검사하지 않은 채 초록**이 된다. 실제로 `/logout` 이 그랬다:
// 그 뒤에 있던 A-509 의 깨진 `parent_id` 를 이 검사가 놓쳤다.
var endsTheSession = map[string]string{
	"/logout":    "세션을 끊는다 — 이후 라우트가 전부 로그인으로 밀린다",
	"/me/delete": "계정을 비활성화한다 — 같은 이유",
}

// **폼으로 온 깨진 uuid 도 500 이 되지 않는다.**
//
// 모든 POST 라우트에 모든 후보 필드를 한꺼번에 실어 보낸다 — 그 화면이 읽지
// 않는 필드는 무시되므로 해가 없고, 읽는 화면은 반드시 걸린다.
func TestNoRouteAnswers500ToAMalformedFormID(t *testing.T) {
	srv, _, c := shopAdminSite(t)

	routes := buildTree(nil, nil, nil, nil, nil, nil, nil, true, noopHandler).Routes()
	checked := 0
	for _, rt := range routes {
		if rt.Method != http.MethodPost || strings.HasPrefix(rt.Pattern, "/webhooks/") {
			continue
		}
		if _, ends := endsTheSession[rt.Pattern]; ends {
			continue
		}
		// 경로 조각은 **멀쩡한** 값으로 채운다 — 여기서 보는 것은 폼 값이다.
		path := pathParam.ReplaceAllString(rt.Pattern, "00000000-0000-0000-0000-000000000000")
		if strings.Contains(path, "{") {
			continue
		}
		form := url.Values{}
		for _, f := range formIDFields {
			form.Set(f, malformedID)
		}
		// 이름·제목 같은 필수 값도 채워 둔다 — 그 앞에서 422 로 끝나면 uuid
		// 자리까지 가 보지 못한다.
		form.Set("name", "이름")
		form.Set("slug", "slug")
		form.Set("title", "제목")
		form.Set("quantity", "1")
		checked++

		resp, err := c.PostForm(srv.URL+path, form)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		resp.Body.Close()
		checkAnswer(t, rt.Method, rt.Pattern, resp.StatusCode)
	}
	if checked < 40 {
		t.Fatalf("POST 라우트를 %d 개밖에 못 찾았다 — 검사가 헛돌았다", checked)
	}
	// **끝까지 로그인 상태였는지 확인한다.** 도중에 세션이 끊기면 그 뒤의
	// 관리자 라우트는 303 만 돌려주고, 이 검사는 아무것도 보지 않은 채
	// 통과한다 — 실제로 `/logout` 을 밟아서 그랬다.
	resp, err := c.Get(srv.URL + "/admin/categories")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("훑고 나니 /admin/categories = HTTP %d — 도중에 세션이 끊겼다. "+
			"그 뒤의 라우트는 검사되지 않았다", resp.StatusCode)
	}
	t.Logf("POST 라우트 %d 개를 확인했다", checked)
}

// notAUUIDField 는 이름이 `_id` 로 끝나지만 **행 id 가 아닌** 폼 필드다.
//
// 이유를 함께 적는다 — 이름만 늘리면 이 목록이 「검사에서 빼고 싶은 것의
// 목록」이 되고, 그때부터 아무것도 막지 않는다.
var notAUUIDField = map[string]string{
	"client_id": "소셜 제공자가 발급한 문자열 (A-206) — 우리 행의 id 가 아니다",
}

// idFieldRe 는 핸들러가 읽는 폼 필드 중 이름이 id 로 끝나는 것을 찾는다.
var idFieldRe = regexp.MustCompile(`PostForm(?:Value)?[\(\[]"([a-z_]*id)"`)

// **위 스윕의 필드 목록이 핸들러가 실제로 읽는 것을 전부 담는다.**
//
// 목록을 손으로 적으면 빠진다. 실제로 첫 판은 `attachment_id`·`board_id`·
// `new_variant_id` 를 빠뜨렸고 아무도 읽지 않는 `category_id`·`role_id` 를
// 적어 두었다 — **그 세 필드는 스윕이 한 번도 건드리지 않았고, 검사는 그
// 사실을 말해 주지 않았다.** 새 화면이 새 `_id` 필드를 읽으면 여기서 걸린다.
func TestFormIDFieldsCoversEveryHandler(t *testing.T) {
	covered := map[string]bool{}
	for _, f := range formIDFields {
		covered[f] = true
	}

	var missing []string
	found := 0
	err := filepath.WalkDir("..", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") ||
			strings.HasSuffix(p, "_test.go") {
			return err
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, m := range idFieldRe.FindAllStringSubmatch(string(src), -1) {
			found++
			name := m[1]
			if covered[name] || notAUUIDField[name] != "" {
				continue
			}
			missing = append(missing, name+" ("+p+")")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found < 8 {
		t.Fatalf("id 로 끝나는 폼 필드를 %d 개밖에 못 찾았다 — 검사가 헛돌았다", found)
	}
	slices.Sort(missing)
	for _, m := range slices.Compact(missing) {
		t.Errorf("핸들러가 읽는데 스윕이 보내지 않는 필드: %s — formIDFields 에 넣거나, "+
			"행 id 가 아니면 notAUUIDField 에 이유와 함께 적을 것", m)
	}

	// 반대 방향도 본다: 아무도 읽지 않는 이름은 스윕을 **넓어 보이게만** 한다.
	read := map[string]bool{}
	_ = filepath.WalkDir("..", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") ||
			strings.HasSuffix(p, "_test.go") {
			return err
		}
		src, _ := os.ReadFile(p)
		for _, m := range idFieldRe.FindAllStringSubmatch(string(src), -1) {
			read[m[1]] = true
		}
		return nil
	})
	for _, f := range formIDFields {
		if !read[f] {
			t.Errorf("formIDFields 의 %q 를 읽는 핸들러가 없다 — 스윕이 넓어 보이기만 한다", f)
		}
	}
}

// pathParamKind 는 라우트가 쓸 수 있는 경로 조각 이름과 그 정체다.
//
// **닫힌 집합이다.** 경로 관문(`guardID`)은 `{id}` 라는 **이름**에만 걸리므로,
// 누군가 `{postID}` 나 `{userId}` 로 uuid 를 받으면 그 라우트는 보호받지 못하고
// 깨진 값이 22P02 로 500 이 된다. 이름을 여기 적게 해서, 새 이름이 생기면
// **관문을 늘릴지 결정하도록** 강제한다.
var pathParamKind = map[string]string{
	"id":       "행의 uuid — guardID 가 형식을 본다",
	"slug":     "사람이 읽는 주소. uuid 가 아니다 (게시판·상품·페이지)",
	"orderNo":  "주문번호. 형식이 다르고 uuid 가 아니다",
	"no":       "주문번호 (관리자 경로의 짧은 이름)",
	"returnNo": "반품·교환 번호",
	"token":    "메일로 보낸 1회용 토큰",
	"provider": "소셜 제공자 이름 (google·kakao…)",
	"path...":  "정적 자산 경로",
}

// **모든 경로 조각 이름이 선언돼 있고, uuid 는 `id` 뿐이다.**
//
// 이것이 없으면 `{postID}` 같은 이름이 조용히 생기고, 그 라우트만 관문 밖에
// 남는다 — 오늘 열두 곳을 고친 부류가 한 곳에서 되살아난다.
func TestEveryPathParamIsDeclared(t *testing.T) {
	routes := buildTree(nil, nil, nil, nil, nil, nil, nil, true, noopHandler).Routes()
	seen := map[string]bool{}
	for _, rt := range routes {
		for _, m := range regexp.MustCompile(`\{([^}$]+)\}`).FindAllStringSubmatch(rt.Pattern, -1) {
			name := m[1]
			seen[name] = true
			if pathParamKind[name] == "" {
				t.Errorf("선언되지 않은 경로 조각 {%s} (%s %s) — pathParamKind 에 정체를 적고, "+
					"uuid 라면 guardID 가 그것도 보게 할 것", name, rt.Method, rt.Pattern)
			}
		}
	}
	if len(seen) < 5 {
		t.Fatalf("경로 조각을 %d 종밖에 못 찾았다 — 검사가 헛돌았다", len(seen))
	}
	// 반대 방향: 안 쓰는 이름은 목록을 넓어 보이게만 한다.
	for name := range pathParamKind {
		if !seen[name] {
			t.Errorf("pathParamKind 의 {%s} 를 쓰는 라우트가 없다", name)
		}
	}
	// 관문이 보는 이름이 실제로 그 목록에 있는지 — 이름을 바꾸면 여기서 운다.
	if pathParamKind["id"] == "" {
		t.Error("guardID 는 {id} 를 보는데 그 이름이 선언돼 있지 않다")
	}
}

// **`{id}` 자리의 `new` 가 500 을 만들지 않는다.**
//
// 만들기 화면이 `{id}` 를 함께 쓰던 동안 경로 관문은 `new` 를 **모든** `{id}`
// 라우트에서 통과시켰다. 그 값을 만들기로 다루는 핸들러는 넷뿐이었고, 나머지는
// 그대로 `uuid` 컬럼과 비교하다 22P02 로 500 이 났다 — `DELETE /cart/items/new`
// 는 로그인 없이도 낼 수 있었다. 만들기가 자기 주소를 갖게 되어 관문에 예외가
// 없어졌지만, **예외가 되살아나면 여기서 운다.**
//
// 서버를 따로 세운다 — `{id}` 라우트가 서른 일곱이라 위 스윕과 같은 서버를
// 쓰면 관리자 트리의 분당 상한(D15 4.3-2)에 걸려 아무것도 보지 못한다.
func TestNoRouteAnswers500ToTheCreateSentinel(t *testing.T) {
	srv, _, c := shopAdminSite(t)

	routes := buildTree(nil, nil, nil, nil, nil, nil, nil, true, noopHandler).Routes()
	checked := 0
	for _, rt := range routes {
		if !strings.Contains(rt.Pattern, "{id}") ||
			strings.HasPrefix(rt.Pattern, "/webhooks/") {
			continue
		}
		path := pathParam.ReplaceAllString(rt.Pattern, sentinelID)
		if strings.Contains(path, "{") {
			continue
		}
		checked++

		var resp *http.Response
		var err error
		if rt.Method == http.MethodGet {
			resp, err = c.Get(srv.URL + path)
		} else {
			resp, err = c.PostForm(srv.URL+path, url.Values{})
		}
		if err != nil {
			t.Fatalf("%s %s: %v", rt.Method, path, err)
		}
		resp.Body.Close()
		checkAnswer(t, rt.Method, rt.Pattern, resp.StatusCode)
	}
	if checked < 20 {
		t.Fatalf("`{id}` 라우트를 %d 개밖에 못 찾았다 — 검사가 헛돌았다", checked)
	}
	t.Logf("`{id}` 라우트 %d 개를 %q 로 확인했다", checked, sentinelID)
}

// missingID 는 **형식은 멀쩡하고 존재하지 않는** 행의 id 다.
//
// `malformedID` 와 다른 갈래를 연다: 관문(`guardID`)은 이 값을 통과시키므로
// 핸들러가 실제로 돌고, 저장소는 0행을 낸다. 그 0행을 404 로 옮기지 않는
// 핸들러가 500 을 낸다 — 실제로 페이지 저장·발행·삭제와 메뉴 삭제가 그랬다.
const missingID = "00000000-0000-0000-0000-000000000000"

// **없는 행을 가리켜도 500 이 나오지 않는다.**
//
// 「그런 것은 없다」는 404 다. 500 은 운영자에게 서버가 아프다고 말하고,
// 지운 페이지의 편집 폼을 다시 제출하는 것만으로 경보가 울린다.
//
// 서버를 따로 세운다 — 관리자 트리는 분당 60건이라(D15 4.3-2) 위 스윕과 같은
// 서버를 쓰면 그 상한에 걸려 **이 검사가 아무것도 보지 못한다.**
func TestNoRouteAnswers500ToAMissingRow(t *testing.T) {
	srv, _, c := shopAdminSite(t)

	routes := buildTree(nil, nil, nil, nil, nil, nil, nil, true, noopHandler).Routes()
	checked := 0
	for _, rt := range routes {
		if !strings.Contains(rt.Pattern, "{id}") ||
			strings.HasPrefix(rt.Pattern, "/webhooks/") {
			continue
		}
		path := pathParam.ReplaceAllString(rt.Pattern, missingID)
		if strings.Contains(path, "{") {
			continue
		}
		checked++

		var resp *http.Response
		var err error
		if rt.Method == http.MethodGet {
			resp, err = c.Get(srv.URL + path)
		} else {
			// 필수 값은 채운다 — 그 앞에서 422 로 끝나면 「없는 행」까지 가 보지
			// 못한다.
			resp, err = c.PostForm(srv.URL+path, url.Values{
				"title": {"제목"}, "slug": {"slug"}, "name": {"이름"},
				"status": {"published"}, "quantity": {"1"},
			})
		}
		if err != nil {
			t.Fatalf("%s %s: %v", rt.Method, path, err)
		}
		resp.Body.Close()
		checkAnswer(t, rt.Method, rt.Pattern, resp.StatusCode)
	}
	if checked < 20 {
		t.Fatalf("`{id}` 라우트를 %d 개밖에 못 찾았다 — 검사가 헛돌았다", checked)
	}
	t.Logf("`{id}` 라우트 %d 개를 없는 행으로 확인했다", checked)
}
