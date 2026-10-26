package admin

import (
	"testing"

	"github.com/emirue/ondolith/internal/theme"
)

// 리뷰 #4: 폼은 선언에 맞는 저장값만 미리 채운다. 다른 테마의 글 칸이 남긴 「자세히 보기」를
// 링크 칸에 채우면, 운영자가 그 칸을 건드리지 않아도 이 화면의 모든 저장이 링크 오류로 거부된다.
func TestThemeInputsPrefillOnlyValuesThatFit(t *testing.T) {
	s := theme.Screen{Fields: []theme.Field{
		{Key: "link", Label: "링크", Type: theme.FieldURL},
		{Key: "count", Label: "수", Type: theme.FieldNumber},
		{Key: "name", Label: "이름", Type: theme.FieldText},
	}}
	in := themeInputs(s, map[string]any{"link": "자세히 보기", "count": "열", "name": "가나"}, nil, nil)
	if in[0].Value != "" || in[1].Value != "" || in[2].Value != "가나" {
		t.Errorf("미리 채운 값 = %q %q %q (맞지 않는 값은 비워야 한다)", in[0].Value, in[1].Value, in[2].Value)
	}
	if got := themeInputs(s, map[string]any{"count": 1000000.0}, nil, nil)[1].Value; got != "1000000" {
		t.Errorf("숫자 = %q", got)
	}
}
