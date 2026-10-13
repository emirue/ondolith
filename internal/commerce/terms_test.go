package commerce

import (
	"errors"
	"strings"
	"testing"
)

// terms 의 CHECK 를 DB 앞에서 거른다 — 경계에서 한 글자 차이로 갈린다. 여기서 놓친
// 값은 제약 이름으로 A-207 에 나간다.
func TestTermsAreCheckedBeforeTheDatabase(t *testing.T) {
	ok := Terms{Kind: "service", Version: "1.0", Body: "본문"}
	with := func(edit func(*Terms)) Terms { v := ok; edit(&v); return v }
	for _, tc := range []struct {
		name string
		t    Terms
		want error
	}{
		{"정상", ok, nil},
		{"빈 종류", with(func(v *Terms) { v.Kind = "" }), ErrTermsIncomplete},
		{"빈 본문", with(func(v *Terms) { v.Body = "" }), ErrTermsIncomplete},
		{"종류 50자", with(func(v *Terms) { v.Kind = strings.Repeat("a", 50) }), nil},
		{"종류 51자", with(func(v *Terms) { v.Kind = strings.Repeat("a", 51) }), ErrTermsTooLong},
		{"버전 20자", with(func(v *Terms) { v.Version = strings.Repeat("1", 20) }), nil},
		{"버전 21자", with(func(v *Terms) { v.Version = strings.Repeat("1", 21) }), ErrTermsTooLong},
		{"본문 20,000자", with(func(v *Terms) { v.Body = strings.Repeat("가", 20000) }), nil},
		{"본문 20,001자", with(func(v *Terms) { v.Body = strings.Repeat("가", 20001) }), ErrTermsTooLong},
	} {
		if err := validateTerms(tc.t); tc.want == nil && err != nil || tc.want != nil && !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
}
