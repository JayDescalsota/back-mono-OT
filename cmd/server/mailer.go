package main

import (
	"github.com/clinicmanager/shared/tools"
)

// SMTPMailer sends real emails through the configured SMTP server.
// It is a tiny struct (no fields) so it can be passed around by value.
// Both the auth service (verification links) and the tenant service
// (staff invites) share this one mailer.
type SMTPMailer struct{}

// Send delivers one email. The SMTP settings come from the environment
// (SMTP_HOST, SMTP_PORT, SMTP_FROM, SMTP_PASSWORD).
func (SMTPMailer) Send(to, subject, body string) error {
	return tools.SendEmail(to, subject, body)
}
