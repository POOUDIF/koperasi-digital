package service

import (
	"context"
	"fmt"
	"log"
	"net/smtp"
)

// EmailService mendefinisikan interface untuk pengiriman email.
type EmailService interface {
	SendOTPEmail(ctx context.Context, toEmail, otp string) error
}

type smtpEmailService struct {
	host      string
	port      string
	user      string
	password  string
	fromEmail string
}

// NewEmailService membuat instance layanan pengiriman email via SMTP.
func NewEmailService(host, port, user, password, fromEmail string) EmailService {
	return &smtpEmailService{
		host:      host,
		port:      port,
		user:      user,
		password:  password,
		fromEmail: fromEmail,
	}
}

// SendOTPEmail mengirimkan kode OTP ke email tujuan.
func (s *smtpEmailService) SendOTPEmail(ctx context.Context, toEmail, otp string) error {
	// Jika belum diset (misal saat development/local tanpa internet), kita log saja.
	if s.host == "" || s.port == "" {
		log.Printf("[EMAIL SIMULATION] Mengirim OTP %s ke %s", otp, toEmail)
		return nil
	}

	auth := smtp.PlainAuth("", s.user, s.password, s.host)

	subject := "Subject: Kode Verifikasi Koperasi Digital\n"
	mime := "MIME-version: 1.0;\nContent-Type: text/html; charset=\"UTF-8\";\n\n"
	body := fmt.Sprintf(`
		<h2>Selamat Datang di Koperasi Digital</h2>
		<p>Berikut adalah kode verifikasi OTP Anda:</p>
		<h1 style="color: blue;">%s</h1>
		<p>Kode ini berlaku selama 15 menit. Jangan berikan kode ini kepada siapa pun.</p>
	`, otp)

	msg := []byte(subject + mime + body)
	addr := fmt.Sprintf("%s:%s", s.host, s.port)

	// SendMail adalah fungsi blocking yang mencoba koneksi SMTP
	if err := smtp.SendMail(addr, auth, s.fromEmail, []string{toEmail}, msg); err != nil {
		return fmt.Errorf("gagal mengirim email OTP: %w", err)
	}

	return nil
}
