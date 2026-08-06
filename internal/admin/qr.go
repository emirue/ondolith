package admin

import (
	"fmt"
	"strings"

	"rsc.io/qr"
)

// qrSVG renders one QR code as an SVG fragment.
//
// **SVG 다** (D50 「재고·QR」): PNG 은 해상도를 미리 정해야 하는데 스티커 인쇄
// 크기는 제각각이고, 브라우저에서 그리면 JS 없이 인쇄가 안 되는데 스캔 현장은
// 저사양 기기다. 벡터라 그 두 문제가 함께 없어진다.
//
// 라이브러리는 비트맵까지만 준다 — 사각형을 찍는 것은 여기다. 그래서 뷰박스와
// 여백을 우리가 정할 수 있고, 의존성은 인코딩 하나로 끝난다.
//
// 오류 정정 수준은 M(15%) 이다. 스티커는 긁히고 접히는데, L(7%) 은 그 손상에서
// 먼저 읽히지 않고 H(30%) 는 같은 값에 더 촘촘한 격자를 만들어 저사양 카메라가
// 못 읽는다.
func qrSVG(text string, px int) (string, error) {
	if text == "" {
		return "", fmt.Errorf("admin: QR 에 담을 값이 없습니다")
	}
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		return "", err
	}
	const quiet = 4 // 사양이 요구하는 여백. 없으면 스캐너가 경계를 못 찾는다.
	side := code.Size + quiet*2

	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" `+
		`width="%d" height="%d" shape-rendering="crispEdges" role="img">`,
		side, side, px, px)
	b.WriteString(`<rect width="100%" height="100%" fill="#fff"/>`)
	for y := range code.Size {
		// 가로로 이어진 검은 칸을 하나의 사각형으로 묶는다. 칸마다 rect 를
		// 내면 문서가 수천 개 요소가 되고, 라벨 시트 한 장에 수십 개가 들어간다.
		x := 0
		for x < code.Size {
			if !code.Black(x, y) {
				x++
				continue
			}
			run := 1
			for x+run < code.Size && code.Black(x+run, y) {
				run++
			}
			fmt.Fprintf(&b, `<rect x="%d" y="%d" width="%d" height="1"/>`,
				x+quiet, y+quiet, run)
			x += run
		}
	}
	b.WriteString(`</svg>`)
	return b.String(), nil
}
