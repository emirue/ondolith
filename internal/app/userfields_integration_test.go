package app

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// defineUserField 는 A-406 으로 회원 항목을 하나 정의한다. **SQL 로 직접 넣지
// 않는다** — 화면을 지나가야 「관리자가 정의할 수 있다」가 검사된다.
func defineUserField(t *testing.T, post func(string, url.Values) *http.Response, form url.Values) {
	t.Helper()
	resp := post("/admin/user-fields", form)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("항목 정의 = HTTP %d, want 303", resp.StatusCode)
	}
}

// bodyOf GETs a page and returns its markup.
func bodyOf(t *testing.T, c *http.Client, url string) string {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	return readAll(t, resp)
}

// **운영자가 정한 항목이 가입 화면에 나오고, 회원이 적은 값이 저장된다**
// (FR-215, A-406 → P-103 → P-109).
//
// 한 번에 본다: 정의 → 가입 폼에 나옴 → 가입할 때 저장됨 → 내 정보에 다시 보임.
// 나누면 「폼에는 나오는데 저장은 안 되는」 중간 상태가 각각 초록으로 통과한다.
func TestOperatorDefinedFieldReachesSignupAndProfile(t *testing.T) {
	srv, pool := liveSite(t)
	_, post := adminSession(t, srv, pool)

	defineUserField(t, post, url.Values{
		"key": {"nickname"}, "label": {"별명"}, "field_type": {"text"},
		"sort_order": {"0"},
	})

	// 가입 화면이 그 항목을 그린다. 코드에 필드 목록이 없다는 것이 이 단언의
	// 내용이다 — 화면은 방금 정의한 것을 읽어 왔다.
	body := bodyOf(t, client(), srv.URL+"/signup")
	if !strings.Contains(body, `name="nickname"`) {
		t.Fatalf("가입 화면에 방금 정의한 항목이 없다: %.400s", body)
	}
	if !strings.Contains(body, "별명") {
		t.Error("가입 화면에 라벨이 없다")
	}

	// 가입하면서 값을 적는다.
	c := client()
	resp, err := c.PostForm(srv.URL+"/signup", url.Values{
		"email": {"member@example.com"}, "display_name": {"회원"},
		"password": {"user-integration-passphrase"}, "nickname": {"온돌이"},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("가입 = HTTP %d, want 303", resp.StatusCode)
	}

	var stored string
	if err := pool.QueryRow(context.Background(),
		`SELECT custom_fields->>'nickname' FROM users WHERE email = 'member@example.com'`,
	).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != "온돌이" {
		t.Errorf("저장된 값 = %q, want %q", stored, "온돌이")
	}

	// 내 정보에 다시 보이고, 고치면 반영된다.
	me := bodyOf(t, c, srv.URL+"/me")
	if !strings.Contains(me, "온돌이") {
		t.Errorf("내 정보에 적어 낸 값이 없다: %.400s", me)
	}
	resp, err = c.PostForm(srv.URL+"/me", url.Values{
		"display_name": {"회원"}, "nickname": {"구들이"},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if err := pool.QueryRow(context.Background(),
		`SELECT custom_fields->>'nickname' FROM users WHERE email = 'member@example.com'`,
	).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != "구들이" {
		t.Errorf("수정 후 = %q, want %q", stored, "구들이")
	}
}

// **아무도 정의하지 않은 키는 저장되지 않는다** (D19 P-205 와 같은 규칙).
//
// 폼에서 온 것을 그대로 JSONB 에 넣으면 그것이 대량 할당이다. 거부가 아니라
// 버리는 이유는 P-205 에 적혀 있다 — 값이 어디에도 닿지 않는 것이 방어이고,
// 거부하면 폼에 무엇이 더 실렸느냐로 정상 요청이 422 가 된다.
func TestSignupDropsFieldsNobodyDefined(t *testing.T) {
	srv, pool := liveSite(t)
	// 정의된 항목이 하나 있는 상태에서 본다 — 없으면 「아무것도 저장하지
	// 않는다」와 구별되지 않는다.
	_, post := adminSession(t, srv, pool)
	defineUserField(t, post, url.Values{
		"key": {"nickname"}, "label": {"별명"}, "field_type": {"text"}, "sort_order": {"0"},
	})

	c := client()
	resp, err := c.PostForm(srv.URL+"/signup", url.Values{
		"email": {"attacker@example.com"}, "display_name": {"침입"},
		"password": {"user-integration-passphrase"},
		"nickname": {"정상값"}, "is_admin": {"true"}, "role": {"admin"},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("가입 = HTTP %d, want 303", resp.StatusCode)
	}

	var raw string
	if err := pool.QueryRow(context.Background(),
		`SELECT custom_fields::text FROM users WHERE email = 'attacker@example.com'`,
	).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	// 정의된 것은 들어갔다 — 이것이 없으면 아래 단언은 「아무것도 안 들어갔다」
	// 일 때도 통과한다.
	if !strings.Contains(raw, "정상값") {
		t.Fatalf("정의된 항목이 저장되지 않았다: %s", raw)
	}
	for _, banned := range []string{"is_admin", "role"} {
		if strings.Contains(raw, banned) {
			t.Errorf("정의하지 않은 키 %q 가 저장됐다: %s", banned, raw)
		}
	}
	// 권한도 오르지 않았다.
	var isAdmin bool
	if err := pool.QueryRow(context.Background(),
		`SELECT is_admin FROM users WHERE email = 'attacker@example.com'`).Scan(&isAdmin); err != nil {
		t.Fatal(err)
	}
	if isAdmin {
		t.Error("폼에 실린 is_admin 이 계정에 반영됐다")
	}
}

// **필수로 지정한 항목은 비우고 가입할 수 없다.**
//
// 이것이 없으면 「필수」 체크는 화면의 별표일 뿐이고, 폼을 고쳐 보내면 통과한다.
func TestRequiredFieldIsEnforcedOnTheServer(t *testing.T) {
	srv, pool := liveSite(t)
	_, post := adminSession(t, srv, pool)
	defineUserField(t, post, url.Values{
		"key": {"phone"}, "label": {"연락처"}, "field_type": {"text"},
		"is_required": {"1"}, "sort_order": {"0"},
	})

	c := client()
	resp, err := c.PostForm(srv.URL+"/signup", url.Values{
		"email": {"nophone@example.com"}, "display_name": {"회원"},
		"password": {"user-integration-passphrase"},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("필수 항목 없이 가입 = HTTP %d, want 422", resp.StatusCode)
	}

	// 헛돌기 방지: 값을 넣으면 통과해야 한다. 그렇지 않으면 위 단언은
	// 「가입 자체가 안 된다」일 수도 있다.
	resp, err = c.PostForm(srv.URL+"/signup", url.Values{
		"email": {"withphone@example.com"}, "display_name": {"회원"},
		"password": {"user-integration-passphrase"}, "phone": {"01012345678"},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("필수 항목을 채운 가입 = HTTP %d, want 303 — 위 단언이 헛돌았다",
			resp.StatusCode)
	}
}

// **항목 정의를 지워도 회원이 적어 낸 값은 남는다** (D14 3절 규칙 4).
//
// 잘못 지운 운영자가 사람들의 입력까지 잃지 않는다. 지우면 값도 지우는 쪽이
// 「깔끔」해 보이지만, 그 깔끔함은 되돌릴 수 없다.
func TestDeletingAFieldKeepsWhatMembersWrote(t *testing.T) {
	srv, pool := liveSite(t)
	_, post := adminSession(t, srv, pool)
	ctx := context.Background()

	defineUserField(t, post, url.Values{
		"key": {"team"}, "label": {"소속"}, "field_type": {"text"}, "sort_order": {"0"},
	})
	c := client()
	resp, err := c.PostForm(srv.URL+"/signup", url.Values{
		"email": {"member@example.com"}, "display_name": {"회원"},
		"password": {"user-integration-passphrase"}, "team": {"온돌팀"},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	defineUserField(t, post, url.Values{"delete": {"team"}})

	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM user_fields`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("정의가 %d 개 남았다 — 삭제가 되지 않았으면 아래 단언은 헛돈다", n)
	}
	var kept string
	if err := pool.QueryRow(ctx,
		`SELECT custom_fields->>'team' FROM users WHERE email = 'member@example.com'`,
	).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if kept != "온돌팀" {
		t.Errorf("정의를 지우자 값도 사라졌다 = %q, want %q", kept, "온돌팀")
	}
}

// **개수 제한이 없다** (FR-215). 그누보드의 여분 필드는 10개가 상한이고, 그
// 상한은 컬럼을 미리 만들어 두는 방식에서 나온다. 정의가 행이고 값이 JSONB 면
// 열한 번째를 만드는 데 마이그레이션이 필요 없다.
func TestFieldCountHasNoCeiling(t *testing.T) {
	srv, pool := liveSite(t)
	_, post := adminSession(t, srv, pool)
	const want = 25 // 그누보드 상한(10)보다 넉넉히 많게
	for i := range want {
		defineUserField(t, post, url.Values{
			"key":        {"f" + string(rune('a'+i%26)) + string(rune('0'+i/26))},
			"label":      {"항목"},
			"field_type": {"text"},
			"sort_order": {"0"},
		})
	}
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM user_fields`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != want {
		t.Errorf("항목 %d 개, want %d", n, want)
	}
	// 전부 가입 화면에 나온다 — 저장만 되고 그려지지 않으면 쓸 수 없다.
	body := bodyOf(t, client(), srv.URL+"/signup")
	for i := range want {
		key := "f" + string(rune('a'+i%26)) + string(rune('0'+i/26))
		if !strings.Contains(body, `name="`+key+`"`) {
			t.Errorf("가입 화면에 %s 가 없다", key)
		}
	}
}

// **「사용자 목록에 표시」를 켠 항목이 A-401 의 열이 된다** (FR-215).
//
// 이 배선이 없으면 그 체크박스는 아무 일도 하지 않는 스위치다 — 켜도 꺼도
// 화면이 같다. 오늘 고친 결함이 전부 그 모양이었다: 만들어 두고 잇지 않은 것.
func TestShowInListAddsAColumnToTheUserList(t *testing.T) {
	srv, pool := liveSite(t)
	c, post := adminSession(t, srv, pool)

	defineUserField(t, post, url.Values{
		"key": {"team"}, "label": {"소속"}, "field_type": {"text"},
		"show_in_list": {"1"}, "sort_order": {"0"},
	})
	// 목록에 내지 않기로 한 항목도 하나 둔다 — 없으면 「전부 나온다」와
	// 구별되지 않는다.
	defineUserField(t, post, url.Values{
		"key": {"memo"}, "label": {"비고"}, "field_type": {"text"}, "sort_order": {"1"},
	})

	member := client()
	resp, err := member.PostForm(srv.URL+"/signup", url.Values{
		"email": {"member@example.com"}, "display_name": {"회원"},
		"password": {"user-integration-passphrase"},
		"team":     {"온돌팀"}, "memo": {"숨은메모"},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	body := bodyOf(t, c, srv.URL+"/admin/users")
	if !strings.Contains(body, "소속") {
		t.Errorf("목록에 열 이름이 없다: %.600s", body)
	}
	if !strings.Contains(body, "온돌팀") {
		t.Errorf("목록에 값이 없다: %.600s", body)
	}
	if strings.Contains(body, "숨은메모") {
		t.Error("목록에 내지 않기로 한 항목의 값이 나왔다")
	}
}
