package app

import (
	"testing"

	"github.com/emirue/ondolith/internal/backup"
)

// 고른 적이 없는 자동 백업은 꺼짐·0시(사용이 가장 적은 자정)·7개다 — A-604 화면이 보이는
// 기본값과 같아야 「화면에는 0시인데 실제로는 3시」 가 생기지 않는다 (FR-715).
func TestBackupScheduleDefaultsMatchTheScreen(t *testing.T) {
	if got := backupSchedule(map[string]string{}); got != (backup.Schedule{Enabled: false, Hour: 0, Keep: 7}) {
		t.Errorf("기본값 = %+v", got)
	}
	got := backupSchedule(map[string]string{"backup.auto": "1", "backup.hour": "4", "backup.keep": "30"})
	if got != (backup.Schedule{Enabled: true, Hour: 4, Keep: 30}) {
		t.Errorf("저장한 값 = %+v", got)
	}
}
