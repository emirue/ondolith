package app

import (
	"testing"

	"github.com/emirue/ondolith/internal/backup"
)

// 고른 적이 없는 자동 백업은 꺼짐·3시·7개다 — A-604 화면이 보이는 기본값과 같아야
// 「화면에는 3시인데 실제로는 0시」 가 생기지 않는다 (FR-715).
func TestBackupScheduleDefaultsMatchTheScreen(t *testing.T) {
	if got := backupSchedule(map[string]string{}); got != (backup.Schedule{Enabled: false, Hour: 3, Keep: 7}) {
		t.Errorf("기본값 = %+v", got)
	}
	got := backupSchedule(map[string]string{"backup.auto": "1", "backup.hour": "0", "backup.keep": "30"})
	if got != (backup.Schedule{Enabled: true, Hour: 0, Keep: 30}) {
		t.Errorf("저장한 값 = %+v", got)
	}
}
