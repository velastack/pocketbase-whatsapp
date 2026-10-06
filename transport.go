package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// AuthCode is a single one-time code delivery request.
type AuthCode struct {
	// To is the recipient phone number in E.164 format (eg. "+16165550123").
	To string

	// Code is the plain one-time code.
	Code string

	// Lang is the preferred template language (eg. "es_MX").
	// It is up to the transport to fallback to a supported one.
	Lang string

	// TTL is the code validity duration.
	TTL time.Duration

	// AppName and AppURL are the app meta settings
	// (WhatsApp authentication templates have fixed text so
	// they are informative and used only by relays).
	AppName string
	AppURL  string

	// Collection is the name of the auth collection the code is for.
	Collection string

	// ClientIP is the end user IP (only set when explicitly enabled).
	ClientIP string

	// ChallengeID is the unique id of the code challenge
	// (could be used as idempotency key).
	ChallengeID string
}

// Transport delivers one-time auth codes to WhatsApp users.
type Transport interface {
	// SendAuthCode sends the provided auth code and returns the
	// provider message id on success.
	SendAuthCode(ctx context.Context, m AuthCode) (messageID string, err error)
}

// TransportStatus describes the readiness of a transport.
type TransportStatus struct {
	OK        bool           `json:"ok"`
	Message   string         `json:"message,omitempty"`
	Sender    *SenderInfo    `json:"sender,omitempty"`
	Languages []string       `json:"languages,omitempty"`
	Details   map[string]any `json:"details,omitempty"`
}

// SenderInfo describes the WhatsApp number that sends the codes.
type SenderInfo struct {
	Phone       string `json:"phone,omitempty"`
	DisplayName string `json:"displayName,omitempty"`

	// Kind is "shared" (eg. the VelaStack shared sender) or "own".
	Kind string `json:"kind,omitempty"`
}

// StatusChecker is an optional interface that could be implemented
// by a Transport to report its configuration status in the superuser UI.
type StatusChecker interface {
	Status(ctx context.Context) (*TransportStatus, error)
}

// Transport error codes.
const (
	ErrCodeUnauthorized        = "unauthorized"
	ErrCodeInvalidPhone        = "invalid_phone"
	ErrCodeCountryNotAllowed   = "country_not_allowed"
	ErrCodeRateLimited         = "rate_limited"
	ErrCodeBudgetExceeded      = "budget_exceeded"
	ErrCodeTemplateUnavailable = "template_unavailable"
	ErrCodeRecipientNotAllowed = "recipient_not_allowed"
	ErrCodeUnavailable         = "unavailable"
)

// TransportError is a normalized transport delivery error.
type TransportError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	RetryAfter int    `json:"retryAfter,omitempty"` // in seconds
}

func (e *TransportError) Error() string {
	if e.Message == "" {
		return "whatsapp transport error: " + e.Code
	}
	return fmt.Sprintf("whatsapp transport error (%s): %s", e.Code, e.Message)
}

func transportErrorCode(err error) string {
	var te *TransportError
	if errors.As(err, &te) {
		return te.Code
	}
	return ""
}

// pickLanguage returns the best matching language from the supported list
// (exact match, then same base language, then the first supported one).
func pickLanguage(requested string, supported []string, fallback string) string {
	if len(supported) == 0 {
		if requested != "" {
			return requested
		}
		return fallback
	}

	requested = strings.ReplaceAll(strings.TrimSpace(requested), "-", "_")
	if requested != "" {
		for _, l := range supported {
			if strings.EqualFold(l, requested) {
				return l
			}
		}

		base, _, _ := strings.Cut(requested, "_")
		for _, l := range supported {
			lb, _, _ := strings.Cut(l, "_")
			if strings.EqualFold(lb, base) {
				return l
			}
		}
	}

	return supported[0]
}
