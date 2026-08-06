package admin

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"rsc.io/qr"
)

// **QR 이 실제로 그 값을 담는지** 는 그리는 쪽에서 확인할 수 없다 — 그림만
// 보고는 알 수 없기 때문이다. 그래서 인코딩 결과를 되읽어 대조한다.
func TestQRSVGIsWellFormedAndScales(t *testing.T) {
	const id = "550e8400-e29b-41d4-a716-446655440000"
	svg, err := qrSVG(id, 128)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`<svg xmlns="http://www.w3.org/2000/svg"`,
		`viewBox="0 0 `, `width="128" height="128"`,
		`shape-rendering="crispEdges"`, `</svg>`,
	} {
		if !strings.Contains(svg, want) {
			t.Errorf("SVG 에 %q 가 없다", want)
		}
	}
	// **여백이 없으면 스캐너가 경계를 못 찾는다.** 모듈은 사양이 요구하는
	// 4칸만큼 안으로 들어와야 하므로, x·y 가 4보다 작은 사각형은 없어야 한다.
	for _, edge := range []string{
		`<rect x="0"`, `<rect x="1"`, `<rect x="2"`, `<rect x="3"`,
		` y="0"`, ` y="1"`, ` y="2"`, ` y="3"`,
	} {
		if strings.Contains(svg, edge) {
			t.Errorf("모듈이 여백 안쪽(%s)에 그려졌다 — 스캐너가 경계를 못 찾는다", edge)
		}
	}
	// 빈 값은 라벨이 될 수 없다.
	if _, err := qrSVG("", 128); err == nil {
		t.Error("빈 값으로 QR 을 만들었다")
	}
}

// **가로 연속 모듈이 하나의 사각형으로 묶인다.** 칸마다 rect 를 내면 라벨
// 시트 한 장이 수만 개 요소가 되고, 저사양 기기의 브라우저가 인쇄를 못 한다.
func TestQRSVGMergesHorizontalRuns(t *testing.T) {
	svg, err := qrSVG("550e8400-e29b-41d4-a716-446655440000", 128)
	if err != nil {
		t.Fatal(err)
	}
	rects := strings.Count(svg, "<rect")
	if !strings.Contains(svg, `height="1"/>`) {
		t.Fatal("모듈 사각형이 없다")
	}
	// 묶지 않으면 검은 모듈 수만큼 나온다 — uuid 한 개가 대략 400개 이상이다.
	if rects > 300 {
		t.Errorf("rect %d개 — 가로 연속을 묶지 않았다", rects)
	}
	if !strings.Contains(svg, `width="2"`) && !strings.Contains(svg, `width="3"`) {
		t.Error("폭이 2 이상인 사각형이 하나도 없다 — 묶기가 동작하지 않았다")
	}
}

// svgModules parses the rendered SVG back into the module grid.
//
// **되읽는 이유**: qrSVG 와 기대값을 같은 함수로 만들면 인코딩이 통째로
// 틀려도 둘이 나란히 틀려서 검사가 통과한다. 격자를 복원해 라이브러리의
// `Black` 과 직접 대조해야 "이 그림이 그 값을 담고 있다" 가 증명된다.
func svgModules(t *testing.T, svg string) map[[2]int]bool {
	t.Helper()
	const quiet = 4
	re := regexp.MustCompile(`<rect x="(\d+)" y="(\d+)" width="(\d+)" height="1"/>`)
	out := map[[2]int]bool{}
	for _, m := range re.FindAllStringSubmatch(svg, -1) {
		x, _ := strconv.Atoi(m[1])
		y, _ := strconv.Atoi(m[2])
		w, _ := strconv.Atoi(m[3])
		for i := range w {
			out[[2]int{x - quiet + i, y - quiet}] = true
		}
	}
	if len(out) == 0 {
		t.Fatal("SVG 에서 모듈을 하나도 읽지 못했다")
	}
	return out
}

// assertQRCarries 는 그림이 그 값을 담고 있음을 라이브러리와 직접 대조한다.
func assertQRCarries(t *testing.T, svg, want string) {
	t.Helper()
	code, err := qr.Encode(want, qr.M)
	if err != nil {
		t.Fatal(err)
	}
	got := svgModules(t, svg)
	for y := range code.Size {
		for x := range code.Size {
			if code.Black(x, y) != got[[2]int{x, y}] {
				t.Fatalf("(%d,%d) 모듈이 다르다 — 이 QR 은 %q 를 담고 있지 않다", x, y, want)
			}
		}
	}
	if len(got) == 0 {
		t.Fatal("모듈이 비었다")
	}
}

// 그려진 QR 이 실제로 그 문자열을 담는다. 다른 문자열이면 격자가 달라진다.
func TestQRSVGActuallyCarriesTheGivenValue(t *testing.T) {
	const id = "550e8400-e29b-41d4-a716-446655440000"
	svg, err := qrSVG(id, 128)
	if err != nil {
		t.Fatal(err)
	}
	assertQRCarries(t, svg, id)

	// 다른 값과는 다르다 — 위 대조가 "무엇이든 통과" 가 아니라는 것.
	other, err := qr.Encode("SKU-A", qr.M)
	if err != nil {
		t.Fatal(err)
	}
	code, _ := qr.Encode(id, qr.M)
	same := other.Size == code.Size
	if same {
		for y := range code.Size {
			for x := range code.Size {
				if other.Black(x, y) != code.Black(x, y) {
					same = false
					break
				}
			}
		}
	}
	if same {
		t.Fatal("서로 다른 값이 같은 격자를 냈다 — 대조가 무의미하다")
	}
}
