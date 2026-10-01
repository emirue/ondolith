package content

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/emirue/ondolith/internal/content/contentq"
)

var (
	ErrNotFound   = errors.New("content: 찾을 수 없습니다")
	ErrSlugTaken  = errors.New("content: 이미 사용 중인 슬러그입니다")
	ErrNoRowsSave = errors.New("content: 저장 대상이 없습니다")
)

// Store reads and writes pages, settings and menus. Validation lives in
// validate.go; assembly lives in menu.go. This file is queries.
//
// Every value reaches SQL as a bind parameter. Nothing here concatenates input
// into a statement, and there is no sort/filter column taken from the request —
// when one is needed it goes through an allow-list, never through escaping
// (D22 6절).
type Store struct {
	pool   *pgxpool.Pool
	sealer Sealer
	// q 는 sqlc 가 queries/*.sql 에서 생성한 질의다 (D22 6절). 트랜잭션 안에서는
	// q.WithTx(tx). 이 패키지에 손으로 스캔하는 질의는 없다 — pool 은 Begin 에만 쓴다.
	q *contentq.Queries
}

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool, q: contentq.New(pool)} }

// affected turns an :execrows result into the store's contract: a statement
// that matched nothing is ErrNotFound.
func affected(n int64, err error) error {
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Sealer is what seals a secret setting on the way in and opens it on the way
// out. secretbox.Box satisfies it; the store does not import that package so
// the tests can pass a stub.
type Sealer interface {
	Seal(name, plaintext string) (string, error)
	Open(name, stored string) (plaintext string, sealed bool, err error)
}

// UseSealer makes Settings/PutSettings seal the keys IsSecretSetting names.
// nil turns it off (tests without a config file).
func (s *Store) UseSealer(box Sealer) { s.sealer = box }

// IsSecretSetting names the settings that never sit in the database as
// plaintext and never travel back to the browser: **한 곳**이다. admin 의
// 「다시 보여주지 않는다」와 이 파일의 「봉인한다」가 같은 목록을 봐야, 새 자격
// 증명을 한쪽에만 등록하는 실수가 다른 쪽에서 드러난다.
func IsSecretSetting(key string) bool {
	switch key {
	case "pg.secret_key", "mail.smtp_password":
		return true
	}
	return strings.HasPrefix(key, "social.") && strings.HasSuffix(key, ".client_secret")
}

type Page struct {
	ID       string
	Slug     string
	Title    string
	Body     string
	Status   PageStatus
	Template string
}

// pageOf is the one place a pages row becomes a Page. 읽기 질의가 전부 열
// 전체를 고르는 것은 이 때문이다 — sqlc 가 모델 하나를 돌려주고 변환도 하나다.
func pageOf(r contentq.Page) Page {
	return Page{ID: r.ID, Slug: r.Slug, Title: r.Title, Body: r.Body,
		Status: PageStatus(r.Status), Template: r.Template}
}

func pagesOf(rows []contentq.Page) []Page {
	var out []Page
	for _, r := range rows {
		out = append(out, pageOf(r))
	}
	return out
}

func onePage(r contentq.Page, err error) (*Page, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	p := pageOf(r)
	return &p, nil
}

// PublishedPageBySlug is the public read path (P-202). The status filter is in
// the WHERE clause, not a Go comparison after the fetch: a draft must not
// travel out of the database at all, and a predicate that is not there cannot
// be forgotten by the next caller.
func (s *Store) PublishedPageBySlug(ctx context.Context, slug string) (*Page, error) {
	return onePage(s.q.PublishedPageBySlug(ctx, slug))
}

// PageBySlug is the admin read path: drafts included, because A-301 lists them.
func (s *Store) PageBySlug(ctx context.Context, slug string) (*Page, error) {
	return onePage(s.q.PageBySlug(ctx, slug))
}

// Pages lists every page for A-301, drafts included: the admin list is where an
// unpublished page is found, so filtering by status here would hide the rows the
// screen exists to show.
func (s *Store) Pages(ctx context.Context) ([]Page, error) {
	rows, err := s.q.Pages(ctx)
	if err != nil {
		return nil, err
	}
	return pagesOf(rows), nil
}

// PageByID is A-302's read. Like PageBySlug it does not filter on status: the
// edit screen is how a draft gets finished.
func (s *Store) PageByID(ctx context.Context, id string) (*Page, error) {
	return onePage(s.q.PageByID(ctx, id))
}

// CreatePage lets UNIQUE (slug) decide on collisions. Checking first and
// inserting after passes two simultaneous requests; the index is what actually
// serialises them (D30 pages).
func (s *Store) CreatePage(ctx context.Context, p Page) (string, error) {
	id, err := s.q.CreatePage(ctx, contentq.CreatePageParams{
		Slug: p.Slug, Title: p.Title, Body: p.Body, Template: p.Template})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return "", ErrSlugTaken
	}
	return id, err
}

// UpdatePage does not touch status. Publishing is a separate permission
// (page.publish vs page.update, D15 2.2); letting an edit carry a status would
// hand the first permission the second one's power.
func (s *Store) UpdatePage(ctx context.Context, id string, p Page) error {
	n, err := s.q.UpdatePage(ctx, contentq.UpdatePageParams{
		ID: id, Slug: p.Slug, Title: p.Title, Body: p.Body, Template: p.Template})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrSlugTaken
	}
	return affected(n, err)
}

// SetPageStatus moves a page between draft and published, refusing anything the
// state graph does not allow. The current status is read and compared inside
// one statement so that two publishers cannot both see `draft` and both act.
func (s *Store) SetPageStatus(ctx context.Context, id string, to PageStatus) error {
	status, err := s.q.PageStatus(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	from := PageStatus(status)
	if err := CanTransition(from, to); err != nil {
		return err
	}
	// 비교-교환이다. 위의 `FOR UPDATE` 는 자동 커밋 문장이라 잠금이 문장과 함께
	// 풀렸다 — 두 발행자가 둘 다 draft 를 읽고 둘 다 지나갈 수 있었다. 읽은
	// 상태가 그대로일 때만 옮기고, 아니면 지금 상태로 다시 판정한다.
	n, err := s.q.SetPageStatus(ctx, contentq.SetPageStatusParams{
		ID: id, To: string(to), From: string(from)})
	if err != nil {
		return err
	}
	if n == 0 {
		now, err := s.q.PageStatus(ctx, id)
		if err != nil {
			return ErrNotFound
		}
		if err := CanTransition(PageStatus(now), to); err != nil {
			return err
		}
		return ErrNoRowsSave
	}
	return nil
}

func (s *Store) DeletePage(ctx context.Context, id string) error {
	return affected(s.q.DeletePage(ctx, id))
}

// Settings are key/value. The caller names the keys it wants; there is no
// "fetch everything" because each screen owns its own keys (D30 settings).
func (s *Store) Settings(ctx context.Context, keys ...string) (map[string]string, error) {
	rows, err := s.q.Settings(ctx, keys)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(keys))
	for _, r := range rows {
		v := r.Value
		if s.sealer != nil && IsSecretSetting(r.Key) {
			// 열리지 않는 값을 평문인 척 돌려주지 않는다 — 키가 바뀐 설정 파일로
			// 부팅한 경우이고, 그 시크릿으로 PG 를 부르면 401 이 아니라 엉뚱한
			// 실패가 난다. 오류가 원인을 말한다.
			pt, _, err := s.sealer.Open(r.Key, v)
			if err != nil {
				return nil, fmt.Errorf("설정 %s: %w", r.Key, err)
			}
			v = pt
		}
		out[r.Key] = v
	}
	return out, nil
}

// PutSettings upserts. ON CONFLICT targets the primary key, which is `key`
// itself — that is why D30 made key the PK instead of adding a uuid.
func (s *Store) PutSettings(ctx context.Context, kv map[string]string) error {
	if len(kv) == 0 {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := s.q.WithTx(tx)
	for k, v := range kv {
		if s.sealer != nil && IsSecretSetting(k) {
			sealed, err := s.sealer.Seal(k, v)
			if err != nil {
				return err
			}
			v = sealed
		}
		if err := q.PutSetting(ctx, contentq.PutSettingParams{Key: k, Value: v}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// SealLegacySecrets re-writes secret settings that were stored before sealing
// existed (v0.1.0·v0.2.0 설치). 부팅 때 한 번 돈다: 평문인 채 남은 값이 있으면
// 그 백업은 여전히 평문을 담고 있다.
func (s *Store) SealLegacySecrets(ctx context.Context) (int, error) {
	if s.sealer == nil {
		return 0, nil
	}
	rows, err := s.q.LegacySecretSettings(ctx)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	legacy := make(map[string]string, len(rows))
	for _, r := range rows {
		legacy[r.Key] = r.Value
	}
	return len(legacy), s.PutSettings(ctx, legacy)
}

// MenuItems reads the whole tree in ONE query. The theme renders the menu on
// every public page (D16 menus), so a per-level query would multiply with depth
// on the hottest path in the product.
//
// Ordering matches menus_parent_sort_idx so the database can walk the index
// instead of sorting (D30).
func (s *Store) MenuItems(ctx context.Context) ([]MenuItem, error) {
	rows, err := s.q.MenuItems(ctx)
	if err != nil {
		return nil, err
	}
	var out []MenuItem
	for _, r := range rows {
		out = append(out, MenuItem{ID: r.ID, ParentID: r.ParentID, Title: r.Title,
			URL: r.Url, Sort: int(r.SortOrder)})
	}
	return out, nil
}

// CreateMenuItem inserts one row. The parent, if any, must exist — the foreign
// key says so — but a cycle is only caught when the tree is assembled, because
// no constraint can see one (D30 3절).
func (s *Store) CreateMenuItem(ctx context.Context, m MenuItem) (string, error) {
	return s.q.CreateMenuItem(ctx, contentq.CreateMenuItemParams{
		Title: m.Title, Url: m.URL, ParentID: m.ParentID, SortOrder: int32(m.Sort)})
}

// UpdateMenuItem edits one row, re-parenting included.
//
// This is the operation that can build a cycle: pointing an entry at one of its
// own descendants is legal for the row and for the foreign key, and only shows
// up when the tree is assembled. The caller checks first (A-204) — the store
// writes what it is told.
func (s *Store) UpdateMenuItem(ctx context.Context, id string, m MenuItem) error {
	return affected(s.q.UpdateMenuItem(ctx, contentq.UpdateMenuItemParams{
		ID: id, Title: m.Title, Url: m.URL, ParentID: m.ParentID, SortOrder: int32(m.Sort)}))
}

func (s *Store) DeleteMenuItem(ctx context.Context, id string) error {
	return affected(s.q.DeleteMenuItem(ctx, id))
}

// PublishedPages is the sitemap's page list (FR-510). The status filter is in
// the WHERE clause for the same reason PublishedPageBySlug has it: a draft must
// not leave the database.
func (s *Store) PublishedPages(ctx context.Context) ([]Page, error) {
	rows, err := s.q.PublishedPages(ctx)
	if err != nil {
		return nil, err
	}
	return pagesOf(rows), nil
}
