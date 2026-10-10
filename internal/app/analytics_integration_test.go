package app

import (
	"context"
	"strings"
	"testing"
)

// **GA4 측정 ID 를 넣으면 공개 화면에 측정 스크립트가 붙는다** (FR-714). 없으면 바깥
// 스크립트도 없다. 스니펫은 Google 문서(developers.google.com/tag-platform/gtagjs/install)
// 그대로다.
func TestAnalyticsSnippetFollowsTheSetting(t *testing.T) {
	srv, pool := liveSite(t)
	if _, body := mustGet(t, client(), srv.URL+"/"); strings.Contains(body, "googletagmanager") {
		t.Errorf("ID 가 없는데 측정 스크립트가 붙었다")
	}
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO settings (key, value) VALUES ('analytics.ga4_id', 'G-PSW1MY7HB4')`); err != nil {
		t.Fatal(err)
	}
	_, body := mustGet(t, client(), srv.URL+"/")
	for _, want := range []string{
		`<script async src="https://www.googletagmanager.com/gtag/js?id=G-PSW1MY7HB4"></script>`,
		`gtag('config', 'G-PSW1MY7HB4');`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("홈에 %s 가 없다", want)
		}
	}
}
