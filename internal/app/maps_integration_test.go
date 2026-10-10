package app

import (
	"context"
	"strings"
	"testing"
)

// **지도 키를 넣으면 공개 화면이 maps.js 와 키를 싣는다** (FR-716). 키가 하나도 없으면
// 스크립트도 없다 — 지도 블록은 자리표시 그림으로 남는다. 구글은 키와 Map ID 가 다
// 있어야 그린다(AdvancedMarkerElement 가 Map ID 를 요구한다, Google 문서).
func TestMapKeysReachThePublicPages(t *testing.T) {
	srv, pool := liveSite(t)
	if _, body := mustGet(t, client(), srv.URL+"/"); strings.Contains(body, "js/maps.js") {
		t.Errorf("지도 키가 없는데 maps.js 를 싣는다")
	}
	if _, err := pool.Exec(context.Background(), `INSERT INTO settings (key, value) VALUES
		('map.kakao_key', 'kakaokey123'), ('map.google_key', 'AIzaTest')`); err != nil {
		t.Fatal(err)
	}
	_, body := mustGet(t, client(), srv.URL+"/")
	for _, want := range []string{`js/maps.js`, `data-kakao="kakaokey123"`, `data-google="AIzaTest"`, `data-google-map-id=""`} {
		if !strings.Contains(body, want) {
			t.Errorf("홈에 %s 가 없다", want)
		}
	}
}
