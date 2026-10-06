package whatsapp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// DefaultVelastackRelayURL is the default VelaStack relay base url.
const DefaultVelastackRelayURL = "https://velastack.dev/api/whatsapp/v1"

// RelayTransport delivers the codes through an HTTP relay
// that implements the relay contract (VelaStack is the default implementation).
//
// The relay owns the WhatsApp sender (eg. a shared verified number or
// the customer own number connected via Embedded Signup) so the instance
// never needs Meta credentials.
//
// Contract:
//
//	POST {URL}/auth-codes
//	Authorization: Bearer {APIKey}
//	Idempotency-Key: {challengeId}
//	{"to","code","lang","ttlSeconds","collection","app":{"name","url"},"clientIp"}
//
//	200 {"messageId", "sender": {"phone","displayName","kind"}}
//	4xx/5xx {"code","message","retryAfter"}
//
//	GET {URL}/status → {"ok","message","sender","languages"}
type RelayTransport struct {
	// URL is the relay base url (default to [DefaultVelastackRelayURL]).
	URL string

	// APIKey is the relay API key.
	APIKey string

	// HTTPClient is the client used to send the requests (default to [http.DefaultClient]).
	HTTPClient *http.Client
}

type relayAuthCodeRequest struct {
	To         string       `json:"to"`
	Code       string       `json:"code"`
	Lang       string       `json:"lang,omitempty"`
	TTLSeconds int          `json:"ttlSeconds"`
	Collection string       `json:"collection,omitempty"`
	App        relayAppInfo `json:"app"`
	ClientIP   string       `json:"clientIp,omitempty"`
}

type relayAppInfo struct {
	Name string `json:"name,omitempty"`
	URL  string `json:"url,omitempty"`
}

func (t *RelayTransport) baseURL() string {
	if t.URL == "" {
		return DefaultVelastackRelayURL
	}
	return strings.TrimRight(t.URL, "/")
}

func (t *RelayTransport) client() *http.Client {
	if t.HTTPClient != nil {
		return t.HTTPClient
	}
	return http.DefaultClient
}

// SendAuthCode implements [Transport].
func (t *RelayTransport) SendAuthCode(ctx context.Context, m AuthCode) (string, error) {
	if t.APIKey == "" {
		return "", &TransportError{Code: ErrCodeUnauthorized, Message: "missing relay API key"}
	}

	body := relayAuthCodeRequest{
		To:         m.To,
		Code:       m.Code,
		Lang:       m.Lang,
		TTLSeconds: int(m.TTL.Seconds()),
		Collection: m.Collection,
		App:        relayAppInfo{Name: m.AppName, URL: m.AppURL},
		ClientIP:   m.ClientIP,
	}

	result := struct {
		MessageID string `json:"messageId"`
	}{}

	headers := map[string]string{}
	if m.ChallengeID != "" {
		headers["Idempotency-Key"] = m.ChallengeID
	}

	if err := t.do(ctx, http.MethodPost, "/auth-codes", headers, body, &result); err != nil {
		return "", err
	}

	return result.MessageID, nil
}

// Status implements [StatusChecker].
func (t *RelayTransport) Status(ctx context.Context) (*TransportStatus, error) {
	if t.APIKey == "" {
		return &TransportStatus{Message: "API key is required."}, nil
	}

	status := &TransportStatus{}
	if err := t.do(ctx, http.MethodGet, "/status", nil, nil, status); err != nil {
		return &TransportStatus{Message: "Failed to reach the relay: " + err.Error()}, nil
	}

	return status, nil
}

func (t *RelayTransport) do(ctx context.Context, method, path string, headers map[string]string, body any, dst any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, t.baseURL()+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+t.APIKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	res, err := t.client().Do(req)
	if err != nil {
		return &TransportError{Code: ErrCodeUnavailable, Message: err.Error()}
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return &TransportError{Code: ErrCodeUnavailable, Message: err.Error()}
	}

	if res.StatusCode >= 400 {
		te := &TransportError{}
		if json.Unmarshal(raw, te) != nil || te.Code == "" {
			te.Code = relayStatusCode(res.StatusCode)
			te.Message = fmt.Sprintf("relay responded with status %d", res.StatusCode)
		}
		return te
	}

	if dst != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, dst); err != nil {
			return &TransportError{Code: ErrCodeUnavailable, Message: "invalid relay response: " + err.Error()}
		}
	}

	return nil
}

func relayStatusCode(status int) string {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrCodeUnauthorized
	case http.StatusTooManyRequests:
		return ErrCodeRateLimited
	case http.StatusPaymentRequired:
		return ErrCodeBudgetExceeded
	default:
		return ErrCodeUnavailable
	}
}
