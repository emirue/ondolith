package app

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// P-905 는 인증 없는 공개 주소다. 거부마다 한 줄이면 밖에서 저널을 채울 수 있다 —
// 한 간격에 한 줄만 쓰고, 그동안 삼킨 수를 다음 줄에 붙인다.
func TestRefusedWebhooksLogAtMostOncePerInterval(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	var th logThrottle
	t0 := time.Unix(1_700_000_000, 0)
	for i := range 100 {
		th.log(log, slog.LevelWarn, t0.Add(time.Duration(i)*time.Millisecond), "웹훅 거부", "remote", "203.0.113.9")
	}
	th.log(log, slog.LevelWarn, t0.Add(webhookWarnEvery), "웹훅 거부", "remote", "203.0.113.9")

	if n := strings.Count(buf.String(), "웹훅 거부"); n != 2 {
		t.Fatalf("101번 거부에 %d줄, want 2:\n%s", n, buf.String())
	}
	if !strings.Contains(buf.String(), "suppressed=99") {
		t.Errorf("삼킨 수가 다음 줄에 없다:\n%s", buf.String())
	}
}
