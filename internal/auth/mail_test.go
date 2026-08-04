package auth

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeSender struct {
	mu       sync.Mutex
	calls    int
	failFor  int // fail this many times, then succeed
	lastBody string
}

func (f *fakeSender) Send(_ context.Context, _, _, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.lastBody = body
	if f.calls <= f.failFor {
		return errors.New("smtp 연결 실패")
	}
	return nil
}

func (f *fakeSender) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func newTestMailer(s Sender, buf *bytes.Buffer) *Mailer {
	m := NewMailer(s, slog.New(slog.NewTextHandler(buf, nil)))
	m.sleep = func(time.Duration) {} // no real backoff in tests
	return m
}

func TestMailSucceedsFirstTry(t *testing.T) {
	s := &fakeSender{}
	m := newTestMailer(s, &bytes.Buffer{})
	<-m.SendAsync("a@example.com", "제목", "본문")
	if s.count() != 1 {
		t.Errorf("발송 %d회, want 1회", s.count())
	}
}

func TestMailRetriesWithBackoff(t *testing.T) {
	s := &fakeSender{failFor: 2}
	var log bytes.Buffer
	m := newTestMailer(s, &log)
	<-m.SendAsync("a@example.com", "제목", "본문")

	if s.count() != 3 {
		t.Errorf("발송 %d회, want 3회 (2회 실패 후 성공)", s.count())
	}
	if strings.Contains(log.String(), "발송 실패") {
		t.Error("결국 성공했는데 실패로 기록됐다")
	}
}

// NFR-103: no queue, no worker. Three attempts, then the failure is logged and
// the user is told to ask again — which they can, because these mails are all
// re-requestable.
func TestMailGivesUpAfterThreeAndLogs(t *testing.T) {
	s := &fakeSender{failFor: 99}
	var log bytes.Buffer
	m := newTestMailer(s, &log)
	<-m.SendAsync("a@example.com", "제목", "본문")

	if s.count() != 3 {
		t.Errorf("발송 %d회, want 3회", s.count())
	}
	if !strings.Contains(log.String(), "메일 발송 실패") {
		t.Error("최종 실패가 기록되지 않았다")
	}
}

// C5: a reset mail body holds the token. A token in the log is a token an
// operator can replay.
func TestMailFailureLogDoesNotCarryTheBody(t *testing.T) {
	s := &fakeSender{failFor: 99}
	var log bytes.Buffer
	m := newTestMailer(s, &log)
	const secret = "TOKEN-0123456789abcdef"
	<-m.SendAsync("a@example.com", "비밀번호 재설정", "링크: https://example.com/reset?t="+secret)

	if strings.Contains(log.String(), secret) {
		t.Errorf("토큰이 로그에 실렸다:\n%s", log.String())
	}
	if !strings.Contains(log.String(), "a@example.com") {
		t.Error("수신 주소가 없어 어느 발송이 실패했는지 알 수 없다")
	}
}

// The request must not fail because a mail server is slow: a visitor who signed
// up successfully would see an error, try again, create nothing, and understand
// less.
func TestSendAsyncReturnsImmediately(t *testing.T) {
	blocked := make(chan struct{})
	s := senderFunc(func(context.Context, string, string, string) error {
		<-blocked
		return nil
	})
	m := newTestMailer(s, &bytes.Buffer{})

	start := time.Now()
	done := m.SendAsync("a@example.com", "제목", "본문")
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Errorf("SendAsync 가 %v 동안 블록했다", elapsed)
	}
	close(blocked)
	<-done
}

type senderFunc func(context.Context, string, string, string) error

func (f senderFunc) Send(ctx context.Context, to, subject, body string) error {
	return f(ctx, to, subject, body)
}

// The request's context is cancelled when the response is written; using it
// would abort every send. A fresh context is what makes the goroutine outlive
// the request.
func TestSendUsesItsOwnContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already dead

	var seenErr error
	s := senderFunc(func(c context.Context, _, _, _ string) error {
		seenErr = c.Err()
		return nil
	})
	m := newTestMailer(s, &bytes.Buffer{})
	_ = ctx
	<-m.SendAsync("a@example.com", "제목", "본문")

	if seenErr != nil {
		t.Errorf("발송이 취소된 컨텍스트를 물려받았다: %v", seenErr)
	}
}
