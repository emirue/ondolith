package admin

import (
	"errors"
	"net/url"
	"testing"
	"time"
)

func day(y int, m time.Month, d int, loc *time.Location) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, loc)
}

// **1개월 전에 같은 날짜가 없으면 그 달의 말일이다** (D19 0.6, FR-712).
//
// time.AddDate(0, -1, 0) 는 3월 31일을 「2월 31일」 = 3월 3일로 넘긴다. 그러면
// 「1개월」 버튼이 28일치만 보여 주고, 사용자는 그 사실을 알 수 없다.
func TestMonthsBeforeClampsToTheLastDayOfTheMonth(t *testing.T) {
	for _, tc := range []struct {
		today  time.Time
		n      int
		wantY  int
		wantM  time.Month
		wantD  int
		reason string
	}{
		{day(2025, 3, 31, time.UTC), 1, 2025, 2, 28, "평년 2월"},
		{day(2024, 3, 31, time.UTC), 1, 2024, 2, 29, "윤년 2월"},
		{day(2025, 3, 30, time.UTC), 1, 2025, 2, 28, "30일도 없다"},
		{day(2025, 3, 28, time.UTC), 1, 2025, 2, 28, "있는 날짜는 그대로"},
		{day(2025, 5, 31, time.UTC), 1, 2025, 4, 30, "30일까지인 달"},
		{day(2025, 5, 31, time.UTC), 3, 2025, 2, 28, "3개월 전도 말일로"},
		{day(2025, 1, 31, time.UTC), 1, 2024, 12, 31, "해를 넘는다"},
		{day(2025, 2, 15, time.UTC), 3, 2024, 11, 15, "3개월 전이 해를 넘는다"},
		{day(2025, 12, 31, time.UTC), 3, 2025, 9, 30, "12월 31일의 3개월 전"},
	} {
		got := monthsBefore(tc.today, tc.n)
		if y, m, d := got.Date(); y != tc.wantY || m != tc.wantM || d != tc.wantD {
			t.Errorf("%s 의 %d개월 전 = %s, want %04d-%02d-%02d (%s)",
				tc.today.Format(dateLayout), tc.n, got.Format(dateLayout), tc.wantY, tc.wantM, tc.wantD, tc.reason)
		}
	}
}

// 빠른 버튼의 범위: 종료일은 오늘, 시작일은 7일 전·1개월 전·3개월 전.
// A-508 은 3개월이 없다.
func TestQuickRanges(t *testing.T) {
	today := time.Date(2025, 3, 31, 15, 4, 5, 0, time.UTC) // 시각이 붙어 있어도 날짜만 쓴다
	got := quickRanges(today, true)
	want := []period{
		{From: "2025-03-24", To: "2025-03-31"},
		{From: "2025-02-28", To: "2025-03-31"},
		{From: "2024-12-31", To: "2025-03-31"},
	}
	if len(got) != len(want) {
		t.Fatalf("버튼 %d개, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].From != want[i].From || got[i].To != want[i].To {
			t.Errorf("%d번 버튼 %s ~ %s, want %s ~ %s", i, got[i].From, got[i].To, want[i].From, want[i].To)
		}
	}
	if n := len(quickRanges(today, false)); n != 2 {
		t.Errorf("A-508 버튼 %d개, want 2 (1주일·1개월)", n)
	}
}

// **1개월 버튼은 어느 날에 눌러도 A-508 의 상한(31일) 안이다** (D19 0.6 「상한」).
// 상한을 날짜의 차이로 재므로 그렇다 — 날 수로 재면 31일짜리 달에서 넘는다.
func TestOneMonthButtonAlwaysFitsTheReconcileLimit(t *testing.T) {
	for d := day(2024, 1, 1, time.UTC); d.Year() < 2026; d = d.AddDate(0, 0, 1) {
		month := quickRanges(d, false)[1]
		p, err := parsePeriod(month.From, month.To, time.UTC)
		if err != nil {
			t.Fatalf("%s: %v", d.Format(dateLayout), err)
		}
		if p.days > reconcileMaxDays || p.days < 28 {
			t.Errorf("%s 의 1개월 버튼이 %d일 차이다 (%s ~ %s)", d.Format(dateLayout), p.days, month.From, month.To)
		}
	}
}

// **하루의 경계는 `시작일 00:00 ≤ 시각 < 종료일 다음 날 00:00` 이고, 넘긴 시간대의
// 자정이다** (D19 0.6).
//
// 시간대를 인자로 고정한 채 단언한다. time.Local 에 기대면 UTC 인 빌드 서버와
// KST 인 개발 장비에서 다른 순간을 재게 되고, 어느 쪽에서도 테스트는 초록이다.
func TestParsePeriodDayBoundariesFollowTheGivenZone(t *testing.T) {
	kst := time.FixedZone("KST", 9*60*60)
	p, err := parsePeriod("2025-03-10", "2025-03-11", kst)
	if err != nil {
		t.Fatal(err)
	}
	// KST 자정은 UTC 로 전날 15시다.
	if want := time.Date(2025, 3, 9, 15, 0, 0, 0, time.UTC); !p.Since.Equal(want) {
		t.Errorf("시작 경계 %s, want %s", p.Since.UTC(), want)
	}
	if want := time.Date(2025, 3, 11, 15, 0, 0, 0, time.UTC); !p.Until.Equal(want) {
		t.Errorf("끝 경계 %s, want %s (종료일 다음 날 00:00 KST)", p.Until.UTC(), want)
	}
	in := func(ts time.Time) bool { return !ts.Before(*p.Since) && ts.Before(*p.Until) }
	for name, tc := range map[string]struct {
		ts   time.Time
		want bool
	}{
		"시작일 00:00:00":   {time.Date(2025, 3, 10, 0, 0, 0, 0, kst), true},
		"시작일 직전":         {time.Date(2025, 3, 9, 23, 59, 59, 999999999, kst), false},
		"종료일 23:59:59":   {time.Date(2025, 3, 11, 23, 59, 59, 999999999, kst), true},
		"종료일 다음 날 00:00": {time.Date(2025, 3, 12, 0, 0, 0, 0, kst), false},
		"UTC 로는 시작일인 새벽": {time.Date(2025, 3, 9, 20, 0, 0, 0, time.UTC), true},
	} {
		if got := in(tc.ts); got != tc.want {
			t.Errorf("%s: 포함=%v, want %v", name, got, tc.want)
		}
	}
	if p.days != 1 {
		t.Errorf("종료일 − 시작일 = %d, want 1", p.days)
	}

	// 같은 날짜라도 시간대가 다르면 다른 순간이다.
	utc, _ := parsePeriod("2025-03-10", "2025-03-11", time.UTC)
	if utc.Since.Equal(*p.Since) {
		t.Error("UTC 와 KST 의 시작 경계가 같다 — 시간대 인자를 쓰지 않는다")
	}

	// 하루가 23시간인 날(서머타임 시작)에도 끝 경계는 다음 날 자정이다.
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("시간대 자료가 없다: %v", err)
	}
	dst, _ := parsePeriod("2025-03-09", "2025-03-09", ny)
	if want := time.Date(2025, 3, 10, 0, 0, 0, 0, ny); !dst.Until.Equal(want) {
		t.Errorf("서머타임 시작일의 끝 경계 %s, want %s", dst.Until, want)
	}
	if got := dst.Until.Sub(*dst.Since); got != 23*time.Hour {
		t.Errorf("그날의 길이 %s, want 23h", got)
	}
}

// 한쪽을 비우면 그쪽 끝이 열리고, 둘 다 비우면 기간 조건이 없다.
func TestParsePeriodOpenEnds(t *testing.T) {
	p, err := parsePeriod("", "", time.UTC)
	if err != nil || p.Since != nil || p.Until != nil || p.days != -1 {
		t.Errorf("둘 다 빔: %+v, %v", p, err)
	}
	p, err = parsePeriod("2025-03-10", "", time.UTC)
	if err != nil || p.Since == nil || p.Until != nil || p.days != -1 {
		t.Errorf("시작일만: %+v, %v", p, err)
	}
	p, err = parsePeriod("", "2025-03-10", time.UTC)
	if err != nil || p.Since != nil || p.Until == nil {
		t.Errorf("종료일만: %+v, %v", p, err)
	}
	// 같은 날은 하루치다.
	p, err = parsePeriod("2025-03-10", "2025-03-10", time.UTC)
	if err != nil || p.days != 0 || p.Until.Sub(*p.Since) != 24*time.Hour {
		t.Errorf("같은 날: %+v, %v", p, err)
	}
}

func TestParsePeriodRefusals(t *testing.T) {
	for _, bad := range [][2]string{
		{"2025-13-01", ""}, {"", "2025-02-30"}, {"03/10/2025", ""}, {"2025-3-1", ""},
		{"어제", ""}, {"2025-03-10T00:00", ""},
	} {
		if _, err := parsePeriod(bad[0], bad[1], time.UTC); !errors.Is(err, errPeriodFormat) {
			t.Errorf("%q ~ %q = %v, want errPeriodFormat", bad[0], bad[1], err)
		}
	}
	p, err := parsePeriod("2025-03-11", "2025-03-10", time.UTC)
	if !errors.Is(err, errPeriodOrder) {
		t.Errorf("시작일 > 종료일 = %v, want errPeriodOrder", err)
	}
	// 거부해도 받은 값은 돌려준다 — 날짜 칸을 다시 채워야 고칠 수 있다.
	if p.From != "2025-03-11" || p.To != "2025-03-10" {
		t.Errorf("거부된 기간이 받은 값을 잃었다: %+v", p)
	}
}

// 빠른 버튼은 다른 조건을 유지하고 쪽은 싣지 않는다 — 기간을 바꾸면 1쪽부터다.
func TestQuickLinksKeepOtherConditionsAndDropThePage(t *testing.T) {
	links := quickLinks("/admin/orders", url.Values{"status": {"결제완료"}},
		day(2025, 3, 31, time.UTC), true)
	if len(links) != 3 || links[0].Label != "1주일" || links[1].Label != "1개월" || links[2].Label != "3개월" {
		t.Fatalf("버튼 %+v", links)
	}
	u, err := url.Parse(links[1].URL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if u.Path != "/admin/orders" || q.Get("from") != "2025-02-28" || q.Get("to") != "2025-03-31" ||
		q.Get("status") != "결제완료" || q.Has("page") {
		t.Errorf("1개월 링크 %s", links[1].URL)
	}
}
