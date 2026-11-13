package content

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	stddraw "image/draw"
	"image/jpeg"
	"image/png"
	"os"
	"runtime"
	"testing"
)

// halves is a w×h picture whose left half is red and right half blue — 회전을
// 픽셀로 확인하기 위한 그림이다.
func halves(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.RGBA{220, 20, 20, 255}
			if x >= w/2 {
				c = color.RGBA{20, 20, 220, 255}
			}
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

func encodeJPEG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// withExif puts an APP1 Exif segment right after SOI: orientation tag 0x0112 and
// an extra ASCII entry carrying marker — 위치정보 대신 넣는 표식이다.
func withExif(jpg []byte, orientation uint16, marker string) []byte {
	var tiff bytes.Buffer
	le := binary.LittleEndian
	tiff.WriteString("II")
	binary.Write(&tiff, le, uint16(42))
	binary.Write(&tiff, le, uint32(8)) // IFD0 at offset 8
	binary.Write(&tiff, le, uint16(2)) // two entries
	// 0x0112 Orientation, SHORT, count 1, value inline
	binary.Write(&tiff, le, uint16(0x0112))
	binary.Write(&tiff, le, uint16(3))
	binary.Write(&tiff, le, uint32(1))
	binary.Write(&tiff, le, orientation)
	binary.Write(&tiff, le, uint16(0))
	// 0x010E ImageDescription, ASCII, points past the IFD
	payload := []byte(marker + "\x00")
	binary.Write(&tiff, le, uint16(0x010E))
	binary.Write(&tiff, le, uint16(2))
	binary.Write(&tiff, le, uint32(len(payload)))
	binary.Write(&tiff, le, uint32(8+2+2*12+4))
	binary.Write(&tiff, le, uint32(0)) // next IFD
	tiff.Write(payload)

	seg := append([]byte("Exif\x00\x00"), tiff.Bytes()...)
	var out bytes.Buffer
	out.Write(jpg[:2]) // SOI
	out.Write([]byte{0xFF, 0xE1})
	binary.Write(&out, binary.BigEndian, uint16(len(seg)+2))
	out.Write(seg)
	out.Write(jpg[2:])
	return out.Bytes()
}

// **큰 휴대폰 사진은 긴 변 1920px 로 줄고 480px 썸네일이 생긴다** (FR-514).
func TestProcessImageShrinksAndMakesThumbnail(t *testing.T) {
	out, err := ProcessImage(encodeJPEG(t, halves(4000, 3000)), "image/jpeg")
	if err != nil {
		t.Fatal(err)
	}
	if out.Width != 1920 || out.Height != 1440 || out.MIME != "image/jpeg" {
		t.Errorf("본문 사진 %dx%d %s, want 1920x1440 image/jpeg", out.Width, out.Height, out.MIME)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(out.Thumb))
	if err != nil || cfg.Width != 480 || cfg.Height != 360 {
		t.Errorf("썸네일 %dx%d (%v), want 480x360", cfg.Width, cfg.Height, err)
	}
	// 작은 사진은 키우지 않는다.
	small, err := ProcessImage(encodeJPEG(t, halves(300, 200)), "image/jpeg")
	if err != nil || small.Width != 300 || small.Height != 200 {
		t.Errorf("작은 사진 %dx%d (%v), want 300x200 그대로", small.Width, small.Height, err)
	}
}

// **휴대폰 사진의 회전 태그를 반영하고, 다시 인코딩하며 EXIF 를 버린다** (FR-514).
//
// 회전 태그(6 = 시계 방향 90°)를 무시하고 다시 인코딩하면 세로 사진이 눕는다 —
// 브라우저는 원본의 태그로 바로 세워 보여 주던 것을 우리 파일에서는 볼 수 없다.
// 그리고 EXIF 에는 위치정보가 들어 있다: 시공 현장·집 주소가 사진과 함께 나간다.
func TestProcessImageAppliesOrientationAndDropsExif(t *testing.T) {
	src := withExif(encodeJPEG(t, halves(400, 200)), 6, "GPS-SECRET-37.5665N")
	out, err := ProcessImage(src, "image/jpeg")
	if err != nil {
		t.Fatal(err)
	}
	if out.Width != 200 || out.Height != 400 {
		t.Fatalf("회전 뒤 %dx%d, want 200x400 (세로)", out.Width, out.Height)
	}
	img, err := jpeg.Decode(bytes.NewReader(out.Display))
	if err != nil {
		t.Fatal(err)
	}
	// 시계 방향 90°: 원래의 왼쪽(빨강)이 위로 간다.
	if r, _, b, _ := img.At(100, 20).RGBA(); r < b {
		t.Errorf("위쪽이 빨강이 아니다 (r=%d b=%d) — 회전 방향이 틀렸다", r>>8, b>>8)
	}
	if r, _, b, _ := img.At(100, 380).RGBA(); b < r {
		t.Errorf("아래쪽이 파랑이 아니다 (r=%d b=%d)", r>>8, b>>8)
	}
	for _, f := range [][]byte{out.Display, out.Thumb} {
		if bytes.Contains(f, []byte("GPS-SECRET")) || bytes.Contains(f, []byte("Exif\x00")) {
			t.Error("EXIF 가 결과에 남았다")
		}
	}
}

// 투명한 PNG 는 PNG 로 남고(투명도), webp 는 JPEG 로 다시 인코딩된다 — 표준
// 라이브러리와 x/image 에 webp 인코더가 없다.
func TestProcessImageKeepsPNGAndConvertsWebP(t *testing.T) {
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewNRGBA(image.Rect(0, 0, 64, 48))); err != nil {
		t.Fatal(err)
	}
	out, err := ProcessImage(b.Bytes(), "image/png")
	if err != nil || out.MIME != "image/png" {
		t.Errorf("PNG → %s (%v), want image/png", out.MIME, err)
	}
	webp, err := os.ReadFile("testdata/blue-purple-pink.lossy.webp")
	if err != nil {
		t.Fatal(err)
	}
	out, err = ProcessImage(webp, "image/webp")
	if err != nil || out.MIME != "image/jpeg" {
		t.Errorf("webp → %s (%v), want image/jpeg", out.MIME, err)
	}
}

// **픽셀이 너무 많은 그림은 디코드하기 전에 거부한다** (FR-514). 머리말 몇 바이트로
// 30000×30000 을 주장하는 파일은 펼치면 3.6GB 다 — 1GB 서버가 거기서 죽는다.
func TestProcessImageRefusesHugeDimensionsBeforeDecoding(t *testing.T) {
	var b bytes.Buffer
	b.Write([]byte("\x89PNG\r\n\x1a\n"))
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], 30000)
	binary.BigEndian.PutUint32(ihdr[4:], 30000)
	ihdr[8], ihdr[9] = 8, 6 // 8bit RGBA
	binary.Write(&b, binary.BigEndian, uint32(13))
	b.WriteString("IHDR")
	b.Write(ihdr)
	binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(append([]byte("IHDR"), ihdr...)))
	if _, err := ProcessImage(b.Bytes(), "image/png"); !errors.Is(err, ErrImageTooLarge) {
		t.Errorf("= %v, want ErrImageTooLarge", err)
	}
}

// 회전 태그 읽기는 신뢰할 수 없는 바이트를 다룬다 — 잘린 입력 어디서도 패닉하지 않고
// 「회전 없음」(1)으로 물러선다.
func TestJPEGOrientationSurvivesTruncation(t *testing.T) {
	src := withExif(encodeJPEG(t, halves(16, 8)), 8, "x")
	if got := jpegOrientation(src); got != 8 {
		t.Fatalf("온전한 입력 = %d, want 8", got)
	}
	for n := 0; n < 200 && n < len(src); n++ {
		if got := jpegOrientation(src[:n]); got < 1 || got > 8 {
			t.Fatalf("잘린 입력(%d바이트) = %d, want 1..8", n, got)
		}
	}
}

// **사진 한 장을 줄이는 데 드는 메모리는 원본 한 벌과 결과 몇 벌이다** (FR-514, NFR-101).
//
// x/image/draw 의 커널 축소는 「결과 폭 × 원본 높이」 의 [4]float64 임시 버퍼를 잡았다 — 4000×3000
// 휴대폰 사진 한 장에 250MB 를 할당했고, 512MB 서버가 사진 저장 중에 죽었다 (2026-11-14 운영). 그 방식으로
// 되돌리면 이 그림(2400×1600)에서만 1920×1600×32 ≈ 98MB 가 더 들어 아래 상한을 넘는다.
func TestProcessImageMemoryIsBoundedByTheTarget(t *testing.T) {
	const w, h = 2400, 1600
	data := encodeJPEG(t, halves(w, h))
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	p, err := ProcessImage(data, "image/jpeg")
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	// 원본을 펼친 한 벌(YCbCr 는 화소당 3바이트 이하) + 결과 크기 RGBA 네 벌(줄인 것·회전·JPEG 바탕·여유) + 8MB.
	limit := uint64(w*h*3 + 4*p.Width*p.Height*4 + 8<<20)
	if got := after.TotalAlloc - before.TotalAlloc; got > limit {
		t.Errorf("사진 한 장에 %.1fMB 를 할당했다 — 상한 %.1fMB", float64(got)/1e6, float64(limit)/1e6)
	}
}

// 면적 평균: 결과 화소 하나는 그것이 덮는 원본 화소들의 넓이 가중 평균이다.
func TestShrinkAveragesTheCoveredArea(t *testing.T) {
	px := func(img *image.RGBA, x, y int) [4]uint8 {
		i := img.PixOffset(x, y)
		return [4]uint8(img.Pix[i : i+4])
	}

	// 단색은 그대로다.
	solid := image.NewRGBA(image.Rect(0, 0, 7, 5))
	for i := range solid.Pix {
		solid.Pix[i] = []uint8{10, 200, 30, 255}[i%4]
	}
	got := image.NewRGBA(image.Rect(0, 0, 3, 2))
	shrink(got, solid)
	for y := 0; y < 2; y++ {
		for x := 0; x < 3; x++ {
			if c := px(got, x, y); c != [4]uint8{10, 200, 30, 255} {
				t.Errorf("단색 (%d,%d) = %v", x, y, c)
			}
		}
	}

	// 4×4 바둑판(검정·흰색)을 2×2 로 — 칸마다 2×2 블록의 평균 127.5 → 128.
	checker := image.NewGray(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			if (x+y)%2 == 0 {
				checker.SetGray(x, y, color.Gray{255})
			}
		}
	}
	got = image.NewRGBA(image.Rect(0, 0, 2, 2))
	shrink(got, checker)
	if c := px(got, 1, 1); c != [4]uint8{128, 128, 128, 255} {
		t.Errorf("바둑판 = %v, want 128 회색", c)
	}

	// 정수배가 아닌 비율: 폭 3 → 2. 결과 0 은 원본 0 전부와 1 의 절반, 결과 1 은 1 의 절반과 2 전부를 덮는다.
	// 값 0·90·180 → (0 + 45)/1.5 = 30, (45 + 180)/1.5 = 150.
	ramp := image.NewGray(image.Rect(0, 0, 3, 1))
	ramp.Pix = []uint8{0, 90, 180}
	got = image.NewRGBA(image.Rect(0, 0, 2, 1))
	shrink(got, ramp)
	if a, b := px(got, 0, 0)[0], px(got, 1, 0)[0]; a != 30 || b != 150 {
		t.Errorf("3→2 = %d, %d, want 30, 150", a, b)
	}

	// 투명도는 미리 곱한 값으로 평균한다: 불투명한 빨강과 완전 투명이 반씩이면 반투명한 빨강이지
	// 어두운 빨강이 아니다 (RGBA 는 미리 곱한 값이라 128,0,0,128 = 펼치면 255,0,0).
	edge := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	edge.SetNRGBA(0, 0, color.NRGBA{255, 0, 0, 255})
	edge.SetNRGBA(1, 0, color.NRGBA{0, 0, 0, 0})
	got = image.NewRGBA(image.Rect(0, 0, 1, 1))
	shrink(got, edge)
	if c := px(got, 0, 0); c != [4]uint8{128, 0, 0, 128} {
		t.Errorf("반투명 가장자리 = %v, want [128 0 0 128]", c)
	}
}

// opaque hides the concrete type so shrink takes its generic At() path.
type opaque struct{ image.Image }

// JPEG(YCbCr)·PNG(NRGBA)·RGBA 는 화소 배열을 직접 읽는다. 그 빠른 길이 At() 로 읽는 길과 같은
// 결과를 내야 한다 — 다르면 사진 형식에 따라 색이 달라진다. 원점이 0 이 아닌 그림(하위 영역)도 본다.
func TestShrinkFastPathsMatchTheGenericPath(t *testing.T) {
	const w, h = 37, 23
	nrgba := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			nrgba.SetNRGBA(x, y, color.NRGBA{uint8(x * 7), uint8(y * 11), uint8(x * y), uint8(128 + x*3)})
		}
	}
	rgba := image.NewRGBA(nrgba.Bounds())
	stddraw.Draw(rgba, rgba.Bounds(), nrgba, image.Point{}, stddraw.Src)
	ycc := image.NewYCbCr(nrgba.Bounds(), image.YCbCrSubsampleRatio420)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			yy, cb, cr := color.RGBToYCbCr(uint8(x*7), uint8(y*11), uint8(x*y))
			ycc.Y[ycc.YOffset(x, y)] = yy
			ycc.Cb[ycc.COffset(x, y)], ycc.Cr[ycc.COffset(x, y)] = cb, cr
		}
	}
	sub := nrgba.SubImage(image.Rect(5, 3, w, h)).(*image.NRGBA)

	for name, src := range map[string]image.Image{"NRGBA": nrgba, "RGBA": rgba, "YCbCr": ycc, "NRGBA 하위 영역": sub} {
		fast := image.NewRGBA(image.Rect(0, 0, 11, 7))
		slow := image.NewRGBA(fast.Bounds())
		shrink(fast, src)
		shrink(slow, opaque{src})
		for i := range fast.Pix {
			if d := int(fast.Pix[i]) - int(slow.Pix[i]); d < -1 || d > 1 {
				t.Errorf("%s: 바이트 %d 가 %d, 일반 경로는 %d", name, i, fast.Pix[i], slow.Pix[i])
				break
			}
		}
	}
}

// **서버 메모리가 모자라면 펼치기 전에 거부한다** — 펼치다 죽으면 쓰던 글이 날아가고 사이트가 502 다.
func TestProcessImageRefusesWhatMemoryCannotHold(t *testing.T) {
	data := encodeJPEG(t, halves(2400, 1600))
	orig := availableMemory
	t.Cleanup(func() { availableMemory = orig })

	availableMemory = func() (int64, bool) { return 20 << 20, true } // 20MB 남았다
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err := ProcessImage(data, "image/jpeg")
	runtime.ReadMemStats(&after)
	if !errors.Is(err, ErrImageNoMemory) {
		t.Fatalf("= %v, want ErrImageNoMemory", err)
	}
	if got := after.TotalAlloc - before.TotalAlloc; got > 4<<20 {
		t.Errorf("거부하기 전에 %.1fMB 를 할당했다 — 펼친 것이다", float64(got)/1e6)
	}

	availableMemory = func() (int64, bool) { return 2 << 30, true } // 넉넉하다
	if _, err := ProcessImage(data, "image/jpeg"); err != nil {
		t.Errorf("넉넉한데 = %v", err)
	}
	availableMemory = func() (int64, bool) { return 0, false } // 잴 수 없는 OS — 화소 상한만 본다
	if _, err := ProcessImage(data, "image/jpeg"); err != nil {
		t.Errorf("잴 수 없는데 = %v", err)
	}
}

// 가용 메모리는 MemAvailable + SwapFree 다 — 스왑을 둔 512MB 서버는 큰 사진도 (느리지만) 처리한다.
func TestMeminfoCountsAvailableAndSwap(t *testing.T) {
	dir := t.TempDir()
	write := func(s string) string {
		p := dir + "/meminfo"
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	got, ok := meminfo(write("MemTotal:         491520 kB\nMemFree:           20480 kB\nMemAvailable:     153600 kB\nSwapTotal:       1048576 kB\nSwapFree:        1000000 kB\n"))
	if want := int64(153600+1000000) * 1024; !ok || got != want {
		t.Errorf("= %d, %v, want %d", got, ok, want)
	}
	if _, ok := meminfo(write("MemTotal: 491520 kB\nSwapFree: 0 kB\n")); ok {
		t.Error("MemAvailable 이 없는데 ok — 잴 수 없으면 거부하지 않아야 한다")
	}
	if _, ok := meminfo(dir + "/없는 파일"); ok {
		t.Error("파일이 없는데 ok")
	}
}

// 펼친 크기는 색 형식으로 정해진다. 16비트 PNG 는 같은 화소 수의 8비트 PNG 의 두 배다.
func TestDecodeBytesFollowsTheColorModel(t *testing.T) {
	const px = 1000 * 1000
	for _, tc := range []struct {
		model color.Model
		per   int64
	}{
		{color.YCbCrModel, 3}, {color.NRGBAModel, 4}, {color.RGBAModel, 4}, {color.NRGBA64Model, 8},
		{color.RGBA64Model, 8}, {color.GrayModel, 1}, {color.Gray16Model, 2}, {color.CMYKModel, 4},
		{color.Palette{color.Black, color.White}, 1},
	} {
		if got := decodeBytes(image.Config{ColorModel: tc.model, Width: 1000, Height: 1000}); got != px*tc.per {
			t.Errorf("%T: %d, want %d", tc.model, got, px*tc.per)
		}
	}
	// 처리 전체: 펼친 원본 + 줄인 결과(6000×4000 → 1920×1280) RGBA 세 벌 + 8MB.
	cfg := image.Config{ColorModel: color.NRGBAModel, Width: 6000, Height: 4000}
	if got, want := workBytes(cfg), int64(6000*4000*4+3*1920*1280*4+8<<20); got != want {
		t.Errorf("workBytes(6000×4000 PNG) = %d, want %d", got, want)
	}
	// 줄일 필요가 없는 작은 그림은 결과도 원본 크기다.
	small := image.Config{ColorModel: color.NRGBAModel, Width: 800, Height: 600}
	if got, want := workBytes(small), int64(800*600*4+3*800*600*4+8<<20); got != want {
		t.Errorf("workBytes(800×600) = %d, want %d", got, want)
	}
}
