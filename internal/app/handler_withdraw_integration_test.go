package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/emirue/ondolith/internal/auth"
)

// login 은 이 파일의 테스트가 반복하는 준비다.
func loginAs(t *testing.T, h http.Handler, store *auth.Store, email, pw string) *http.Cookie {
	t.Helper()
	mkAccount(t, store, email, pw)
	c := sessionCookie(postForm(h, "/login",
		url.Values{"email": {email}, "password": {pw}}, nil))
	if c == nil {
		t.Fatal("로그인 실패 — 이후 단계가 익명으로 돈다")
	}
	return c
}

func userByEmail(t *testing.T, store *auth.Store, email string) *auth.User {
	t.Helper()
	u, _, err := store.FindActiveUserByEmail(context.Background(), email)
	if err != nil {
		t.Fatalf("%s: %v", email, err)
	}
	return u
}

// P-110 은 **비활성화지 삭제가 아니다** (FR-212, D19 P-110 「성공 후」).
//
// 계정 행이 사라지면 그 사람의 주문도 주인을 잃고, 그때부터 정산과 분쟁 대응이
// 불가능해진다. 화면 문구가 「삭제된다」였을 때 서버는 실제로 삭제하지 않았다 —
// 화면과 서버가 다른 말을 하고 있었고, 라우트가 아예 없어서 둘 다 안 돌았다.
func TestWithdrawDeactivatesButKeepsTheRow(t *testing.T) {
	_, h, store, _ := accountFixture(t, false)
	c := loginAs(t, h, store, "a@example.com", "correct-horse-battery")
	id := userByEmail(t, store, "a@example.com").ID

	rec := postForm(h, "/me/delete", url.Values{
		"password": {"correct-horse-battery"}, "confirm": {"1"},
	}, []*http.Cookie{c})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("HTTP %d — 탈퇴가 처리되지 않았다: %s", rec.Code, rec.Body.String())
	}

	// 행은 남아 있고, 비활성이다.
	ctx := context.Background()
	u, err := store.FindUserByID(ctx, id)
	if err != nil {
		t.Fatalf("계정 행이 사라졌다 — 비활성화여야 한다: %v", err)
	}
	if u.IsActive {
		t.Error("탈퇴했는데 계정이 활성이다")
	}
	// 세션은 끝났다.
	if a := actorOf(t, h, c); a {
		t.Error("탈퇴 뒤에도 세션이 살아 있다")
	}
}

// actorOf 는 그 쿠키로 인증된 화면이 열리는지 본다.
func actorOf(t *testing.T, h http.Handler, c *http.Cookie) bool {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, "/me", nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code == http.StatusOK
}

// 되돌릴 수 없는 작업이므로 확인란과 비밀번호를 **둘 다** 요구한다
// (D19 P-110 입력 필드). 하나만 걸면 나머지 하나는 있으나 마나다.
func TestWithdrawNeedsBothConfirmAndPassword(t *testing.T) {
	for _, tc := range []struct {
		name string
		form url.Values
	}{
		{"확인란 없음", url.Values{"password": {"correct-horse-battery"}}},
		{"비밀번호 틀림", url.Values{"password": {"틀린 비밀번호"}, "confirm": {"1"}}},
		{"비밀번호 없음", url.Values{"confirm": {"1"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, h, store, _ := accountFixture(t, false)
			c := loginAs(t, h, store, "a@example.com", "correct-horse-battery")

			rec := postForm(h, "/me/delete", tc.form, []*http.Cookie{c})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("HTTP %d — 거부되지 않았다", rec.Code)
			}
			if !userByEmail(t, store, "a@example.com").IsActive {
				t.Error("거부됐는데 계정이 비활성이 됐다")
			}
		})
	}
}

// 마지막 superuser 는 스스로 탈퇴할 수 없다 (D15 5.2). 나가면 아무도 남을
// 다시 들여보낼 수 없고, 되돌릴 화면은 로그인 뒤에 있다.
func TestLastSuperuserCannotWithdraw(t *testing.T) {
	_, h, store, _ := accountFixture(t, false)
	c := loginAs(t, h, store, "root@example.com", "correct-horse-battery")
	id := userByEmail(t, store, "root@example.com").ID
	grantSuperuser(t, store, id)

	rec := postForm(h, "/me/delete", url.Values{
		"password": {"correct-horse-battery"}, "confirm": {"1"},
	}, []*http.Cookie{c})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("HTTP %d — 마지막 관리자가 탈퇴했다", rec.Code)
	}
	if !userByEmail(t, store, "root@example.com").IsActive {
		t.Error("거부 응답인데 계정은 비활성이 됐다 — 거부가 늦었다")
	}
}

// **P-113 은 수신 주소를 입력으로 받지 않는다** (D19 P-113 「받지 않는 필드」).
// 받으면 우리 도메인에서 임의 주소로 메일을 쏘는 릴레이가 된다.
func TestResendIgnoresAttackerSuppliedRecipient(t *testing.T) {
	_, h, store, sender := accountFixture(t, true)
	c := loginAs(t, h, store, "a@example.com", "correct-horse-battery")
	other := "victim@elsewhere.example"

	rec := postForm(h, "/verify/resend", url.Values{
		"email": {other}, "user_id": {"00000000-0000-0000-0000-000000000000"},
	}, []*http.Cookie{c})
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", rec.Code, rec.Body.String())
	}

	// **먼저 실제로 발송이 일어났는지 본다.** 아무것도 안 나가는 상태에서는
	// 아래 "공격자 주소로 안 갔다" 가 언제나 참이라 아무것도 검사하지 않는다.
	if _, ok := waitMail(t, sender, func(m capturedMail) bool {
		return m.to == "a@example.com"
	}); !ok {
		t.Fatal("메일이 한 통도 나가지 않았다 — 재발송이 동작하지 않으면 이 검사는 의미가 없다")
	}
	for _, m := range sender.snapshot() {
		if m.to != "a@example.com" {
			t.Errorf("세션 사용자가 아닌 %q 로 보냈다 (%s) — 스팸 릴레이다", m.to, other)
		}
	}
}

// 계정당 3회/시간 (D15 4.3-2). 계정 키다 — 막으려는 것은 한 수신함에 쌓이는
// 메일이고, IP 로 세면 같은 계정을 여러 IP 에서 두드릴 수 있다.
func TestResendIsRateLimitedPerAccount(t *testing.T) {
	d, h, store, sender := accountFixture(t, true)
	c := loginAs(t, h, store, "a@example.com", "correct-horse-battery")
	limit := d.limits.VerifyMailAccount

	var last int
	for range limit.Burst + 1 {
		last = postForm(h, "/verify/resend", url.Values{}, []*http.Cookie{c}).Code
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("%d회 연속 요청의 마지막이 HTTP %d — 상한이 없다", limit.Burst+1, last)
	}
	// 상한을 넘긴 요청은 **메일도 만들지 않아야** 한다. 429 만 돌려주고 발송은
	// 그대로면 폭탄은 그대로 나간다.
	if n := len(sender.snapshot()); n > limit.Burst {
		t.Errorf("메일 %d통 — 상한 %d 를 넘겨 발송됐다", n, limit.Burst)
	}
}

// 이미 인증된 계정에도 **성공과 같은 응답**을 준다. 다르게 답하면 이 화면이
// 「이 계정이 인증됐는지」를 알려주는 조회 도구가 된다.
func TestResendToVerifiedAccountLooksIdenticalButSendsNothing(t *testing.T) {
	_, h, store, sender := accountFixture(t, true)
	c := loginAs(t, h, store, "a@example.com", "correct-horse-battery")
	ctx := context.Background()
	if err := store.MarkEmailVerified(ctx, userByEmail(t, store, "a@example.com").ID); err != nil {
		t.Fatal(err)
	}

	rec := postForm(h, "/verify/resend", url.Values{}, []*http.Cookie{c})
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP %d — 인증된 계정에만 다른 답을 준다", rec.Code)
	}
	if n := len(sender.snapshot()); n != 0 {
		t.Errorf("이미 인증된 계정에 메일 %d통을 보냈다", n)
	}
}

// GET 으로는 발송되지 않는다. 되면 링크 프리페치와 크롤러가 메일을 쏜다.
func TestResendIsPostOnly(t *testing.T) {
	_, h, store, sender := accountFixture(t, true)
	c := loginAs(t, h, store, "a@example.com", "correct-horse-battery")

	req, _ := http.NewRequest(http.MethodGet, "/verify/resend", nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if n := len(sender.snapshot()); n != 0 {
		t.Errorf("GET 으로 메일 %d통이 나갔다 (HTTP %d)", n, rec.Code)
	}
	if strings.Contains(rec.Body.String(), "signup-sent") {
		t.Error("GET 이 재발송 화면을 그렸다 — 라우트가 GET 을 받고 있다")
	}
}

// grantSuperuser 는 시드된 superuser 역할을 붙인다. 이 파일에서만 쓰므로
// 여기 둔다 — 나중에 두 번째 호출자가 생기면 그때 공용으로 옮긴다.
func grantSuperuser(t *testing.T, store *auth.Store, userID string) {
	t.Helper()
	if err := store.AssignRole(context.Background(), userID, "admin"); err != nil {
		t.Fatal(err)
	}
}
