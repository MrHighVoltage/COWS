package notifications

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"time"

	"github.com/cows-project/cows/internal/domain"
)

type SMTPConfig struct {
	Host       string
	Port       int
	From       string
	Username   string
	Password   string
	RequireTLS bool
}

type SMTPSender struct {
	config SMTPConfig
}

func NewSMTPSender(config SMTPConfig) (*SMTPSender, error) {
	if config.Host == "" || config.Port < 1 || config.Port > 65535 {
		return nil, errors.New("SMTP host and port are required")
	}
	parsed, err := mail.ParseAddress(config.From)
	if err != nil || parsed.Address != config.From {
		return nil, errors.New("SMTP sender address is invalid")
	}
	if (config.Username == "") != (config.Password == "") {
		return nil, errors.New("SMTP username and password must be provided together")
	}
	return &SMTPSender{config: config}, nil
}

func (s *SMTPSender) Send(ctx context.Context, notification domain.EmailNotification) error {
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	connection, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(s.config.Host, strconv.Itoa(s.config.Port)))
	if err != nil {
		return fmt.Errorf("dial SMTP server: %w", err)
	}
	client, err := smtp.NewClient(connection, s.config.Host)
	if err != nil {
		connection.Close()
		return fmt.Errorf("create SMTP client: %w", err)
	}
	defer client.Close()
	if supported, _ := client.Extension("STARTTLS"); supported {
		if err := client.StartTLS(&tls.Config{ServerName: s.config.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("start SMTP TLS: %w", err)
		}
	} else if s.config.RequireTLS {
		return errors.New("SMTP server does not support STARTTLS")
	}
	if s.config.Username != "" {
		if err := client.Auth(smtp.PlainAuth("", s.config.Username, s.config.Password, s.config.Host)); err != nil {
			return fmt.Errorf("authenticate SMTP client: %w", err)
		}
	}
	if err := client.Mail(s.config.From); err != nil {
		return fmt.Errorf("set SMTP sender: %w", err)
	}
	if err := client.Rcpt(notification.Recipient); err != nil {
		return fmt.Errorf("set SMTP recipient: %w", err)
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("start SMTP message: %w", err)
	}
	message := "From: " + s.config.From + "\r\nTo: " + notification.Recipient + "\r\nSubject: " + notification.Subject + "\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n" + notification.Body + "\r\n"
	if _, err := io.WriteString(writer, message); err != nil {
		_ = writer.Close()
		return fmt.Errorf("write SMTP message: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("finish SMTP message: %w", err)
	}
	if err := client.Quit(); err != nil {
		return fmt.Errorf("close SMTP session: %w", err)
	}
	return nil
}
