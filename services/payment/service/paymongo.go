package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// PayMongo API base and event types we handle.
// https://docs.paymongo.com/reference/create_checkout_sessions_2
const (
	payMongoBaseURL = "https://api.paymongo.com"

	eventCheckoutSessionPaid   = "checkout_session.payment.paid"
	eventCheckoutSessionFailed = "checkout_session.payment.failed"
	eventPaymentPaid           = "payment.paid"
	eventPaymentFailed         = "payment.failed"

	capabilitiesCacheTTL = 10 * time.Minute
)

// knownPaymentMethods are the type strings PayMongo accepts. Which of them
// are usable still depends on the merchant account's enabled capabilities.
var knownPaymentMethods = map[string]bool{
	"card": true, "gcash": true, "paymaya": true, "grab_pay": true,
	"qrph": true, "dob": true, "dob_ubp": true, "brankas": true,
	"billease": true, "atome": true, "shopee_pay": true,
}

// safeDefaultMethods are enabled for virtually every PayMongo account.
var safeDefaultMethods = []string{"card", "gcash", "qrph"}

// PayMongoGateway is the concrete Gateway implementation for PayMongo.
// Swap it for another struct implementing Gateway to change providers;
// nothing else in the codebase names PayMongo.
type PayMongoGateway struct {
	secretKey     string
	webhookSecret string
	preferred     []string
	httpClient    *http.Client

	mu             sync.Mutex
	capabilityList []string
	capabilityAt   time.Time
}

func NewPayMongoGateway(secretKey, webhookSecret string, preferredMethods []string) *PayMongoGateway {
	return &PayMongoGateway{
		secretKey:     secretKey,
		webhookSecret: webhookSecret,
		preferred:     preferredMethods,
		httpClient:    &http.Client{Timeout: 30 * time.Second},
	}
}

// Name identifies the provider in stored records.
func (g *PayMongoGateway) Name() string { return "paymongo" }

// IsLive reports whether the configured secret key is a live key.
func (g *PayMongoGateway) IsLive() bool { return strings.HasPrefix(g.secretKey, "sk_live") }

// payMongoErrorDetail mirrors PayMongo's JSON:API error object so validation
// failures name the offending field instead of returning a generic message.
type payMongoErrorDetail struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
	Source struct {
		Pointer   string `json:"pointer"`
		Parameter string `json:"parameter"`
		Header    string `json:"header"`
	} `json:"source"`
}

func formatPayMongoErrors(status int, body []byte) error {
	var envelope struct {
		Errors []payMongoErrorDetail `json:"errors"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || len(envelope.Errors) == 0 {
		return fmt.Errorf("paymongo: request rejected (status %d): %s", status, truncate(string(body), 500))
	}
	parts := make([]string, 0, len(envelope.Errors))
	for _, e := range envelope.Errors {
		field := e.Source.Pointer
		if field == "" {
			field = e.Source.Parameter
		}
		switch {
		case field != "" && e.Detail != "":
			parts = append(parts, fmt.Sprintf("%s at %s", e.Detail, field))
		case e.Detail != "":
			parts = append(parts, e.Detail)
		case e.Code != "":
			parts = append(parts, e.Code)
		}
	}
	if len(parts) == 0 {
		return fmt.Errorf("paymongo: request rejected (status %d): %s", status, truncate(string(body), 500))
	}
	return fmt.Errorf("paymongo: %s", strings.Join(parts, "; "))
}

// resolvePaymentMethods picks payment method types the account can actually
// accept: the configured preference intersected with the account's
// capabilities, falling back to a safe set when capabilities are unavailable.
func (g *PayMongoGateway) resolvePaymentMethods(ctx context.Context) []string {
	if len(g.preferred) == 0 {
		return safeDefaultMethods
	}
	caps := g.fetchCapabilities(ctx)
	if len(caps) == 0 {
		// Cannot verify eligibility; send the preference and let PayMongo
		// answer with a precise error if a method is not enabled.
		return g.preferred
	}
	allowed := map[string]bool{}
	for _, c := range caps {
		allowed[c] = true
	}
	out := make([]string, 0, len(g.preferred))
	for _, m := range g.preferred {
		if allowed[m] {
			out = append(out, m)
		}
	}
	if len(out) == 0 {
		return safeDefaultMethods
	}
	return out
}

// fetchCapabilities returns the merchant's enabled payment method types.
// Responsible for caching; empty slice means "unknown".
func (g *PayMongoGateway) fetchCapabilities(ctx context.Context) []string {
	g.mu.Lock()
	if time.Since(g.capabilityAt) < capabilitiesCacheTTL && g.capabilityList != nil {
		cached := g.capabilityList
		g.mu.Unlock()
		return cached
	}
	g.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, payMongoBaseURL+"/v1/merchants/capabilities/payment_methods", nil)
	if err != nil {
		return nil
	}
	req.SetBasicAuth(g.secretKey, "")
	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}
	caps := extractPaymentMethods(body)
	if caps == nil {
		caps = []string{}
	}
	g.mu.Lock()
	g.capabilityList = caps
	g.capabilityAt = time.Now()
	g.mu.Unlock()
	return caps
}

// extractPaymentMethods walks an arbitrary JSON payload and collects every
// string that names a known payment method type, so we do not depend on the
// exact capabilities response shape.
func extractPaymentMethods(body []byte) []string {
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil
	}
	seen := map[string]bool{}
	var walk func(v any)
	walk = func(v any) {
		switch node := v.(type) {
		case string:
			if knownPaymentMethods[node] {
				seen[node] = true
			}
		case []any:
			for _, item := range node {
				walk(item)
			}
		case map[string]any:
			for _, item := range node {
				walk(item)
			}
		}
	}
	walk(payload)
	out := make([]string, 0, len(seen))
	for m := range seen {
		out = append(out, m)
	}
	return out
}

type checkoutSessionResponse struct {
	Data struct {
		ID         string `json:"id"`
		Attributes struct {
			CheckoutURL string `json:"checkout_url"`
		} `json:"attributes"`
	} `json:"data"`
}

// sanitizeReference keeps the merchant reference alphanumeric (PayMongo
// rejects some punctuation) and bounded in length.
func sanitizeReference(ref string) string {
	cleaned := make([]rune, 0, len(ref))
	for _, r := range ref {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			cleaned = append(cleaned, r)
		}
	}
	if len(cleaned) > 40 {
		cleaned = cleaned[:40]
	}
	if len(cleaned) == 0 {
		return "TOPUP" + time.Now().Format("20060102150405")
	}
	return string(cleaned)
}

// nonEmptyMetadata drops blank values, which some providers reject.
func nonEmptyMetadata(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		if strings.TrimSpace(v) == "" {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// CreateCheckoutSession creates a hosted checkout session and returns its id
// and customer-facing URL.
func (g *PayMongoGateway) CreateCheckoutSession(ctx context.Context, in CheckoutSessionInput) (*CheckoutSession, error) {
	if g.secretKey == "" {
		return nil, fmt.Errorf("paymongo: missing secret key (set PAYMONGO_SECRET_KEY)")
	}
	if in.AmountCents < 100 {
		return nil, fmt.Errorf("paymongo: amount must be at least 100 centavos (PHP 1.00)")
	}
	currency := in.Currency
	if currency == "" {
		currency = "PHP"
	}
	attributes := map[string]any{
		"line_items": []map[string]any{
			{
				"name":     in.Description,
				"amount":   in.AmountCents,
				"currency": currency,
				"quantity": 1,
			},
		},
		"payment_method_types": g.resolvePaymentMethods(ctx),
		"reference_number":     sanitizeReference(in.Reference),
		"send_email_receipt":   false,
	}
	if in.SuccessURL != "" {
		attributes["success_url"] = in.SuccessURL
	}
	if in.CancelURL != "" {
		attributes["cancel_url"] = in.CancelURL
	}
	if md := nonEmptyMetadata(in.Metadata); md != nil {
		attributes["metadata"] = md
	}

	body, err := json.Marshal(map[string]any{"data": map[string]any{"attributes": attributes}})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, payMongoBaseURL+"/v2/checkout_sessions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(g.secretKey, "")
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("paymongo: checkout session request failed: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, formatPayMongoErrors(resp.StatusCode, respBody)
	}
	var out checkoutSessionResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("paymongo: cannot parse checkout session response: %w", err)
	}
	if out.Data.ID == "" || out.Data.Attributes.CheckoutURL == "" {
		return nil, fmt.Errorf("paymongo: checkout session response missing id or url: %s", truncate(string(respBody), 500))
	}
	return &CheckoutSession{ID: out.Data.ID, CheckoutURL: out.Data.Attributes.CheckoutURL}, nil
}

// webhookEnvelope is the outer shape of every PayMongo webhook delivery.
type webhookEnvelope struct {
	Data struct {
		ID         string `json:"id"`
		Type       string `json:"type"`
		Attributes struct {
			Livemode bool             `json:"livemode"`
			Type     string           `json:"type"`
			Data     webhookEventData `json:"data"`
		} `json:"attributes"`
	} `json:"data"`
}

type webhookEventData struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Attributes struct {
		Amount          int                    `json:"amount"`
		ReferenceNumber string                 `json:"reference_number"`
		Metadata        map[string]interface{} `json:"metadata"`
	} `json:"attributes"`
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ParseWebhook verifies the Paymongo-Signature header against the raw body
// and normalizes the delivery into a provider-agnostic WebhookEvent.
// Unknown event types return (nil, nil): acknowledge, don't process.
func (g *PayMongoGateway) ParseWebhook(rawBody []byte, signatureHeader string) (*WebhookEvent, error) {
	if g.webhookSecret == "" {
		return nil, fmt.Errorf("paymongo: missing webhook secret (set PAYMONGO_WEBHOOK_SECRET)")
	}
	parts := map[string]string{}
	for _, kv := range strings.Split(signatureHeader, ",") {
		kv = strings.TrimSpace(kv)
		if i := strings.Index(kv, "="); i >= 0 {
			parts[kv[:i]] = kv[i+1:]
		}
	}
	timestamp, ok := parts["t"]
	if !ok || timestamp == "" {
		return nil, fmt.Errorf("paymongo: webhook missing timestamp")
	}
	mac := hmac.New(sha256.New, []byte(g.webhookSecret))
	mac.Write([]byte(timestamp + "." + string(rawBody)))
	expected := hex.EncodeToString(mac.Sum(nil))

	matchedLive := hmac.Equal([]byte(expected), []byte(parts["li"]))
	matchedTest := hmac.Equal([]byte(expected), []byte(parts["te"]))
	if !matchedLive && !matchedTest {
		return nil, fmt.Errorf("paymongo: webhook signature mismatch")
	}

	var env webhookEnvelope
	if err := json.Unmarshal(rawBody, &env); err != nil {
		return nil, fmt.Errorf("paymongo: cannot parse webhook payload: %w", err)
	}
	typ := env.Data.Attributes.Type
	if typ == "" {
		typ = env.Data.Type
	}
	switch typ {
	case eventCheckoutSessionPaid, eventPaymentPaid,
		eventCheckoutSessionFailed, eventPaymentFailed:
	default:
		return nil, nil
	}
	ev := &WebhookEvent{
		ID:          env.Data.ID,
		Type:        typ,
		SessionID:   env.Data.Attributes.Data.ID,
		Livemode:    env.Data.Attributes.Livemode,
		AmountCents: env.Data.Attributes.Data.Attributes.Amount,
		Reference:   env.Data.Attributes.Data.Attributes.ReferenceNumber,
	}
	if ref, ok := env.Data.Attributes.Data.Attributes.Metadata["reference_number"]; ok {
		if s, ok := ref.(string); ok {
			ev.Reference = s
		}
	}
	return ev, nil
}
