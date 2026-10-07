package content

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	stddraw "image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
	"sync"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // image.Decode 가 webp 를 읽게 등록한다
)

const (
	// MediaMaxEdge 는 본문 사진의 긴 변이다 (FR-514). 휴대폰 사진(4000px)을 그대로
	// 내보내면 글 하나가 수십 MB 가 되고, 1920px 는 데스크톱 화면 폭이다.
	MediaMaxEdge = 1920
	// MediaThumbEdge 는 썸네일의 긴 변이다 — 갤러리 카드(FR-515) 한 칸.
	MediaThumbEdge = 480
	// MediaMaxPixels 는 펼치기 전에 거부하는 픽셀 수다. 머리말은 몇 바이트로
	// 30000×30000 을 주장할 수 있고, 펼치면 3.6GB 다. 4000만(8000×5000)은 요즘
	// 휴대폰 사진(4800만 화소 모드 제외)을 받고 1GB 서버를 넘기지 않는다.
	MediaMaxPixels = 40_000_000
)

var (
	// ErrImageTooLarge 는 픽셀 수가 MediaMaxPixels 를 넘는 그림이다.
	ErrImageTooLarge = errors.New("content: 사진이 너무 큽니다")
	// ErrImageDecode 는 확장자·내용 검사를 지났는데 그림으로 읽히지 않는 파일이다.
	ErrImageDecode = errors.New("content: 사진을 읽을 수 없습니다")
)

// ProcessedImage is an uploaded picture ready to store: 본문용과 썸네일.
type ProcessedImage struct {
	Display, Thumb []byte
	MIME           string // Display 의 타입. 썸네일은 jpeg 또는 png
	Width, Height  int
}

// decodeMu 는 펼치기를 한 번에 하나로 묶는다. 사진 하나를 펼치는 데 수십 MB 가
// 들고, 동시에 몇 장이 오면 1GB 서버가 그 합을 감당해야 한다.
// ponytail: 전역 잠금 — 업로드가 줄을 서는 사이트가 되면 크기별 세마포어로.
var decodeMu sync.Mutex

// ProcessImage shrinks, straightens and re-encodes one validated upload (FR-514).
//
// mime 은 ValidateUpload 가 내용으로 잰 값이다. **다시 인코딩하므로 EXIF 가 남지
// 않는다** — 휴대폰 사진의 위치정보가 시공 현장·집 주소를 알려 준다. 그 전에
// 회전 태그를 읽어 픽셀에 반영한다. 움직이는 GIF 는 프레임을 다시 쓰지 않고 원본을
// 쓰며, 썸네일만 첫 장으로 만든다.
func ProcessImage(data []byte, mime string) (ProcessedImage, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return ProcessedImage{}, fmt.Errorf("%w: %v", ErrImageDecode, err)
	}
	if int64(cfg.Width)*int64(cfg.Height) > MediaMaxPixels {
		return ProcessedImage{}, fmt.Errorf("%w: %d×%d", ErrImageTooLarge, cfg.Width, cfg.Height)
	}
	decodeMu.Lock()
	defer decodeMu.Unlock()

	if mime == "image/gif" {
		first, err := gif.Decode(bytes.NewReader(data))
		if err != nil {
			return ProcessedImage{}, fmt.Errorf("%w: %v", ErrImageDecode, err)
		}
		thumb, err := encodeAs("image/png", fit(first, MediaThumbEdge))
		if err != nil {
			return ProcessedImage{}, err
		}
		return ProcessedImage{Display: data, Thumb: thumb, MIME: mime, Width: cfg.Width, Height: cfg.Height}, nil
	}

	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return ProcessedImage{}, fmt.Errorf("%w: %v", ErrImageDecode, err)
	}
	orientation := 1
	if mime == "image/jpeg" {
		orientation = jpegOrientation(data)
	}
	display := orient(fit(img, MediaMaxEdge), orientation)
	thumb := fit(display, MediaThumbEdge)

	// PNG 는 투명도가 있을 수 있어 PNG 로 둔다. 나머지(jpeg·webp)는 JPEG 다 —
	// webp 인코더가 표준 라이브러리와 x/image 에 없다.
	out := "image/jpeg"
	if mime == "image/png" {
		out = "image/png"
	}
	d, err := encodeAs(out, display)
	if err != nil {
		return ProcessedImage{}, err
	}
	t, err := encodeAs(out, thumb)
	if err != nil {
		return ProcessedImage{}, err
	}
	b := display.Bounds()
	return ProcessedImage{Display: d, Thumb: t, MIME: out, Width: b.Dx(), Height: b.Dy()}, nil
}

// fit scales src down so its longer edge is at most edge. 키우지는 않는다.
func fit(src image.Image, edge int) *image.RGBA {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w > edge || h > edge {
		if w >= h {
			w, h = edge, max(1, h*edge/w)
		} else {
			w, h = max(1, w*edge/h), edge
		}
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	if w == b.Dx() && h == b.Dy() {
		stddraw.Draw(dst, dst.Bounds(), src, b.Min, stddraw.Src)
		return dst
	}
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Src, nil)
	return dst
}

// orient applies an EXIF orientation (1–8) to the pixels.
func orient(src *image.RGBA, o int) *image.RGBA {
	if o < 2 || o > 8 {
		return src
	}
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var dx, dy int
			switch o {
			case 2:
				dx, dy = w-1-x, y
			case 3:
				dx, dy = w-1-x, h-1-y
			case 4:
				dx, dy = x, h-1-y
			case 5:
				dx, dy = y, x
			case 6: // 시계 방향 90°
				dx, dy = h-1-y, x
			case 7:
				dx, dy = h-1-y, w-1-x
			case 8: // 반시계 방향 90°
				dx, dy = y, w-1-x
			}
			si, di := src.PixOffset(x, y), dst.PixOffset(dx, dy)
			copy(dst.Pix[di:di+4], src.Pix[si:si+4])
		}
	}
	return dst
}

func encodeAs(mime string, img *image.RGBA) ([]byte, error) {
	var b bytes.Buffer
	var err error
	if mime == "image/png" {
		err = png.Encode(&b, img)
	} else {
		// JPEG 에는 투명도가 없다. 흰 바탕에 얹는다 — 그대로 두면 투명한 곳이 검게 나온다.
		flat := image.NewRGBA(img.Bounds())
		stddraw.Draw(flat, flat.Bounds(), &image.Uniform{C: color.White}, image.Point{}, stddraw.Src)
		stddraw.Draw(flat, flat.Bounds(), img, img.Bounds().Min, stddraw.Over)
		err = jpeg.Encode(&b, flat, &jpeg.Options{Quality: 85})
	}
	return b.Bytes(), err
}

// jpegOrientation reads the EXIF orientation tag (0x0112) of a JPEG, or 1.
//
// **신뢰할 수 없는 바이트다.** 모든 읽기 앞에 길이를 본다. 무엇이든 어긋나면
// 「회전 없음」(1)으로 물러선다 — 사진이 누워 보이는 것이 서버가 멈추는 것보다 낫다.
// 라이브러리를 들이지 않는 이유: 필요한 것은 태그 하나이고, 이 읽기는 무엇을
// 허락하는 검증이 아니다.
func jpegOrientation(b []byte) int {
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return 1
	}
	for i := 2; i+4 <= len(b); {
		if b[i] != 0xFF {
			return 1
		}
		marker := b[i+1]
		if marker == 0xD9 || marker == 0xDA { // EOI·SOS 뒤에는 메타데이터가 없다
			return 1
		}
		n := int(binary.BigEndian.Uint16(b[i+2:]))
		if n < 2 || i+2+n > len(b) {
			return 1
		}
		seg := b[i+4 : i+2+n]
		if marker == 0xE1 && len(seg) >= 6 && string(seg[:6]) == "Exif\x00\x00" {
			return tiffOrientation(seg[6:])
		}
		i += 2 + n
	}
	return 1
}

func tiffOrientation(t []byte) int {
	if len(t) < 8 {
		return 1
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 1
	}
	if bo.Uint16(t[2:]) != 42 {
		return 1
	}
	off := int(bo.Uint32(t[4:]))
	if off < 8 || off+2 > len(t) {
		return 1
	}
	for k := range int(bo.Uint16(t[off:])) {
		e := off + 2 + 12*k
		if e+12 > len(t) {
			return 1
		}
		if bo.Uint16(t[e:]) == 0x0112 && bo.Uint16(t[e+2:]) == 3 { // SHORT
			if v := int(bo.Uint16(t[e+8:])); v >= 1 && v <= 8 {
				return v
			}
			return 1
		}
	}
	return 1
}
