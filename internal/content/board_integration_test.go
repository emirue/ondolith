package content

import (
	"context"
	"errors"
	"testing"
)

func newBoard(slug, name string) Board {
	return Board{Slug: slug, Name: name, AllowComments: true, PerPage: 20}
}

func grantCount(t *testing.T, s *Store, boardID string) int {
	t.Helper()
	var n int
	if err := s.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM role_permissions WHERE board_id = $1`, boardID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// D14 4.2: 게시판 행과 부여 행이 같은 트랜잭션에 들어간다. 부여 없는 게시판은
// 만든 사람에게도 보이지 않고, 그것을 고치는 화면은 다른 화면이다 — 운영자의
// 다음 행동은 "실패했나 보다" 하고 게시판을 하나 더 만드는 것이다.
func TestBoardAndItsGrantsLandTogether(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()

	id, err := s.CreateBoard(ctx, newBoard("free", "자유게시판"), PresetPublic)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := PresetGrants(PresetPublic)
	if got := grantCount(t, s, id); got != len(want) {
		t.Errorf("부여 행 %d개, want %d개", got, len(want))
	}

	// 실제로 그 역할·권한 조합인지 본다. 개수만 세면 엉뚱한 조합이 통과한다.
	rows, err := s.pool.Query(ctx, `
		SELECT r.key, p.key FROM role_permissions rp
		JOIN roles r ON r.id = rp.role_id
		JOIN permissions p ON p.id = rp.permission_id
		WHERE rp.board_id = $1`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[Grant]bool{}
	for rows.Next() {
		var g Grant
		if err := rows.Scan(&g.Role, &g.Permission); err != nil {
			t.Fatal(err)
		}
		got[g] = true
	}
	for _, g := range want {
		if !got[g] {
			t.Errorf("프리셋 행이 빠졌다: %+v", g)
		}
	}
}

// 부여가 실패하면 게시판 행도 남지 않는다. 스코프가 아닌 권한을 넣으면 부여
// SELECT 가 0행을 쓰고, 그때 트랜잭션 전체가 되감겨야 한다.
func TestFailedGrantRollsBackTheBoard(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()

	// 프리셋이 쓰는 권한 하나를 스코프가 아닌 것으로 바꿔 부여를 실패시킨다.
	if _, err := pool.Exec(ctx,
		`UPDATE permissions SET is_scoped = false WHERE key = 'post.read'`); err != nil {
		t.Fatal(err)
	}

	if _, err := s.CreateBoard(ctx, newBoard("free", "자유게시판"), PresetPublic); err == nil {
		t.Fatal("부여가 실패했는데 게시판이 만들어졌다")
	}
	var boards int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM boards`).Scan(&boards); err != nil {
		t.Fatal(err)
	}
	if boards != 0 {
		t.Errorf("되감기지 않았다: 게시판 %d행이 남았다", boards)
	}
	var grants int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM role_permissions WHERE board_id IS NOT NULL`).Scan(&grants); err != nil {
		t.Fatal(err)
	}
	if grants != 0 {
		t.Errorf("되감기지 않았다: 부여 %d행이 남았다", grants)
	}
}

func TestDuplicateSlugIsRefused(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	if _, err := s.CreateBoard(ctx, newBoard("free", "자유"), PresetPublic); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateBoard(ctx, newBoard("free", "다른 이름"), PresetPublic); !errors.Is(err, ErrSlugTakenBoard) {
		t.Errorf("중복 주소가 통과했다: %v", err)
	}
}

// 알 수 없는 프리셋이면 게시판을 만들기 전에 멈춘다.
func TestUnknownPresetCreatesNothing(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	if _, err := s.CreateBoard(ctx, newBoard("free", "자유"), "전체공개"); !errors.Is(err, ErrUnknownPreset) {
		t.Fatalf("err = %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM boards`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("게시판 %d행이 만들어졌다", n)
	}
}

// 게시판을 지우면 스코프 부여도 함께 간다 (D30 3-1 CASCADE). 남으면 다음
// 게시판이 같은 id 를 받을 일은 없지만, 죽은 행이 A-404 목록에 계속 뜬다.
func TestDeletingABoardTakesItsGrantsAndPosts(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	id, err := s.CreateBoard(ctx, newBoard("free", "자유"), PresetPublic)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO posts (board_id, title, body) VALUES ($1, '글', '본문')`, id); err != nil {
		t.Fatal(err)
	}

	// 확인 없이 지우려 하면 글 수를 알려주고 멈춘다 — A-305 의 확인 단계가
	// 뜻을 가지려면 몇 건이 사라지는지 말할 수 있어야 한다.
	err = s.DeleteBoard(ctx, id, false)
	if !errors.Is(err, ErrBoardInUse) {
		t.Fatalf("글이 있는데 확인 없이 지웠다: %v", err)
	}
	if !contains([]string{err.Error()}, err.Error()) || err.Error() == "" {
		t.Error("몇 건인지 말하지 않는다")
	}

	if err := s.DeleteBoard(ctx, id, true); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`SELECT count(*) FROM boards`,
		`SELECT count(*) FROM posts`,
		`SELECT count(*) FROM role_permissions WHERE board_id IS NOT NULL`,
	} {
		var n int
		if err := pool.QueryRow(ctx, q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%s → %d행이 남았다", q, n)
		}
	}
}

func TestBoardFieldsRoundTripInOrder(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	id, err := s.CreateBoard(ctx, newBoard("free", "자유"), PresetPublic)
	if err != nil {
		t.Fatal(err)
	}

	fields := []FieldSchema{
		{Key: "color", Label: "색상", Type: FieldSelect, Options: []string{"빨강", "파랑"}, Sort: 2},
		{Key: "memo", Label: "메모", Type: FieldText, Required: true, ShowInList: true, Sort: 1},
	}
	for _, f := range fields {
		if err := s.SaveBoardField(ctx, id, f); err != nil {
			t.Fatalf("%s: %v", f.Key, err)
		}
	}

	got, err := s.BoardFields(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("%d개, want 2개", len(got))
	}
	// sort_order 순서다. 정의한 순서가 아니라.
	if got[0].Key != "memo" || got[1].Key != "color" {
		t.Errorf("순서가 sort_order 를 따르지 않는다: %s, %s", got[0].Key, got[1].Key)
	}
	if !got[0].Required || !got[0].ShowInList {
		t.Errorf("플래그가 왕복하지 않았다: %+v", got[0])
	}
	if len(got[1].Options) != 2 || got[1].Options[0] != "빨강" {
		t.Errorf("선택지가 왕복하지 않았다: %v", got[1].Options)
	}

	// 같은 키로 다시 저장하면 갱신이지 중복이 아니다.
	if err := s.SaveBoardField(ctx, id, FieldSchema{
		Key: "memo", Label: "바뀐 라벨", Type: FieldText, Sort: 5}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.BoardFields(ctx, id)
	if len(got) != 2 {
		t.Errorf("갱신이 아니라 삽입됐다: %d개", len(got))
	}
}

// 예약 키는 저장소에서도 막힌다. 핸들러가 유일한 방어면 다음 호출자가 뚫는다.
func TestStoreRefusesReservedFieldKey(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	id, err := s.CreateBoard(ctx, newBoard("free", "자유"), PresetPublic)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveBoardField(ctx, id, FieldSchema{
		Key: "title", Label: "제목", Type: FieldText}); err == nil {
		t.Error("예약 키가 저장됐다")
	}
}

// D14 3절 규칙 4: 필드 정의를 지워도 글에 적힌 값은 남는다.
func TestDeletingAFieldKeepsStoredValues(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	id, err := s.CreateBoard(ctx, newBoard("free", "자유"), PresetPublic)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveBoardField(ctx, id, FieldSchema{Key: "memo", Label: "메모", Type: FieldText}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO posts (board_id, title, body, custom_fields)
		 VALUES ($1, '글', '본문', '{"memo":"적어둔 값"}')`, id); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteBoardField(ctx, id, "memo"); err != nil {
		t.Fatal(err)
	}
	var v string
	if err := pool.QueryRow(ctx,
		`SELECT custom_fields->>'memo' FROM posts`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != "적어둔 값" {
		t.Errorf("필드를 지웠더니 글의 값도 사라졌다: %q", v)
	}
}
