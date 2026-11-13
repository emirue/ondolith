package app

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/emirue/ondolith/internal/backup"
	"github.com/emirue/ondolith/internal/config"
	"github.com/emirue/ondolith/internal/content"
	"github.com/emirue/ondolith/internal/s3store/fakes3"
	"github.com/jackc/pgx/v5/pgxpool"
)

// **파일 저장소가 S3 면 자동 백업도 그 버킷의 backups/ 로 간다. 아니면 지금처럼 로컬에 쌓인다** (FR-722).
// 앱이 조립한 그대로의 스케줄러를 진짜 DB 와 가짜 버킷으로 돌린다 — 덤프만 가짜다(pg_dump 판이 기계마다 다르다;
// 진짜 덤프·복원은 make test-backup 이 본다).
func TestAutoBackupGoesToTheBucketWhenStorageIsS3(t *testing.T) {
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		t.Skipf("%s 미설정", dsnEnv)
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	for _, s := range []string{"DROP SCHEMA public CASCADE", "CREATE SCHEMA public"} {
		if _, err := pool.Exec(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	migrateAndSeed(t, pool)
	bucket := fakes3.New("site")
	t.Cleanup(bucket.Close)
	setStorage(t, pool, bucket.URL, "s3")

	cfg := &config.Config{DatabaseURL: dsn, UploadDir: t.TempDir(), BackupDir: t.TempDir()}
	store := content.NewStore(pool)
	if err := store.PutSettings(ctx, map[string]string{"backup.auto": "1", "backup.hour": "3"}); err != nil {
		t.Fatal(err)
	}
	dumps := 0
	dump := func(_ context.Context, w io.Writer) error { dumps++; _, err := w.Write([]byte("PGDMP")); return err }
	sched := newBackupScheduler(ctx, cfg, store, &storageRemote{settings: store.SettingsForForm}, dump,
		slog.New(slog.DiscardHandler))
	day := func(d int) time.Time { return time.Date(2026, 11, d, 3, 0, 0, 0, time.Local) }
	lastLog := func() string {
		var s string
		_ = pool.QueryRow(ctx, `SELECT summary FROM operation_logs WHERE action = 'backup.auto' ORDER BY created_at DESC, id DESC LIMIT 1`).Scan(&s)
		return s
	}
	local := func() []backup.File {
		files, err := backup.List(cfg.Backups())
		if err != nil {
			t.Fatal(err)
		}
		return files
	}

	// ① S3 — 버킷에 가고 로컬에는 없다. 작업 로그와 마지막 백업이 남는다.
	sched.Tick(ctx, day(14))
	data, ok := bucket.Object("backups/auto-20261114-0300.zip")
	if !ok {
		t.Fatalf("버킷에 백업이 없다: %v", bucket.Keys())
	}
	if zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data))); err != nil || len(zr.File) == 0 || zr.File[0].Name != "ondolith.dump" {
		t.Errorf("버킷의 백업이 덤프를 담은 zip 이 아니다: %v", err)
	}
	if files := local(); len(files) != 0 {
		t.Errorf("버킷으로 갔는데 로컬에 %v", files)
	}
	if l := lastLog(); !strings.Contains(l, "버킷 backups/auto-20261114-0300.zip") {
		t.Errorf("작업 로그 = %q", l)
	}
	kv, err := store.Settings(ctx, "backup.last_at", "backup.last_where")
	if err != nil {
		t.Fatal(err)
	}
	if kv["backup.last_where"] != "s3:backups/auto-20261114-0300.zip" || !strings.HasPrefix(kv["backup.last_at"], "2026-11-14T03:00") {
		t.Errorf("마지막 백업 = %v", kv)
	}

	// ② 재시작 — 새 스케줄러는 지난 날을 모른다. 버킷 백업은 로컬에 흔적이 없어도 같은 날 다시 만들지 않는다.
	again := newBackupScheduler(ctx, cfg, store, &storageRemote{settings: store.SettingsForForm}, dump,
		slog.New(slog.DiscardHandler))
	again.Tick(ctx, day(14).Add(10*time.Minute))
	if dumps != 1 {
		t.Errorf("재시작한 같은 날 또 만들었다 (덤프 %d번)", dumps)
	}

	// ③ 버킷이 거부한 날 — 로컬에 남기고 실패로 적는다. 마지막 백업은 로컬을 가리킨다.
	bucket.RejectCode = "AccessDenied"
	sched.Tick(ctx, day(15))
	if files := local(); len(files) != 1 || files[0].Name != "auto-20261115-0300.zip" {
		t.Errorf("버킷이 거부한 날 로컬 = %v", files)
	}
	if l := lastLog(); !strings.Contains(l, "버킷에 올리지 못해 이 서버에 남겼다") || !strings.Contains(l, "AccessDenied") {
		t.Errorf("실패 기록 = %q", l)
	}
	if kv, _ := store.Settings(ctx, "backup.last_where"); kv["backup.last_where"] != "local:auto-20261115-0300.zip" {
		t.Errorf("마지막 백업 = %q", kv["backup.last_where"])
	}

	// ④ 저장소를 로컬로 되돌리면 백업도 로컬이다 (자격증명이 남아 있어도).
	bucket.RejectCode = ""
	setStorage(t, pool, bucket.URL, "")
	sched.Tick(ctx, day(16))
	if _, ok := bucket.Object("backups/auto-20261116-0300.zip"); ok {
		t.Error("저장소가 로컬인데 버킷으로 갔다")
	}
	if files := local(); len(files) != 2 || files[0].Name != "auto-20261116-0300.zip" {
		t.Errorf("로컬 백업 = %v", files)
	}
}
