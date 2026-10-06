package whatsapp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDirectTransportSend(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Write([]byte(`{"messaging_product":"whatsapp","contacts":[{"input":"16165550100","wa_id":"16165550100"}],"messages":[{"id":"wamid.abc"}]}`))
	}))
	defer srv.Close()

	tr := &DirectTransport{
		PhoneNumberID: "1234",
		AccessToken:   "secret",
		TemplateName:  "login_code",
		Languages:     []string{"en_US", "es_MX"},
		BaseURL:       srv.URL,
	}

	id, err := tr.SendAuthCode(context.Background(), AuthCode{To: "+16165550100", Code: "482913", Lang: "es"})
	if err != nil {
		t.Fatal(err)
	}
	if id != "wamid.abc" {
		t.Fatalf("Expected wamid.abc, got %q", id)
	}
	if gotPath != "/"+DefaultGraphVersion+"/1234/messages" || gotAuth != "Bearer secret" {
		t.Fatalf("Unexpected request %q %q", gotPath, gotAuth)
	}

	raw, _ := json.Marshal(gotBody)
	for _, part := range []string{
		`"to":"16165550100"`,
		`"name":"login_code"`,
		`"language":{"code":"es_MX"}`,
		`{"parameters":[{"text":"482913","type":"text"}],"type":"body"}`,
		`"sub_type":"url"`,
	} {
		if !strings.Contains(string(raw), part) {
			t.Errorf("Expected %s in %s", part, raw)
		}
	}
}

func TestDirectTransportErrors(t *testing.T) {
	scenarios := []struct {
		body     string
		expected string
	}{
		{`{"error":{"message":"Invalid OAuth access token","code":190}}`, ErrCodeUnauthorized},
		{`{"error":{"message":"Template name does not exist in the translation","code":132001}}`, ErrCodeTemplateUnavailable},
		{`{"error":{"message":"Recipient phone number not in allowed list","code":131030}}`, ErrCodeRecipientNotAllowed},
		{`{"error":{"message":"Rate limit hit","code":130429}}`, ErrCodeRateLimited},
		{`not json`, ErrCodeUnavailable},
	}

	for _, s := range scenarios {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(s.body))
		}))

		tr := &DirectTransport{PhoneNumberID: "1", AccessToken: "x", TemplateName: "t", BaseURL: srv.URL}
		_, err := tr.SendAuthCode(context.Background(), AuthCode{To: "+16165550100", Code: "1"})
		srv.Close()

		var te *TransportError
		if !errors.As(err, &te) || te.Code != s.expected {
			t.Errorf("[%s] Expected %s, got %v", s.body, s.expected, err)
		}
	}
}

func TestDirectTransportStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/1234"):
			w.Write([]byte(`{"display_phone_number":"+1 555-0100","verified_name":"Acme","quality_rating":"GREEN"}`))
		case strings.HasSuffix(r.URL.Path, "/999/message_templates"):
			if r.URL.Query().Get("name") != "login_code" {
				t.Errorf("Unexpected template filter %q", r.URL.RawQuery)
			}
			w.Write([]byte(`{"data":[
				{"name":"login_code","status":"APPROVED","language":"en_US","category":"AUTHENTICATION"},
				{"name":"login_code","status":"PENDING","language":"es_MX","category":"AUTHENTICATION"}
			]}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	tr := &DirectTransport{PhoneNumberID: "1234", WABAID: "999", AccessToken: "x", TemplateName: "login_code", BaseURL: srv.URL}

	status, err := tr.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.OK || status.Sender.DisplayName != "Acme" || len(status.Languages) != 1 || status.Languages[0] != "en_US" {
		t.Fatalf("Unexpected status %+v", status)
	}

	// no approved template
	tr.TemplateName = "missing"
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/message_templates") {
			w.Write([]byte(`{"data":[]}`))
			return
		}
		w.Write([]byte(`{"verified_name":"Acme"}`))
	})
	status, _ = tr.Status(context.Background())
	if status.OK {
		t.Fatalf("Expected not OK status, got %+v", status)
	}
}

func TestRelayTransport(t *testing.T) {
	var gotBody relayAuthCodeRequest
	var gotIdempotency string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer key123" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		switch r.URL.Path {
		case "/v1/auth-codes":
			gotIdempotency = r.Header.Get("Idempotency-Key")
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			if gotBody.To == "+19999999999" {
				w.WriteHeader(http.StatusTooManyRequests)
				w.Write([]byte(`{"code":"rate_limited","message":"slow down","retryAfter":30}`))
				return
			}
			w.Write([]byte(`{"messageId":"wamid.relay","sender":{"kind":"shared"}}`))
		case "/v1/status":
			w.Write([]byte(`{"ok":true,"sender":{"displayName":"VelaStack","kind":"shared"},"languages":["en_US"]}`))
		}
	}))
	defer srv.Close()

	tr := &RelayTransport{URL: srv.URL + "/v1/", APIKey: "key123"}

	id, err := tr.SendAuthCode(context.Background(), AuthCode{
		To:          "+16165550100",
		Code:        "123456",
		TTL:         5 * time.Minute,
		AppName:     "Acme",
		Collection:  "users",
		ChallengeID: "chal1",
	})
	if err != nil || id != "wamid.relay" {
		t.Fatalf("Expected wamid.relay, got %q (%v)", id, err)
	}
	if gotIdempotency != "chal1" || gotBody.TTLSeconds != 300 || gotBody.App.Name != "Acme" || gotBody.ClientIP != "" {
		t.Fatalf("Unexpected relay request %+v (%s)", gotBody, gotIdempotency)
	}

	_, err = tr.SendAuthCode(context.Background(), AuthCode{To: "+19999999999", Code: "1"})
	var te *TransportError
	if !errors.As(err, &te) || te.Code != ErrCodeRateLimited || te.RetryAfter != 30 {
		t.Fatalf("Expected rate_limited error, got %v", err)
	}

	status, _ := tr.Status(context.Background())
	if !status.OK || status.Sender.Kind != "shared" {
		t.Fatalf("Unexpected status %+v", status)
	}

	// wrong key → normalized from the status code
	tr.APIKey = "wrong"
	_, err = tr.SendAuthCode(context.Background(), AuthCode{To: "+16165550100", Code: "1"})
	if !errors.As(err, &te) || te.Code != ErrCodeUnauthorized {
		t.Fatalf("Expected unauthorized error, got %v", err)
	}
}
