package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
)

func TestSanitizeReference(t *testing.T) {
	got := sanitizeReference("7f1c0d1e-4b5a-4c9d-8e1f-2a3b4c5d6e7f")
	if strings.ContainsAny(got, "-_ ") {
		t.Fatalf("reference must be alphanumeric, got %q", got)
	}
	if len(got) > 40 {
		t.Fatalf("reference too long: %d", len(got))
	}
	if sanitizeReference("") == "" {
		t.Fatal("empty reference must fall back to a generated value")
	}
}

func TestNonEmptyMetadata(t *testing.T) {
	if got := nonEmptyMetadata(map[string]string{"a": "1", "b": "  "}); len(got) != 1 || got["a"] != "1" {
		t.Fatalf("blank metadata values must be dropped: %v", got)
	}
	if got := nonEmptyMetadata(map[string]string{"a": ""}); got != nil {
		t.Fatalf("all-blank metadata must be omitted, got %v", got)
	}
}

func TestFormatPayMongoErrors(t *testing.T) {
	body := []byte(`{"errors":[{"code":"invalid_request_body","detail":"Payment method type is invalid","source":{"pointer":"/data/attributes/payment_method_types"}}]}`)
	err := formatPayMongoErrors(400, body)
	if err == nil {
		t.Fatal("expected an error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "Payment method type is invalid") || !strings.Contains(msg, "/data/attributes/payment_method_types") {
		t.Fatalf("error must include detail and pointer, got %q", msg)
	}
}

func TestFormatPayMongoErrorsFallback(t *testing.T) {
	err := formatPayMongoErrors(500, []byte("<html>oops</html>"))
	if err == nil || !strings.Contains(err.Error(), "status 500") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExtractPaymentMethods(t *testing.T) {
	body := []byte(`{"data":[{"type":"payment_method","attributes":{"type":"card"}},{"attributes":{"name":"gcash"}},"qrph","unknown"]}`)
	got := extractPaymentMethods(body)
	found := map[string]bool{}
	for _, m := range got {
		found[m] = true
	}
	for _, want := range []string{"card", "gcash", "qrph"} {
		if !found[want] {
			t.Fatalf("missing %q in %v", want, got)
		}
	}
	if found["unknown"] {
		t.Fatalf("unknown method leaked into capabilities: %v", got)
	}
}

func TestParseWebhookVerifiesSignature(t *testing.T) {
	const secret = "whsec_test"
	g := NewPayMongoGateway("sk_test_x", secret, nil)

	body := []byte(`{"data":{"id":"evt_1","type":"event","attributes":{"livemode":false,"type":"checkout_session.payment.paid","data":{"id":"cs_123","type":"checkout_session","attributes":{"amount":50000,"reference_number":"REF1","metadata":{"topup_id":"t1"}}}}}}`)
	timestamp := "1496734173"
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "." + string(body)))
	sig := hex.EncodeToString(mac.Sum(nil))
	header := fmt.Sprintf("t=%s,te=%s,li=", timestamp, sig)

	ev, err := g.ParseWebhook(body, header)
	if err != nil {
		t.Fatalf("ParseWebhook: %v", err)
	}
	if ev == nil {
		t.Fatal("expected an event")
	}
	if ev.Type != eventCheckoutSessionPaid || ev.SessionID != "cs_123" || ev.AmountCents != 50000 {
		t.Fatalf("unexpected event: %+v", ev)
	}

	if _, err := g.ParseWebhook(body, fmt.Sprintf("t=%s,te=%s,li=", timestamp, "deadbeef")); err == nil {
		t.Fatal("expected signature mismatch")
	}
}

func TestParseWebhookIgnoresUnknownEvents(t *testing.T) {
	const secret = "whsec_test"
	g := NewPayMongoGateway("sk_test_x", secret, nil)
	body := []byte(`{"data":{"id":"evt_2","attributes":{"type":"some.other.event","data":{"id":"x"}}}}`)
	timestamp := "1496734173"
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "." + string(body)))
	header := fmt.Sprintf("t=%s,te=%s", timestamp, hex.EncodeToString(mac.Sum(nil)))
	ev, err := g.ParseWebhook(body, header)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev != nil {
		t.Fatalf("unknown events must be ignored, got %+v", ev)
	}
}
