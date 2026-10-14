package whatsapp

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testRelayKey     = "pk_testkey.secret"
	testClosedPhone  = "+5215550000099" // the fake VelaStack answers window_closed
	testContactPhone = "+447700900123"
)

// fakeVelastack is a stand-in for the VelaStack relay.
type fakeVelastack struct {
	mu       sync.Mutex
	server   *httptest.Server
	secret   string
	webhooks []map[string]any
	sent     []map[string]any
	read     []string
	inbound  []map[string]any // what GET /messages?direction=in lists
	statuses map[string]string
}

func newFakeVelastack(t *testing.T) *fakeVelastack {
	t.Helper()

	f := &fakeVelastack{statuses: map[string]string{}}
	mux := http.NewServeMux()

	mux.HandleFunc("PUT /webhook", func(w http.ResponseWriter, r *http.Request) {
		body := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.webhooks = append(f.webhooks, body)
		result := map[string]any{"url": body["url"]}
		if f.secret == "" || body["rotate"] == true {
			f.secret = fmt.Sprintf("whsec_test%d", len(f.webhooks))
			result["secret"] = f.secret
		}
		_ = json.NewEncoder(w).Encode(result)
	})

	mux.HandleFunc("GET /messages", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		start := 0
		if after := r.URL.Query().Get("after"); after != "" {
			start, _ = strconv.Atoi(strings.TrimPrefix(after, "c"))
		}
		data := f.inbound[min(start, len(f.inbound)):]
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "next": "c" + strconv.Itoa(len(f.inbound))})
	})

	mux.HandleFunc("POST /messages", func(w http.ResponseWriter, r *http.Request) {
		body := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["to"] == testClosedPhone && body["type"] == "text" {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "window_closed", "message": "closed"})
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.sent = append(f.sent, body)
		id := fmt.Sprintf("wamid.OUT%d", len(f.sent))
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{"messageId": id})
	})

	mux.HandleFunc("GET /messages/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		status, ok := f.statuses[r.PathValue("id")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"messageId": r.PathValue("id"), "status": status})
	})

	mux.HandleFunc("POST /messages/{id}/read", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.read = append(f.read, r.PathValue("id"))
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("GET /templates", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{
			{"name": "order_shipped", "language": "en_US", "status": "APPROVED", "category": "UTILITY"},
			{"name": "velastack_auth_code", "language": "en_US", "status": "APPROVED", "category": "AUTHENTICATION"},
			{"name": "draft", "language": "en_US", "status": "PENDING", "category": "UTILITY"},
		}})
	})

	mux.HandleFunc("GET /media/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") == "expired" {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "not_found", "message": "gone"})
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("jpeg-bytes"))
	})

	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testRelayKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(f.server.Close)

	return f
}

// newIncomingEnv is a test env sending through the fake VelaStack, with the
// app url set to appURL.
func newIncomingEnv(t *testing.T, appURL string) (*testEnv, *fakeVelastack) {
	t.Helper()

	env := newTestEnv(t, Config{}, defaultTestConfig())
	fake := newFakeVelastack(t)

	env.app.Settings().Meta.AppURL = appURL
	if err := env.app.Save(env.app.Settings()); err != nil {
		t.Fatal(err)
	}

	res := env.do(http.MethodPatch, "/api/whatsapp/settings", map[string]any{
		"mode":      ModeVelastack,
		"velastack": map[string]any{"apiKey": testRelayKey, "relayURL": fake.server.URL},
	}, env.superuserToken)
	if res.status != http.StatusOK {
		t.Fatalf("Failed to save the settings: %d %s", res.status, res.raw)
	}

	return env, fake
}

func sign(secret string, body []byte, at time.Time) string {
	ts := strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	return "t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

func (env *testEnv) postEvent(secret string, event map[string]any) testResponse {
	env.t.Helper()

	body, _ := json.Marshal(event)
	req := httptest.NewRequest(http.MethodPost, webhookPath, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-VelaStack-Signature", sign(secret, body, time.Now()))
	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)

	return testResponse{status: rec.Code, raw: rec.Body.String()}
}

func receivedEvent(id, messageID, from, text string) map[string]any {
	return map[string]any{
		"id":   id,
		"type": "message.received",
		"data": map[string]any{
			"messageId": messageID,
			"from":      strings.TrimPrefix(from, "+"),
			"name":      "Ana",
			"type":      "text",
			"timestamp": strconv.FormatInt(time.Now().Unix(), 10),
			"text":      text,
			"message":   map[string]any{"id": messageID, "type": "text", "text": map[string]any{"body": text}},
		},
	}
}

func TestVerifySignature(t *testing.T) {
	body := []byte(`{"id":"evt_1"}`)
	now := time.Now()

	scenarios := []struct {
		name   string
		header string
		secret string
		ok     bool
	}{
		{"valid", sign("whsec_a", body, now), "whsec_a", true},
		{"wrong secret", sign("whsec_b", body, now), "whsec_a", false},
		{"stale", sign("whsec_a", body, now.Add(-10*time.Minute)), "whsec_a", false},
		{"future", sign("whsec_a", body, now.Add(10*time.Minute)), "whsec_a", false},
		{"no secret", sign("", body, now), "", false},
		{"garbage", "v1=abc", "whsec_a", false},
	}

	for _, s := range scenarios {
		t.Run(s.name, func(t *testing.T) {
			err := verifySignature(s.header, body, s.secret, now)
			if (err == nil) != s.ok {
				t.Fatalf("Expected ok=%v, got %v", s.ok, err)
			}
		})
	}
}

func TestPublicHTTPS(t *testing.T) {
	scenarios := map[string]bool{
		"https://example.com":        true,
		"https://app.example.com/pb": true,
		"http://example.com":         false,
		"https://localhost:8090":     false,
		"https://127.0.0.1":          false,
		"https://10.0.0.5":           false,
		"https://myapp.test":         false,
		"https://intranet":           false,
		"":                           false,
	}
	for url, expected := range scenarios {
		if got := publicHTTPS(url); got != expected {
			t.Errorf("publicHTTPS(%q) = %v, expected %v", url, got, expected)
		}
	}
}

func TestIncomingWebhook(t *testing.T) {
	env, fake := newIncomingEnv(t, "https://app.example.com")

	// saving the settings registered the webhook
	if len(fake.webhooks) != 1 || fake.webhooks[0]["url"] != "https://app.example.com"+webhookPath {
		t.Fatalf("Expected the webhook to be registered, got %v", fake.webhooks)
	}
	state, _ := env.plugin.loadIncomingState(env.app)
	if state.Mode != IncomingWebhook || state.Secret != fake.secret {
		t.Fatalf("Expected webhook mode with the secret, got %+v", state)
	}

	// the member's number is verified, so the contact links to them
	env.member.Set("phoneVerified", true)
	if err := env.app.Save(env.member); err != nil {
		t.Fatal(err)
	}

	if res := env.postEvent("whsec_wrong", receivedEvent("evt_1", "wamid.IN1", testMemberPhone, "Hola")); res.status != http.StatusUnauthorized {
		t.Fatalf("Expected 401 for a bad signature, got %d", res.status)
	}

	for range 2 { // VelaStack retries deliver the same event again
		if res := env.postEvent(fake.secret, receivedEvent("evt_1", "wamid.IN1", testMemberPhone, "Hola")); res.status != http.StatusNoContent {
			t.Fatalf("Expected 204, got %d: %s", res.status, res.raw)
		}
	}

	messages, _ := env.app.FindAllRecords(CollectionNameMessages)
	if len(messages) != 1 || messages[0].GetString("text") != "Hola" || messages[0].GetString("eventId") != "evt_1" {
		t.Fatalf("Expected one stored message, got %v", messages)
	}
	contact, err := env.app.FindFirstRecordByData(CollectionNameContacts, "phone", testMemberPhone)
	if err != nil {
		t.Fatal(err)
	}
	if contact.GetString("name") != "Ana" || contact.GetInt("unread") != 1 || contact.GetString("lastMessage") != "Hola" {
		t.Fatalf("Unexpected contact %v", contact)
	}
	if contact.GetString("user") != env.member.Id || contact.GetString("userCollection") != env.members.Id {
		t.Fatalf("Expected the contact to link to the member, got %v", contact)
	}

	if res := env.postEvent(fake.secret, map[string]any{"id": "evt_t", "type": "webhook.test", "data": map[string]any{}}); res.status != http.StatusNoContent {
		t.Fatalf("Expected 204 for a test event, got %d", res.status)
	}
}

func TestIncomingStatus(t *testing.T) {
	env, fake := newIncomingEnv(t, "https://app.example.com")

	env.postEvent(fake.secret, receivedEvent("evt_1", "wamid.IN1", testContactPhone, "Hola"))
	contact, _ := env.app.FindFirstRecordByData(CollectionNameContacts, "phone", testContactPhone)
	res := env.do(http.MethodPost, "/api/whatsapp/inbox/"+contact.Id, map[string]any{"text": "Hi Ana"}, env.superuserToken)
	if res.status != http.StatusOK {
		t.Fatalf("Expected the reply to send, got %d: %s", res.status, res.raw)
	}
	messageID := res.body["messageId"].(string)

	status := func(id, s string) {
		env.postEvent(fake.secret, map[string]any{
			"id": id, "type": "message.status",
			"data": map[string]any{"messageId": messageID, "status": s},
		})
	}
	status("evt_s1", "read")
	status("evt_s2", "delivered") // late, must not undo read

	sent, _ := env.app.FindFirstRecordByData(CollectionNameMessages, "messageId", messageID)
	if sent.GetString("status") != "read" {
		t.Fatalf("Expected read, got %q", sent.GetString("status"))
	}
}

func TestIncomingPolling(t *testing.T) {
	env, fake := newIncomingEnv(t, "http://localhost:8090")

	if len(fake.webhooks) != 0 {
		t.Fatalf("Expected no webhook for a local app, got %v", fake.webhooks)
	}

	at := strconv.FormatInt(time.Now().Unix(), 10)
	fake.inbound = []map[string]any{
		{"messageId": "wamid.P1", "from": "447700900123", "type": "text", "text": "One", "message": map[string]any{"timestamp": at}},
		{"messageId": "wamid.P2", "from": "447700900123", "type": "image", "mediaId": "m1", "message": map[string]any{"timestamp": at, "image": map[string]any{"mime_type": "image/jpeg"}}},
	}
	if err := env.plugin.pollOnce(env.app); err != nil {
		t.Fatal(err)
	}
	if err := env.plugin.pollOnce(env.app); err != nil { // nothing new
		t.Fatal(err)
	}

	messages, _ := env.app.FindAllRecords(CollectionNameMessages)
	if len(messages) != 2 {
		t.Fatalf("Expected 2 messages, got %d", len(messages))
	}
	contact, _ := env.app.FindFirstRecordByData(CollectionNameContacts, "phone", testContactPhone)
	if contact.GetInt("unread") != 2 || contact.GetString("lastMessage") != "[image]" && contact.GetString("lastMessage") != "One" {
		t.Fatalf("Unexpected contact %v", contact)
	}
	image, _ := env.app.FindFirstRecordByData(CollectionNameMessages, "messageId", "wamid.P2")
	if image.GetString("mediaMime") != "image/jpeg" {
		t.Fatalf("Expected the media type, got %v", image)
	}
	state, _ := env.plugin.loadIncomingState(env.app)
	if state.Mode != IncomingPolling || state.Cursor != "c2" {
		t.Fatalf("Unexpected state %+v", state)
	}

	// polling also settles the statuses of inbox replies
	res := env.do(http.MethodPost, "/api/whatsapp/inbox/"+contact.Id, map[string]any{"text": "Thanks"}, env.superuserToken)
	messageID := res.body["messageId"].(string)
	fake.mu.Lock()
	fake.statuses[messageID] = "delivered"
	fake.mu.Unlock()
	if err := env.plugin.pollOnce(env.app); err != nil {
		t.Fatal(err)
	}
	sent, _ := env.app.FindFirstRecordByData(CollectionNameMessages, "messageId", messageID)
	if sent.GetString("status") != "delivered" {
		t.Fatalf("Expected delivered, got %q", sent.GetString("status"))
	}
}

func TestInbox(t *testing.T) {
	env, fake := newIncomingEnv(t, "https://app.example.com")
	env.postEvent(fake.secret, receivedEvent("evt_1", "wamid.IN1", testContactPhone, "Hola"))
	contact, _ := env.app.FindFirstRecordByData(CollectionNameContacts, "phone", testContactPhone)

	t.Run("superusers only", func(t *testing.T) {
		for _, url := range []string{"/api/whatsapp/inbox", "/api/whatsapp/inbox/" + contact.Id} {
			if res := env.do(http.MethodGet, url, nil, env.memberToken); res.status != http.StatusForbidden && res.status != http.StatusUnauthorized {
				t.Fatalf("Expected %s to refuse a member, got %d", url, res.status)
			}
		}
	})

	t.Run("list and read", func(t *testing.T) {
		res := env.do(http.MethodGet, "/api/whatsapp/inbox", nil, env.superuserToken)
		contacts := res.body["contacts"].([]any)
		if len(contacts) != 1 || contacts[0].(map[string]any)["windowOpen"] != true {
			t.Fatalf("Unexpected contacts %s", res.raw)
		}

		res = env.do(http.MethodGet, "/api/whatsapp/inbox/"+contact.Id, nil, env.superuserToken)
		if msgs := res.body["messages"].([]any); len(msgs) != 1 {
			t.Fatalf("Unexpected thread %s", res.raw)
		}

		if res := env.do(http.MethodPost, "/api/whatsapp/inbox/"+contact.Id+"/read", nil, env.superuserToken); res.status != http.StatusNoContent {
			t.Fatalf("Expected 204, got %d", res.status)
		}
		contact, _ := env.app.FindRecordById(CollectionNameContacts, contact.Id)
		if contact.GetInt("unread") != 0 || len(fake.read) != 1 || fake.read[0] != "wamid.IN1" {
			t.Fatalf("Expected the thread read, got unread=%d read=%v", contact.GetInt("unread"), fake.read)
		}
	})

	t.Run("template", func(t *testing.T) {
		res := env.do(http.MethodGet, "/api/whatsapp/inbox/templates", nil, env.superuserToken)
		templates := res.body["templates"].([]any)
		if len(templates) != 1 || templates[0].(map[string]any)["name"] != "order_shipped" {
			t.Fatalf("Expected only the approved non-auth template, got %s", res.raw)
		}

		res = env.do(http.MethodPost, "/api/whatsapp/inbox/"+contact.Id, map[string]any{
			"template": map[string]any{"name": "order_shipped", "language": "en_US", "params": []string{"A-12"}},
		}, env.superuserToken)
		if res.status != http.StatusOK {
			t.Fatalf("Expected 200, got %d: %s", res.status, res.raw)
		}
		last := fake.sent[len(fake.sent)-1]
		if last["type"] != "template" || last["to"] != testContactPhone {
			t.Fatalf("Unexpected send %v", last)
		}
	})

	t.Run("window closed", func(t *testing.T) {
		closed, _ := env.plugin.upsertContact(env.app, testClosedPhone)
		if err := env.app.Save(closed); err != nil {
			t.Fatal(err)
		}
		res := env.do(http.MethodPost, "/api/whatsapp/inbox/"+closed.Id, map[string]any{"text": "Hello?"}, env.superuserToken)
		if res.status != http.StatusUnprocessableEntity || res.body["code"] != "window_closed" {
			t.Fatalf("Expected 422 window_closed, got %d: %s", res.status, res.raw)
		}
	})

	t.Run("media", func(t *testing.T) {
		message, _ := env.app.FindFirstRecordByData(CollectionNameMessages, "messageId", "wamid.IN1")
		message.Set("mediaId", "m1")
		if err := env.app.Save(message); err != nil {
			t.Fatal(err)
		}

		req := httptest.NewRequest(http.MethodGet, "/api/whatsapp/inbox/media/m1", nil)
		req.Header.Set("Authorization", env.superuserToken)
		rec := httptest.NewRecorder()
		env.handler.ServeHTTP(rec, req)
		body, _ := io.ReadAll(rec.Body)
		if rec.Code != http.StatusOK || string(body) != "jpeg-bytes" || rec.Header().Get("Content-Security-Policy") != "sandbox" {
			t.Fatalf("Unexpected media response %d %q %v", rec.Code, body, rec.Header())
		}

		// only media of stored messages
		if res := env.do(http.MethodGet, "/api/whatsapp/inbox/media/other", nil, env.superuserToken); res.status != http.StatusNotFound {
			t.Fatalf("Expected 404, got %d", res.status)
		}
	})
}

func TestIncomingNewKeyRotatesSecret(t *testing.T) {
	env, fake := newIncomingEnv(t, "https://app.example.com")
	first, _ := env.plugin.loadIncomingState(env.app)

	// the same key keeps its secret
	if err := env.plugin.syncIncoming(env.app); err != nil {
		t.Fatal(err)
	}
	same, _ := env.plugin.loadIncomingState(env.app)
	if same.Secret != first.Secret || fake.webhooks[1]["rotate"] == true {
		t.Fatalf("Expected the secret kept, got %+v / %v", same, fake.webhooks[1])
	}

	// a lost secret is rotated
	same.Secret = ""
	if err := env.plugin.saveIncomingState(env.app, same); err != nil {
		t.Fatal(err)
	}
	if err := env.plugin.syncIncoming(env.app); err != nil {
		t.Fatal(err)
	}
	rotated, _ := env.plugin.loadIncomingState(env.app)
	if rotated.Secret == "" || rotated.Secret == first.Secret {
		t.Fatalf("Expected a new secret, got %+v", rotated)
	}
}
