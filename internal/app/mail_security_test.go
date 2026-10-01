package app

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"
)

func TestSMTPRespectsTLSMode(t *testing.T) {
	for _, tc := range []struct {
		mode      string
		advertise bool
		wantError bool
	}{
		{"none", false, false},
		{"none", true, false},
		{"starttls", false, true},
		{"", false, true},
		{"invalid", false, true},
	} {
		t.Run(tc.mode+"/"+map[bool]string{true: "advertised", false: "absent"}[tc.advertise], func(t *testing.T) {
			client, server := net.Pipe()
			defer server.Close()
			_ = server.SetDeadline(time.Now().Add(3 * time.Second))
			seen := make(chan string, 1)
			go func() { seen <- fakeSMTPWithSTARTTLS(server, tc.advertise) }()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			err := sendSMTP(ctx, client, "localhost", tc.mode, nil,
				"from@example.com", []string{"to@example.com"}, []byte("reset-token"))
			if (err != nil) != tc.wantError {
				t.Errorf("err = %v, wantError = %v", err, tc.wantError)
			}
			transcript := <-seen
			if tc.wantError && (strings.Contains(transcript, "MAIL FROM") || strings.Contains(transcript, "reset-token")) {
				t.Errorf("TLS 검증 실패 뒤에도 메일을 보냈다: %s", transcript)
			}
			if !tc.wantError && !strings.Contains(transcript, "reset-token") {
				t.Errorf("메일 본문이 전송되지 않았다: %s", transcript)
			}
		})
	}
}

func TestSMTPImplicitTLSVerifiesCertificateBeforeGreeting(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{
		SerialNumber: big.NewInt(1), DNSNames: []string{"localhost"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	client, server := net.Pipe()
	defer server.Close()
	_ = server.SetDeadline(time.Now().Add(3 * time.Second))
	done := make(chan error, 1)
	go func() {
		peer := tls.Server(server, &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}})
		done <- peer.Handshake()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err = sendSMTP(ctx, client, "localhost", "tls", nil,
		"from@example.com", []string{"to@example.com"}, []byte("reset-token"))
	var verification *tls.CertificateVerificationError
	if !errors.As(err, &verification) {
		t.Errorf("TLS 인증서 검증 오류 대신 %v 를 반환했다", err)
	}
	if err := <-done; err == nil {
		t.Error("신뢰하지 않는 인증서로 TLS 연결이 성공했다")
	}
}

func TestSMTPCancelInterruptsGreetingRead(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reading := make(chan struct{})
	go func() {
		_, _ = server.Write([]byte("220")) // An incomplete greeting leaves the read waiting.
		close(reading)
	}()
	done := make(chan error, 1)
	go func() {
		done <- sendSMTP(ctx, client, "localhost", "none", nil,
			"from@example.com", []string{"to@example.com"}, []byte("reset-token"))
	}()
	select {
	case <-reading:
	case <-time.After(time.Second):
		t.Fatal("SMTP 인사말을 읽기 시작하지 않았다")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Error("취소된 전송이 성공했다")
		}
	case <-time.After(time.Second):
		t.Error("취소 뒤에도 SMTP 응답을 기다린다")
	}
}
