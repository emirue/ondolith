package admin

import (
	"errors"
	"net/url"
	"time"
)

// 기간 조회 — 목록 화면 공통 (D19 0.6, FR-712). A-504·A-508·A-601·A-603 이 쓴다.
//
// 날짜 계산은 전부 여기의 순수 함수다. 「오늘」과 시간대를 인자로 받는다 — 안에서
// time.Now·time.Local 을 읽으면 빌드 환경의 시각·시간대에 기대게 되고, 그런
// 테스트는 물지 않는다 (M4).

const dateLayout = "2006-01-02"

var (
	errPeriodFormat = errors.New("날짜 형식이 올바르지 않습니다 (YYYY-MM-DD)")
	errPeriodOrder  = errors.New("시작일이 종료일보다 늦습니다")
	errPeriodSpan   = errors.New("결제 목록은 최대 31일까지 조회합니다")
)

// period 는 from·to 한 쌍이다. 둘 다 그날을 포함한다.
type period struct {
	// From·To 는 받은 그대로다 (`YYYY-MM-DD` 또는 빈 값). 날짜 칸을 다시 채우고
	// 링크에 싣는 값이다.
	From, To string
	// Since·Until 은 질의에 넘길 경계다: `Since ≤ 시각 < Until`. nil 이면 그쪽
	// 끝이 열려 있다.
	Since, Until *time.Time
	// days 는 종료일 − 시작일이다. 한쪽이라도 비면 -1.
	days int
}

// parsePeriod reads from·to in loc.
//
// **하루의 경계는 loc 의 자정이다**: `시작일 00:00 ≤ 시각 < 종료일 다음 날 00:00`.
// 종료일에 24시간을 더하지 않고 날짜를 하루 올린다 — 서머타임이 있는 시간대에서는
// 하루가 23시간이거나 25시간이다.
func parsePeriod(from, to string, loc *time.Location) (period, error) {
	p := period{From: from, To: to, days: -1}
	var f, t time.Time
	var err error
	if from != "" {
		if f, err = time.ParseInLocation(dateLayout, from, loc); err != nil {
			return p, errPeriodFormat
		}
		p.Since = &f
	}
	if to != "" {
		if t, err = time.ParseInLocation(dateLayout, to, loc); err != nil {
			return p, errPeriodFormat
		}
		next := time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, loc)
		p.Until = &next
	}
	if from != "" && to != "" {
		if f.After(t) {
			return p, errPeriodOrder
		}
		// 달력 날짜의 차이다. 시각의 차이를 24시간으로 나누면 서머타임 경계에서
		// 하루가 어긋난다.
		p.days = int(time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC).Sub(
			time.Date(f.Year(), f.Month(), f.Day(), 0, 0, 0, 0, time.UTC)).Hours() / 24)
	}
	return p, nil
}

// monthsBefore 는 day 의 n 개월 전 날짜다. **같은 날짜가 없으면 그 달의 말일이다**
// (3월 31일의 1개월 전 = 2월 28일, 윤년이면 29일).
//
// time.AddDate(0, -1, 0) 는 넘치는 날짜를 다음 달로 넘긴다 — 3월 31일에서 한 달을
// 빼면 「2월 31일」이 되어 3월 3일이 나온다.
func monthsBefore(day time.Time, n int) time.Time {
	y, m, d := day.Date()
	// 그 달의 0일 = 전 달의 말일.
	if last := time.Date(y, m-time.Month(n)+1, 0, 0, 0, 0, 0, day.Location()).Day(); d > last {
		d = last
	}
	return time.Date(y, m-time.Month(n), d, 0, 0, 0, 0, day.Location())
}

// quickLink 는 빠른 버튼 하나다 — 서버가 날짜를 계산해 둔 GET 링크라 JS 가 필요 없다.
type quickLink struct {
	Label string
	URL   string
}

// quickRanges 는 빠른 버튼의 범위다. 종료일은 today, 시작일은 7일 전·1개월 전·
// (quarter 면) 3개월 전이다. A-508 은 상한이 31일이라 3개월이 없다.
func quickRanges(today time.Time, quarter bool) []period {
	to := today.Format(dateLayout)
	out := []period{
		{From: today.AddDate(0, 0, -7).Format(dateLayout), To: to},
		{From: monthsBefore(today, 1).Format(dateLayout), To: to},
	}
	if quarter {
		out = append(out, period{From: monthsBefore(today, 3).Format(dateLayout), To: to})
	}
	return out
}

// quickLinks 는 그 범위를 path 의 링크로 만든다. keep 은 유지할 다른 조건
// (예: A-504 의 상태)이다. **쪽은 싣지 않는다** — 기간을 바꾸면 1쪽부터다.
func quickLinks(path string, keep url.Values, today time.Time, quarter bool) []quickLink {
	labels := []string{"1주일", "1개월", "3개월"}
	var out []quickLink
	for i, r := range quickRanges(today, quarter) {
		out = append(out, quickLink{Label: labels[i], URL: listURL(path, withPeriod(keep, r), 1)})
	}
	return out
}

// withPeriod 는 조건에 from·to 를 얹는다. 쪽 이동 링크도 이것으로 만든다.
func withPeriod(keep url.Values, p period) url.Values {
	v := url.Values{"from": {p.From}, "to": {p.To}}
	for k, vals := range keep {
		v[k] = vals
	}
	return v
}
