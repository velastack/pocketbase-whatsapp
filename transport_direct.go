package whatsapp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// DefaultGraphVersion is the default Meta Graph API version used by [DirectTransport].
const DefaultGraphVersion = "v24.0"

// DefaultGraphURL is the default Meta Graph API base url.
const DefaultGraphURL = "https://graph.facebook.com"

// DirectTransport sends the codes with the WhatsApp Cloud API using
// an approved AUTHENTICATION template with a copy code button.
type DirectTransport struct {
	// PhoneNumberID is the Cloud API business phone number id (not the phone number itself).
	PhoneNumberID string

	// WABAID is the WhatsApp Business Account id (used only for the status checks).
	WABAID string

	// AccessToken is a system user (or business integration) access token
	// with the whatsapp_business_messaging permission.
	AccessToken string

	// TemplateName is the name of the approved authentication template.
	TemplateName string

	// Languages is the list with the template approved languages (eg. "en_US", "es_MX").
	// The first one is used as fallback.
	Languages []string

	// GraphVersion is the Graph API version (default to [DefaultGraphVersion]).
	GraphVersion string

	// BaseURL is the Graph API base url (default to [DefaultGraphURL]).
	BaseURL string

	// HTTPClient is the client used to send the requests (default to [http.DefaultClient]).
	HTTPClient *http.Client
}

func (t *DirectTransport) endpoint(parts ...string) string {
	base := t.BaseURL
	if base == "" {
		base = DefaultGraphURL
	}

	version := t.GraphVersion
	if version == "" {
		version = DefaultGraphVersion
	}

	escaped := make([]string, len(parts))
	for i, p := range parts {
		escaped[i] = url.PathEscape(p)
	}

	return strings.TrimRight(base, "/") + "/" + version + "/" + strings.Join(escaped, "/")
}

func (t *DirectTransport) client() *http.Client {
	if t.HTTPClient != nil {
		return t.HTTPClient
	}
	return http.DefaultClient
}

// SendAuthCode implements [Transport].
func (t *DirectTransport) SendAuthCode(ctx context.Context, m AuthCode) (string, error) {
	if t.PhoneNumberID == "" || t.AccessToken == "" || t.TemplateName == "" {
		return "", &TransportError{Code: ErrCodeUnavailable, Message: "the WhatsApp Cloud API transport is not fully configured"}
	}

	// the code is both the body parameter and the copy code button parameter
	payload := map[string]any{
		"messaging_product": "whatsapp",
		"recipient_type":    "individual",
		"to":                strings.TrimPrefix(m.To, "+"),
		"type":              "template",
		"template": map[string]any{
			"name":     t.TemplateName,
			"language": map[string]any{"code": pickLanguage(m.Lang, t.Languages, "en_US")},
			"components": []map[string]any{
				{
					"type":       "body",
					"parameters": []map[string]any{{"type": "text", "text": m.Code}},
				},
				{
					"type":       "button",
					"sub_type":   "url",
					"index":      "0",
					"parameters": []map[string]any{{"type": "text", "text": m.Code}},
				},
			},
		},
	}

	result := struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
	}{}

	if err := t.do(ctx, http.MethodPost, t.endpoint(t.PhoneNumberID, "messages"), payload, &result); err != nil {
		return "", err
	}

	if len(result.Messages) == 0 {
		return "", &TransportError{Code: ErrCodeUnavailable, Message: "missing message id in the Cloud API response"}
	}

	return result.Messages[0].ID, nil
}

// Status implements [StatusChecker].
//
// It checks the sender phone number and the configured template approval status.
func (t *DirectTransport) Status(ctx context.Context) (*TransportStatus, error) {
	status := &TransportStatus{Languages: t.Languages, Details: map[string]any{}}

	if t.PhoneNumberID == "" || t.AccessToken == "" || t.TemplateName == "" {
		status.Message = "Phone number ID, access token and template name are required."
		return status, nil
	}

	phone := struct {
		DisplayPhoneNumber string `json:"display_phone_number"`
		VerifiedName       string `json:"verified_name"`
		QualityRating      string `json:"quality_rating"`
	}{}
	if err := t.do(ctx, http.MethodGet, t.endpoint(t.PhoneNumberID)+"?fields=display_phone_number,verified_name,quality_rating", nil, &phone); err != nil {
		status.Message = "Failed to load the sender phone number: " + err.Error()
		return status, nil
	}
	status.Sender = &SenderInfo{Phone: phone.DisplayPhoneNumber, DisplayName: phone.VerifiedName, Kind: "own"}
	status.Details["qualityRating"] = phone.QualityRating

	if t.WABAID == "" {
		status.OK = true
		status.Message = "The template approval status is not checked because the WhatsApp Business Account ID is not set."
		return status, nil
	}

	templates := struct {
		Data []struct {
			Name     string `json:"name"`
			Status   string `json:"status"`
			Language string `json:"language"`
			Category string `json:"category"`
		} `json:"data"`
	}{}
	query := url.Values{}
	query.Set("name", t.TemplateName)
	query.Set("fields", "name,status,language,category")
	query.Set("limit", "100")
	if err := t.do(ctx, http.MethodGet, t.endpoint(t.WABAID, "message_templates")+"?"+query.Encode(), nil, &templates); err != nil {
		status.Message = "Failed to load the message templates: " + err.Error()
		return status, nil
	}

	approved := []string{}
	templateStatuses := map[string]string{}
	for _, tpl := range templates.Data {
		if tpl.Name != t.TemplateName {
			continue
		}
		templateStatuses[tpl.Language] = tpl.Status
		if tpl.Status == "APPROVED" && tpl.Category == "AUTHENTICATION" {
			approved = append(approved, tpl.Language)
		}
	}
	status.Details["templates"] = templateStatuses

	if len(approved) == 0 {
		status.Message = fmt.Sprintf("No APPROVED authentication template named %q was found.", t.TemplateName)
		return status, nil
	}

	if len(t.Languages) == 0 {
		status.Languages = approved
	}

	status.OK = true

	return status, nil
}

func (t *DirectTransport) do(ctx context.Context, method string, endpoint string, body any, dst any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+t.AccessToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
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
		return graphError(res.StatusCode, raw)
	}

	if dst != nil {
		if err := json.Unmarshal(raw, dst); err != nil {
			return &TransportError{Code: ErrCodeUnavailable, Message: "invalid Cloud API response: " + err.Error()}
		}
	}

	return nil
}

// graphError normalizes a Graph API error response.
func graphError(status int, raw []byte) error {
	data := struct {
		Error struct {
			Message   string `json:"message"`
			Code      int    `json:"code"`
			ErrorData struct {
				Details string `json:"details"`
			} `json:"error_data"`
		} `json:"error"`
	}{}

	if err := json.Unmarshal(raw, &data); err != nil || data.Error.Code == 0 {
		return &TransportError{
			Code:    ErrCodeUnavailable,
			Message: fmt.Sprintf("unexpected Cloud API response (status %d)", status),
		}
	}

	msg := data.Error.Message
	if data.Error.ErrorData.Details != "" {
		msg += " (" + data.Error.ErrorData.Details + ")"
	}
	msg = fmt.Sprintf("[%d] %s", data.Error.Code, msg)

	return &TransportError{Code: graphErrorCode(data.Error.Code), Message: msg}
}

// graphErrorCode maps the Cloud API error codes to a [TransportError] code.
//
// See https://developers.facebook.com/docs/whatsapp/cloud-api/support/error-codes
func graphErrorCode(code int) string {
	switch code {
	case 0, 3, 10, 190, 200, 131005:
		return ErrCodeUnauthorized
	case 4, 80007, 130429, 131048, 131056:
		return ErrCodeRateLimited
	case 131042:
		return ErrCodeBudgetExceeded
	case 131030:
		return ErrCodeRecipientNotAllowed
	case 131009, 131021, 131026, 100:
		return ErrCodeInvalidPhone
	case 132000, 132001, 132005, 132007, 132012, 132015, 132016:
		return ErrCodeTemplateUnavailable
	default:
		return ErrCodeUnavailable
	}
}
