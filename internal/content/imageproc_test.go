package content

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
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
