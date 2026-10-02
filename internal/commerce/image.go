package commerce

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/emirue/ondolith/internal/commerce/commerceq"
	"github.com/emirue/ondolith/internal/content"
)

// MaxProductImages bounds how many images one product carries. 상한이 없으면
// 상품 하나가 디스크를 다 쓴다 — 첨부의 글당 상한과 같은 이유다.
const MaxProductImages = 10

// ProductImage is one row of product_images (D30).
//
// **원본을 그대로 내보낸다.** 썸네일을 만들지 않는다 — 이미지 처리 라이브러리는
// 의존성 하나와 디코더 공격면을 들이는데, 목록의 크기는 CSS 가 정할 수 있다
// (`aspect-ratio` + `object-fit`). 그래서 이 구조체에 폭·높이가 없다.
type ProductImage struct {
	ID           string
	ProductID    string
	StoredPath   string
	OriginalName string
	MIMEType     string
	ByteSize     int64
	CreatedAt    time.Time
}

func imageOf(r commerceq.ProductImage) ProductImage {
	return ProductImage{ID: r.ID, ProductID: r.ProductID, StoredPath: r.StoredPath,
		OriginalName: r.OriginalName, MIMEType: r.MimeType, ByteSize: r.ByteSize,
		CreatedAt: r.CreatedAt}
}

// Images is the store's view of the upload directory, for product images.
// 첨부(content.Attachments)와 같은 루트를 쓴다 — 웹루트·테마 트리 밖이다 (D60 3항).
type Images struct {
	store *Store
	root  string
}

func (s *Store) ImagesIn(root string) *Images { return &Images{store: s, root: root} }

// Save validates, writes the file and records the row (A-502, FR-601).
//
// **검증은 여기서 하지 않는다** — D60 의 네 겹은 전부 content.StoreUpload 안에
// 있고, 첨부·상품 이미지가 같은 함수를 지난다 (NFR-201). 여기서 정하는 것은
// 「이미지만」이라는 범위뿐이고, 그것도 허용목록을 좁히는 쪽이다. limits 는
// A-309 의 설정값이다: 크기 상한과 운영자가 뺀 확장자가 상품 이미지에도 걸린다.
//
// 행을 **마지막에** 쓴다 (첨부와 같은 순서). 파일 없는 행은 깨진 이미지로
// 손님에게 보이고, 행 없는 파일은 아무에게도 안 보인다.
func (im *Images) Save(ctx context.Context, productID, name string, r io.Reader,
	limits content.UploadLimits) (ProductImage, error) {

	// 개수를 먼저 센다. 쓴 뒤에 세면 상한을 넘긴 파일이 디스크에 다녀간다.
	n, err := im.store.q.CountProductImages(ctx, productID)
	if err != nil {
		return ProductImage{}, err
	}
	if n >= MaxProductImages {
		return ProductImage{}, fmt.Errorf("%w: 상품당 %d개", content.ErrUploadTooMany, MaxProductImages)
	}

	stored, err := content.StoreUpload(im.root, name, r, time.Now(), limits.ImageOnly())
	if err != nil {
		return ProductImage{}, err
	}
	row, err := im.store.q.CreateProductImage(ctx, commerceq.CreateProductImageParams{
		ProductID: productID, StoredPath: stored.StoredPath, OriginalName: stored.OriginalName,
		MimeType: stored.MIMEType, ByteSize: stored.ByteSize})
	if err != nil {
		// 행이 안 들어갔으면 바이트도 남기지 않는다. 아무것도 가리키지 않는
		// 파일은 나중에 살아 있는 이미지와 구분할 수 없다.
		_ = content.RemoveUpload(im.root, stored.StoredPath)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return ProductImage{}, ErrNotFound // 없는 상품
		}
		return ProductImage{}, err
	}
	return ProductImage{ID: row.ID, CreatedAt: row.CreatedAt, ProductID: productID,
		StoredPath: stored.StoredPath, OriginalName: stored.OriginalName,
		MIMEType: stored.MIMEType, ByteSize: stored.ByteSize}, nil
}

// List is a product's images in upload order. 첫 번째가 대표 이미지다.
func (im *Images) List(ctx context.Context, productID string) ([]ProductImage, error) {
	rows, err := im.store.q.ProductImages(ctx, productID)
	if err != nil {
		return nil, err
	}
	out := make([]ProductImage, 0, len(rows))
	for _, r := range rows {
		out = append(out, imageOf(r))
	}
	return out, nil
}

// ByID reads one image **with its product's visibility** (P-306).
//
// 둘을 함께 돌려주는 이유는 VariantForPurchase 와 같다: 이미지 id 는 누구나 쥘
// 수 있는 URL 이고, 노출 여부를 따로 읽게 하면 그것을 안 읽는 호출자가 생긴다.
func (im *Images) ByID(ctx context.Context, id string) (img *ProductImage, productVisible bool, err error) {
	r, err := im.store.q.ProductImageByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, ErrNotFound
	}
	if err != nil {
		return nil, false, err
	}
	return &ProductImage{ID: r.ID, ProductID: r.ProductID, StoredPath: r.StoredPath,
		OriginalName: r.OriginalName, MIMEType: r.MimeType, ByteSize: r.ByteSize,
		CreatedAt: r.CreatedAt}, r.ProductVisible, nil
}

// Open returns the file for serving.
func (im *Images) Open(img *ProductImage) (*os.File, error) {
	return content.OpenUpload(im.root, img.StoredPath)
}

// Delete removes the row and then the file, and reports whose image it was.
//
// 행이 먼저다 (A-309 와 같은 결정). 파일 삭제가 실패하면 보고만 한다 — 행은
// 이미 갔고, 남은 파일은 아무도 가리키지 않는 쓰레기일 뿐이다.
func (im *Images) Delete(ctx context.Context, id string) (productID string, err error) {
	r, err := im.store.q.DeleteProductImage(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return r.ProductID, content.RemoveUpload(im.root, r.StoredPath)
}

// DeleteProduct removes the product and then the files its images left behind.
//
// 이미지 행은 CASCADE 로 가지만 **파일은 따라가지 않는다.** 정리 잡이 없으므로
// (NFR-103) 여기서 안 지우면 영원히 남는다. 경로를 먼저 읽는다 — 행이 사라진
// 뒤에는 어느 파일이 그 상품의 것이었는지 알 방법이 없다. 삭제가 거부되면
// (주문된 상품, ErrProductInUse) 파일은 건드리지 않는다.
func (im *Images) DeleteProduct(ctx context.Context, productID string) error {
	paths, err := im.store.q.ProductImagePaths(ctx, productID)
	if err != nil {
		return err
	}
	if err := im.store.DeleteProduct(ctx, productID); err != nil {
		return err
	}
	var firstErr error
	for _, p := range paths {
		if err := content.RemoveUpload(im.root, p); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
