package app

import (
	"bufio"
	"errors"
	"net"
	"strings"
	"testing"
)

func TestBlockMetadataAddr(t *testing.T) {
	cases := []struct {
		addr    string
		blocked bool
	}{
		{"169.254.169.254:25", true},          // the cloud metadata endpoint
		{"169.254.0.0:587", true},             // range start
		{"169.254.255.255:587", true},         // range end
		{"[::ffff:169.254.169.254]:25", true}, // same address, IPv4-mapped
		{"169.253.255.255:25", false},         // one below the range
		{"169.255.0.0:25", false},             // one above the range
		{"127.0.0.1:25", false},               // a local relay is normal
		{"10.0.0.1:25", false},                // a private relay is normal
		{"[fe80::1]:25", false},               // 권고는 169.254.0.0/16 만이다
		{"8.8.8.8:587", false},
	}
	for _, tc := range cases {
		err := blockMetadataAddr("tcp", tc.addr, nil)
		if got := errors.Is(err, ErrMailHostBlocked); got != tc.blocked {
			t.Errorf("blockMetadataAddr(%q): blocked=%v, want %v (err=%v)",
				tc.addr, got, tc.blocked, err)
		}
	}
}

// TestSendMailRefusesMetadataHost proves the Control hook is actually wired
// into the dialer sendMail uses. Nothing listens on that address; Control runs
// before connect, so the call returns our error rather than a timeout.
func TestSendMailRefusesMetadataHost(t *testing.T) {
	err := sendMail("169.254.169.254:25", "169.254.169.254", nil,
		"a@example.com", []string{"b@example.com"}, []byte("hi"))
	if !errors.Is(err, ErrMailHostBlocked) {
		t.Fatalf("sendMail to the metadata address: %v, want ErrMailHostBlocked", err)
	}
}

// TestSendMailDeliversToServer exercises the whole exchange against a real
// socket. sendMail reimplements smtp.SendMail's flow to get a dial hook, so a
// dropped step (EHLO, DATA terminator, QUIT) has to fail here.
func TestSendMailDeliversToServer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	got := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			got <- "accept: " + err.Error()
			return
		}
		defer conn.Close()
		got <- fakeSMTP(conn)
	}()

	msg := []byte("Subject: 확인\r\n\r\n본문입니다")
	err = sendMail(ln.Addr().String(), "127.0.0.1", nil,
		"from@example.com", []string{"to@example.com"}, msg)
	if err != nil {
		t.Fatalf("sendMail: %v", err)
	}

	transcript := <-got
	for _, want := range []string{
		"EHLO localhost",
		"MAIL FROM:<from@example.com>",
		"RCPT TO:<to@example.com>",
		"DATA",
		"본문입니다",
		"QUIT",
	} {
		if !strings.Contains(transcript, want) {
			t.Errorf("서버가 %q 를 받지 못했다\n전체:\n%s", want, transcript)
		}
	}
}

// fakeSMTP speaks just enough SMTP to accept one message and returns what the
// client sent. It advertises no extension, so STARTTLS and AUTH stay out of
// the path this test is about.
func fakeSMTP(conn net.Conn) string {
	var seen strings.Builder
	r := bufio.NewReader(conn)
	write := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }

	write("220 fake ESMTP")
	inData := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return seen.String()
		}
		line = strings.TrimRight(line, "\r\n")
		seen.WriteString(line + "\n")

		if inData {
			if line == "." {
				inData = false
				write("250 2.0.0 queued")
			}
			continue
		}
		switch {
		case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
			write("250 fake")
		case strings.HasPrefix(line, "MAIL FROM"), strings.HasPrefix(line, "RCPT TO"):
			write("250 2.0.0 OK")
		case line == "DATA":
			inData = true
			write("354 go ahead")
		case line == "QUIT":
			write("221 2.0.0 bye")
			return seen.String()
		default:
			write("500 5.5.1 unknown")
		}
	}
}
