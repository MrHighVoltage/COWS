package notifications

import (
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSMTPSendRejectsUnsafeHeadersBeforeConnecting(t *testing.T) {
	sender, err := NewSMTPSender(SMTPConfig{Host: "127.0.0.1", Port: 1, From: "cows@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ recipient, subject string }{
		{"user@example.test\r\nBcc: other@example.test", "Subject"},
		{"User <user@example.test>", "Subject"},
		{"user@example.test", "Subject\r\nBcc: other@example.test"},
	} {
		if err := sender.Send(context.Background(), test.recipient, test.subject, "Body"); err == nil || !strings.Contains(err.Error(), "invalid SMTP") {
			t.Fatalf("unsafe header reached network: %v", err)
		}
	}
}

func TestSMTPSendCancellationInterruptsStalledProtocol(t *testing.T) {
	for _, phase := range []string{"greeting", "command"} {
		t.Run(phase, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			accepted := make(chan net.Conn, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				if phase == "command" {
					_, _ = conn.Write([]byte("220 test SMTP\r\n"))
				}
				accepted <- conn
			}()
			host, port, _ := net.SplitHostPort(listener.Addr().String())
			portNumber, _ := strconv.Atoi(port)
			sender, err := NewSMTPSender(SMTPConfig{Host: host, Port: portNumber, From: "cows@example.test"})
			if err != nil {
				t.Fatal(err)
			}
			// No deadline: cancellation itself must close a blocked SMTP connection.
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- sender.Send(ctx, "user@example.test", "Subject", "Body") }()
			var conn net.Conn
			select {
			case conn = <-accepted:
			case <-time.After(2 * time.Second):
				t.Fatal("SMTP connection not accepted")
			}
			defer conn.Close()
			cancel()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("stalled send unexpectedly succeeded")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("SMTP send ignored cancellation")
			}
		})
	}
}
