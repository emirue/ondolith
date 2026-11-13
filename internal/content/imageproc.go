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
	"math"
	"os"
	"strconv"
	"strings"
	"sync"

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
	// ErrImageNoMemory 는 펼치는 데 드는 메모리가 서버에 남아 있지 않은 사진이다. 펼치다 죽으면
	// 쓰던 글이 날아가고 사이트가 502 가 된다 — 펼치기 전에 거부한다.
	ErrImageNoMemory = errors.New("content: 서버 메모리가 부족해 사진을 처리할 수 없습니다")
)

// availableMemory is what the process can still take — Linux 의 MemAvailable + SwapFree.
// 잴 수 없는 OS(개발 맥)에서는 ok=false 이고, 그때는 MediaMaxPixels 만 막는다. 시험이 바꿔 끼운다.
var availableMemory = func() (int64, bool) { return meminfo("/proc/meminfo") }

// meminfo reads MemAvailable and SwapFree (kB) from a /proc/meminfo-shaped file.
// 스왑을 더하는 이유: 스왑을 둔 512MB 서버는 큰 사진도 (느리지만) 처리한다 — 거부할 일이 아니다.
func meminfo(path string) (int64, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	var avail, swap int64
	found := false
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		kb, err := strconv.ParseInt(f[1], 10, 64)
		if err != nil {
			continue
		}
		switch f[0] {
		case "MemAvailable:":
			avail, found = kb*1024, true
		case "SwapFree:":
			swap = kb * 1024
		}
	}
	return avail + swap, found
}

// decodeBytes is the heap one decode takes: 화소 수 × 색 형식의 화소당 바이트. JPEG(YCbCr)는
// 4:4:4 가 최악이라 3, 16비트 PNG 는 8 이다.
func decodeBytes(cfg image.Config) int64 {
	per := int64(4)
	switch cfg.ColorModel {
	case color.YCbCrModel:
		per = 3
	case color.GrayModel:
		per = 1
	case color.Gray16Model:
		per = 2
	case color.NRGBA64Model, color.RGBA64Model:
		per = 8
	}
	if _, ok := cfg.ColorModel.(color.Palette); ok {
		per = 1
	}
	return int64(cfg.Width) * int64(cfg.Height) * per
}

// workBytes is what processing one photo takes besides its file: 펼친 원본 한 벌, 줄인 결과 RGBA
// 세 벌(줄인 것·회전·JPEG 흰 바탕), 썸네일과 인코딩 버퍼 몫 8MB. 2026-11-14 메모리를 180MB 로 묶은
// 리눅스 컨테이너에서 잰 정점(6000×4000 PNG 161MB)이 이 값 + 파일 + 런타임과 맞았다.
func workBytes(cfg image.Config) int64 {
	w, h := fitSize(cfg.Width, cfg.Height, MediaMaxEdge)
	return decodeBytes(cfg) + 3*int64(w)*int64(h)*4 + 8<<20
}

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

	// 잠금 안에서 잰다 — 펼치기는 한 번에 하나라, 앞 사진이 쓴 메모리가 돌아온 뒤의 값이다.
	// 가용분을 다 쓰면 같은 서버의 PostgreSQL 이 쓸 몫이 없다 — 5분의 1 을 남긴다.
	if avail, ok := availableMemory(); ok {
		if need := workBytes(cfg); need > avail/5*4 {
			return ProcessedImage{}, fmt.Errorf("%w: %d×%d 에 %dMB, 남은 %dMB", ErrImageNoMemory,
				cfg.Width, cfg.Height, need>>20, avail>>20)
		}
	}

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
	w, h := fitSize(b.Dx(), b.Dy(), edge)
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	if w == b.Dx() && h == b.Dy() {
		stddraw.Draw(dst, dst.Bounds(), src, b.Min, stddraw.Src)
		return dst
	}
	shrink(dst, src)
	return dst
}

// fitSize is w×h scaled down so the longer edge is at most edge.
func fitSize(w, h, edge int) (int, int) {
	if w > edge || h > edge {
		if w >= h {
			return edge, max(1, h*edge/w)
		}
		return max(1, w*edge/h), edge
	}
	return w, h
}

// shrink area-averages src into the smaller dst, one source row at a time.
//
// **결과 크기만큼의 메모리만 쓴다.** x/image/draw 의 커널 축소(CatmullRom)는 「결과 폭 × 원본 높이」
// 의 [4]float64 임시 버퍼를 잡는다 — 4000×3000 휴대폰 사진 한 장에 250MB 를 할당했고, 512MB 서버가
// 사진 저장 중에 죽었다 (2026-11-14 운영). 줄이기만 하므로 면적 평균(결과 화소 하나가 덮는 원본
// 화소들의 넓이 가중 평균)으로 충분하다. 색은 미리 곱한 알파로 평균한다 — 그대로 평균하면 투명한
// 가장자리가 어두워진다. dst 는 src 보다 크지 않아야 한다.
func shrink(dst *image.RGBA, src image.Image) {
	b := src.Bounds()
	dw, dh := dst.Bounds().Dx(), dst.Bounds().Dy()
	xs, ys := areaSpans(b.Dx(), dw), areaSpans(b.Dy(), dh)
	row := make([]float64, b.Dx()*4) // 원본 한 줄, 미리 곱한 RGBA
	acc := make([]float64, dw*4)     // 결과 한 줄을 쌓는 곳
	for dy, sy := range ys {
		clear(acc)
		for i, wy := range sy.weights {
			readRow(src, b.Min.X, b.Min.Y+sy.first+i, row)
			for dx, sx := range xs {
				var r, g, bl, a float64
				for j, wx := range sx.weights {
					k := (sx.first + j) * 4
					r, g, bl, a = r+row[k]*wx, g+row[k+1]*wx, bl+row[k+2]*wx, a+row[k+3]*wx
				}
				k := dx * 4
				acc[k], acc[k+1], acc[k+2], acc[k+3] = acc[k]+r*wy, acc[k+1]+g*wy, acc[k+2]+bl*wy, acc[k+3]+a*wy
			}
		}
		out := dst.Pix[dst.PixOffset(dst.Bounds().Min.X, dst.Bounds().Min.Y+dy):]
		for k, v := range acc {
			out[k] = uint8(min(max(v+0.5, 0), 255))
		}
	}
}

// span is the input cells one output cell covers: first 부터 len(weights) 칸, 칸마다 덮는 비율.
type span struct {
	first   int
	weights []float64
}

// areaSpans maps m output cells onto n ≥ m input cells. 칸마다 가중치의 합은 1 이다.
func areaSpans(n, m int) []span {
	out := make([]span, m)
	scale := float64(n) / float64(m)
	for i := range out {
		lo, hi := float64(i)*scale, float64(i+1)*scale
		first, last := int(lo), min(int(math.Ceil(hi)), n)
		ws := make([]float64, last-first)
		for j := range ws {
			ws[j] = (min(hi, float64(first+j+1)) - max(lo, float64(first+j))) / scale
		}
		out[i] = span{first: first, weights: ws}
	}
	return out
}

// readRow fills row with one source row as premultiplied RGBA (0–255). JPEG(YCbCr)·PNG(NRGBA)·
// RGBA 는 화소 배열을 직접 읽는다 — At() 은 화소마다 인터페이스를 거쳐 2400만 화소에서 느리다.
func readRow(src image.Image, x0, y int, row []float64) {
	n := len(row) / 4
	switch s := src.(type) {
	case *image.YCbCr:
		for x := 0; x < n; x++ {
			ci := s.COffset(x0+x, y)
			r, g, b := color.YCbCrToRGB(s.Y[s.YOffset(x0+x, y)], s.Cb[ci], s.Cr[ci])
			row[x*4], row[x*4+1], row[x*4+2], row[x*4+3] = float64(r), float64(g), float64(b), 255
		}
	case *image.NRGBA:
		p := s.Pix[s.PixOffset(x0, y):]
		for x := 0; x < n; x++ {
			a := float64(p[x*4+3])
			row[x*4], row[x*4+1], row[x*4+2], row[x*4+3] =
				float64(p[x*4])*a/255, float64(p[x*4+1])*a/255, float64(p[x*4+2])*a/255, a
		}
	case *image.RGBA:
		p := s.Pix[s.PixOffset(x0, y):]
		for i := 0; i < n*4; i++ {
			row[i] = float64(p[i])
		}
	default:
		for x := 0; x < n; x++ {
			r, g, b, a := src.At(x0+x, y).RGBA()
			row[x*4], row[x*4+1], row[x*4+2], row[x*4+3] =
				float64(r)/257, float64(g)/257, float64(b)/257, float64(a)/257
		}
	}
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
