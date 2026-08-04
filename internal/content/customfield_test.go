package content

import (
	"errors"
	"net/url"
	"reflect"
	"testing"
)

func schema() []FieldSchema {
	return []FieldSchema{
		{Key: "memo", Label: "메모", Type: FieldText},
		{Key: "detail", Label: "상세", Type: FieldTextarea},
		{Key: "qty", Label: "수량", Type: FieldNumber},
		{Key: "color", Label: "색상", Type: FieldSelect, Options: []string{"빨강", "파랑"}},
		{Key: "agree", Label: "동의", Type: FieldCheckbox},
		{Key: "tags", Label: "태그", Type: FieldMultiselect, Options: []string{"A", "B", "C"}},
		{Key: "due", Label: "기한", Type: FieldDate},
		{Key: "site", Label: "링크", Type: FieldURL},
	}
}

// D14 3절 규칙 2. An undefined key is REFUSED, not dropped — dropping is the
// dangerous half of mass assignment, because the request still looks like it
// worked and nobody learns the form and the schema disagree.
func TestUndefinedKeyIsRefusedNotDropped(t *testing.T) {
	got, err := ValidateCustomFields(schema(), url.Values{
		"memo":     {"보통 값"},
		"is_admin": {"true"},
	}, nil)
	if !errors.Is(err, ErrUnknownField) {
		t.Fatalf("err = %v, want ErrUnknownField", err)
	}
	if got != nil {
		t.Errorf("거부했는데 값을 돌려줬다: %v", got)
	}
	var fe FieldError
	if !errors.As(err, &fe) || fe.Key != "is_admin" {
		t.Errorf("어느 키가 문제인지 말하지 않는다: %v", err)
	}
}

// 규칙 3: 타입 검증도 스키마 기준. 타입별 표 테스트.
func TestFieldTypesAreValidated(t *testing.T) {
	tests := []struct {
		name string
		form url.Values
		want any // nil 이면 거부되어야 한다
		key  string
	}{
		{"텍스트", url.Values{"memo": {" 앞뒤 공백 "}}, "앞뒤 공백", "memo"},
		{"긴 글", url.Values{"detail": {"여러\n줄"}}, "여러\n줄", "detail"},
		{"숫자", url.Values{"qty": {"12"}}, 12.0, "qty"},
		{"소수", url.Values{"qty": {"1.5"}}, 1.5, "qty"},
		{"숫자 아님", url.Values{"qty": {"열두개"}}, nil, "qty"},
		{"선택지 안", url.Values{"color": {"빨강"}}, "빨강", "color"},
		{"선택지 밖", url.Values{"color": {"초록"}}, nil, "color"},
		{"체크 켬", url.Values{"agree": {"on"}}, true, "agree"},
		{"날짜", url.Values{"due": {"2026-08-05"}}, "2026-08-05", "due"},
		{"날짜 아님", url.Values{"due": {"2026-13-99"}}, nil, "due"},
		{"슬래시 날짜", url.Values{"due": {"2026/08/05"}}, nil, "due"},
		{"http", url.Values{"site": {"https://example.com/x"}}, "https://example.com/x", "site"},
		{"호스트 없음", url.Values{"site": {"not-a-url"}}, nil, "site"},
		// href 에 그대로 들어가면 저장형 XSS 다. 템플릿은 진짜 링크와 구분하지
		// 못한다 — 여기서 막지 않으면 막을 곳이 없다.
		{"javascript:", url.Values{"site": {"javascript:alert(1)"}}, nil, "site"},
		{"data:", url.Values{"site": {"data:text/html,<script>"}}, nil, "site"},
		// 호스트가 있는 javascript: 는 Host 검사를 통과한다. `//` 뒤가 주석이
		// 되어 브라우저에서는 그대로 실행된다 — 스킴 허용목록만이 막는다.
		{"호스트 있는 javascript:", url.Values{"site": {"javascript://example.com/%0aalert(1)"}}, nil, "site"},
		{"file:", url.Values{"site": {"file://localhost/etc/passwd"}}, nil, "site"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ValidateCustomFields(schema(), tc.form, nil)
			if tc.want == nil {
				if err == nil {
					t.Errorf("거부됐어야 하는데 통과했다: %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("정상 값이 거부됐다: %v", err)
			}
			if !reflect.DeepEqual(got[tc.key], tc.want) {
				t.Errorf("%s = %#v, want %#v", tc.key, got[tc.key], tc.want)
			}
		})
	}
}

// 체크박스는 꺼져 있으면 아무것도 전송되지 않는다. "없음" 상태가 없으므로
// 항상 두 값 중 하나다.
func TestCheckboxAbsentMeansFalse(t *testing.T) {
	got, err := ValidateCustomFields(schema(), url.Values{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got["agree"] != false {
		t.Errorf("agree = %#v, want false", got["agree"])
	}
}

func TestMultiselectAcceptsManyAndRefusesUnknown(t *testing.T) {
	got, err := ValidateCustomFields(schema(), url.Values{"tags": {"A", "C"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got["tags"], []any{"A", "C"}) {
		t.Errorf("tags = %#v", got["tags"])
	}
	if _, err := ValidateCustomFields(schema(), url.Values{"tags": {"A", "Z"}}, nil); !errors.Is(err, ErrFieldOption) {
		t.Errorf("선택지 밖 값이 통과했다: %v", err)
	}
}

func TestRequiredIsEnforcedPerType(t *testing.T) {
	req := []FieldSchema{
		{Key: "memo", Type: FieldText, Required: true},
		{Key: "qty", Type: FieldNumber, Required: true},
		{Key: "tags", Type: FieldMultiselect, Options: []string{"A"}, Required: true},
	}
	// 타입별로 격리한다. 한 폼에 여러 필수 필드를 넣으면 다른 타입의 검사가
	// 대신 울어서, 정작 이 타입의 검사가 사라져도 초록이다.
	for name, tc := range map[string]struct {
		field FieldSchema
		form  url.Values
	}{
		"텍스트 누락":   {req[0], url.Values{}},
		"텍스트 공백만":  {req[0], url.Values{"memo": {"   "}}},
		"숫자 누락":    {req[1], url.Values{}},
		"다중선택 누락":  {req[2], url.Values{}},
		"다중선택 빈 값": {req[2], url.Values{"tags": {""}}},
	} {
		if _, err := ValidateCustomFields([]FieldSchema{tc.field}, tc.form, nil); !errors.Is(err, ErrFieldRequired) {
			t.Errorf("%s 이 통과했다: %v", name, err)
		}
	}
	if _, err := ValidateCustomFields(req, url.Values{
		"memo": {"x"}, "qty": {"1"}, "tags": {"A"}}, nil); err != nil {
		t.Errorf("전부 채웠는데 거부됐다: %v", err)
	}
}

// 스키마에 있는 필드를 비우면 값이 지워진다. prev 를 통째로 복사하면 "지웠는데
// 새로고침하면 돌아온다"가 되고, 그건 규칙 4(삭제된 필드 보존)가 규칙 2 를 넘어
// 살아 있는 필드까지 덮는 경우다.
func TestClearingASchemaFieldRemovesItsValue(t *testing.T) {
	prev := map[string]any{"memo": "예전 메모", "qty": 3.0}
	got, err := ValidateCustomFields(schema(), url.Values{"memo": {""}}, prev)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["memo"]; ok {
		t.Errorf("비웠는데 값이 남았다: %#v", got["memo"])
	}
	// 폼에 아예 없는 스키마 필드도 마찬가지다.
	if _, ok := got["qty"]; ok {
		t.Errorf("전송되지 않은 스키마 필드가 예전 값으로 남았다: %#v", got["qty"])
	}
}

// 선택 필드를 비우면 키 자체가 없다. null 을 넣으면 템플릿이 "값이 있다"와
// 구분하려고 매번 분기해야 한다.
func TestBlankOptionalFieldIsAbsentNotNull(t *testing.T) {
	got, err := ValidateCustomFields(schema(), url.Values{"memo": {"값"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["qty"]; ok {
		t.Errorf("빈 선택 필드가 키로 남았다: %#v", got["qty"])
	}
	if got["memo"] != "값" {
		t.Errorf("memo = %#v", got["memo"])
	}
}

// D14 3절 규칙 4: 필드 삭제는 표시만 중단하고 값은 보존한다. 실수로 지웠을 때
// 되살릴 수 있어야 한다 — 스키마에서 사라졌다고 남의 글 내용을 지우지 않는다.
func TestDeletedFieldKeepsItsStoredValue(t *testing.T) {
	prev := map[string]any{
		"memo":    "예전 메모",
		"retired": "삭제된 필드에 적혀 있던 값",
	}
	got, err := ValidateCustomFields(schema(), url.Values{"memo": {"새 메모"}}, prev)
	if err != nil {
		t.Fatal(err)
	}
	if got["retired"] != "삭제된 필드에 적혀 있던 값" {
		t.Errorf("삭제된 필드의 값이 사라졌다: %#v", got["retired"])
	}
	// ...while a field still in the schema comes from the form, not from prev.
	if got["memo"] != "새 메모" {
		t.Errorf("스키마에 있는 필드가 예전 값으로 덮였다: %#v", got["memo"])
	}
}

// 삭제된 필드의 값이 보존된다고 해서 그 키로 다시 쓸 수 있다는 뜻은 아니다.
// 그렇게 되면 규칙 2 가 규칙 4 로 우회된다.
func TestDeletedFieldCannotBeWrittenThroughTheForm(t *testing.T) {
	prev := map[string]any{"retired": "원래 값"}
	_, err := ValidateCustomFields(schema(), url.Values{"retired": {"덮어쓰기"}}, prev)
	if !errors.Is(err, ErrUnknownField) {
		t.Errorf("삭제된 필드에 폼으로 쓸 수 있다: %v", err)
	}
}

// A-306: 글의 기본 항목과 같은 이름은 커스텀 필드가 될 수 없다. 둘을 템플릿에
// 합치는 자리마다 어느 쪽이 이기는지 달라진다.
func TestReservedKeysAreRefusedWhenDefiningAField(t *testing.T) {
	for _, k := range []string{"id", "title", "body", "author_id", "board_id", "status"} {
		if err := ValidateFieldKey(k); err == nil {
			t.Errorf("예약 키 %q 가 필드 이름으로 통과했다", k)
		}
	}
	for _, k := range []string{"memo", "author_name", "titles"} {
		if err := ValidateFieldKey(k); err != nil {
			t.Errorf("평범한 키 %q 가 거부됐다: %v", k, err)
		}
	}
}
