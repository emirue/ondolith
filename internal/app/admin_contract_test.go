package app

import (
	"io/fs"
	"path"
	"regexp"
	"strings"
	"testing"
)

// **관리자 템플릿은 값을 최상위에서 읽는다.**
//
// adminRenderer.Render 는 핸들러가 준 map 을 뷰 모델에 **병합**한다 — `.Data`
// 라는 것은 만들지 않는다. 그런데 커머스 화면 7개가 프론트 테마의 관례를 따라
// `{{with .Data}}` 로 감싸고 있었다. `with` 는 nil 이면 블록을 통째로 건너뛰므로
// **상품·주문·환불·반품·배송·카테고리 화면이 제목만 남고 비어 있었다.**
//
// 응답은 200 이고 템플릿 파싱도 성공한다. `TestEveryAdminScreenRenders` 가
// 초록이었던 이유가 그것이다 — 그 검사는 상태 코드를 볼 뿐 내용을 보지 않는다.
// 계약 위반은 렌더가 아니라 **소스에서** 잡아야 한다.
// ownH1 은 화면이 스스로 그리는 최상위 제목이다. 레이아웃 안의 것과 구별하려고
// 줄 처음에 오는 것만 본다.
var ownH1 = regexp.MustCompile(`(?m)^\s*<h1[ >]`)

func TestAdminTemplatesUseTheRendererContract(t *testing.T) {
	var withData, ownH1Names, bareTable, scanned []string

	err := fs.WalkDir(adminFS, "templates/admin", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || path.Ext(p) != ".html" {
			return err
		}
		b, err := adminFS.ReadFile(p)
		if err != nil {
			return err
		}
		name := path.Base(p)
		scanned = append(scanned, name)
		body := string(b)

		if strings.Contains(body, ".Data") {
			withData = append(withData, name)
		}
		// 레이아웃이 `<h1>{{.Title}}</h1>` 를 그린다. 화면이 또 그리면 제목이
		// 두 번 나온다 — 실제로 커머스 화면 전부가 그랬다.
		if name != "layout.html" && ownH1.MatchString(body) {
			ownH1Names = append(ownH1Names, name)
		}
		// 클래스 없는 `<table>` 은 스타일이 붙지 않는다. 관리자 CSS 는
		// `.adm-table` 에만 규칙을 두므로, 표가 브라우저 기본 모양으로
		// 그려진다 — 커머스 화면 5개가 그랬다.
		if strings.Contains(body, "<table>") {
			bareTable = append(bareTable, name)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(scanned) < 10 {
		t.Fatalf("관리자 템플릿을 %d개밖에 못 읽었다 — 검사가 헛돌았다", len(scanned))
	}
	if len(withData) > 0 {
		t.Errorf(".Data 를 읽는 템플릿 — 렌더러는 그것을 만들지 않는다. 화면이 비어 나온다: %v",
			withData)
	}
	if len(ownH1Names) > 0 {
		t.Errorf("자기 <h1> 을 그리는 템플릿 — layout.html 이 이미 그린다: %v", ownH1Names)
	}
	if len(bareTable) > 0 {
		t.Errorf("클래스 없는 <table> — .adm-table 이 아니면 스타일이 붙지 않는다: %v",
			bareTable)
	}
}
