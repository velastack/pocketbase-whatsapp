package whatsapp

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func unix(t time.Time) string { return strconv.FormatInt(t.Unix(), 10) }

func echoEvent(id, messageID, to, text string, at time.Time) map[string]any {
	return map[string]any{
		"id":   id,
		"type": "message.echo",
		"data": map[string]any{
			"messageId": messageID,
			"to":        strings.TrimPrefix(to, "+"),
			"type":      "text",
			"timestamp": unix(at),
			"text":      text,
			"message":   map[string]any{"id": messageID, "type": "text", "text": map[string]any{"body": text}},
		},
	}
}

func historyMessage(messageID, direction, text string, at time.Time) map[string]any {
	m := map[string]any{
		"messageId": messageID,
		"direction": direction,
		"type":      "text",
		"timestamp": unix(at),
		"text":      text,
		"status":    "read",
		"message":   map[string]any{"id": messageID, "type": "text"},
	}
	customer := strings.TrimPrefix(testContactPhone, "+")
	if direction == "in" {
		m["from"] = customer
		m["status"] = "received"
	} else {
		m["from"] = "1098"
		m["to"] = customer
	}
	return m
}

func TestCoexistenceEcho(t *testing.T) {
	env, fake := newIncomingEnv(t, "https://app.example.com")
	now := time.Now()

	env.postEvent(fake.secret, receivedEvent("evt_1", "wamid.IN1", testContactPhone, "¿Abren hoy?"))
	for range 2 {
		if res := env.postEvent(fake.secret, echoEvent("evt_2", "wamid.ECHO1", testContactPhone, "Sí, hasta las 10", now.Add(time.Second))); res.status != http.StatusNoContent {
			t.Fatalf("Expected 204, got %d: %s", res.status, res.raw)
		}
	}

	echo, err := env.app.FindFirstRecordByData(CollectionNameMessages, "messageId", "wamid.ECHO1")
	if err != nil {
		t.Fatal(err)
	}
	if echo.GetString("direction") != "out" || echo.GetString("origin") != OriginApp || echo.GetString("status") != "sent" {
		t.Fatalf("Unexpected echo %v", echo)
	}
	all, _ := env.app.FindAllRecords(CollectionNameMessages)
	if len(all) != 2 {
		t.Fatalf("Expected the resent echo to be ignored, got %d messages", len(all))
	}

	contact, _ := env.app.FindFirstRecordByData(CollectionNameContacts, "phone", testContactPhone)
	// answered from the phone: read, and the window is the customer's message's
	if contact.GetInt("unread") != 0 || contact.GetString("lastMessage") != "Sí, hasta las 10" {
		t.Fatalf("Unexpected contact %v", contact)
	}
	if !windowOpen(contact, now) {
		t.Fatal("Expected the customer's message to keep the window open")
	}

	// an echo alone never opens the window
	env.postEvent(fake.secret, echoEvent("evt_3", "wamid.ECHO2", "+447700900456", "Hola", now))
	other, _ := env.app.FindFirstRecordByData(CollectionNameContacts, "phone", "+447700900456")
	if windowOpen(other, now) || other.GetInt("unread") != 0 {
		t.Fatalf("Expected a closed window and nothing unread, got %v", other)
	}

	// and polling never asks VelaStack for an echo's status
	pending, _ := recentPendingOutbound(env.app, 20)
	if len(pending) != 0 {
		t.Fatalf("Expected no pending inbox sends, got %d", len(pending))
	}
}

func TestCoexistenceHistory(t *testing.T) {
	env, fake := newIncomingEnv(t, "https://app.example.com")
	now := time.Now()

	event := map[string]any{
		"id":   "evt_h1",
		"type": "message.history",
		"data": map[string]any{
			"phase": 0, "chunkOrder": 1, "progress": 100,
			"messages": []map[string]any{
				historyMessage("wamid.H2", "out", "Sí, a las nueve", now.Add(-47*time.Hour)),
				historyMessage("wamid.H1", "in", "¿Mesa para cuatro?", now.Add(-48*time.Hour)),
			},
		},
	}
	for range 2 {
		if res := env.postEvent(fake.secret, event); res.status != http.StatusNoContent {
			t.Fatalf("Expected 204, got %d: %s", res.status, res.raw)
		}
	}

	contact, _ := env.app.FindFirstRecordByData(CollectionNameContacts, "phone", testContactPhone)
	if contact.GetInt("unread") != 0 || windowOpen(contact, now) {
		t.Fatalf("Expected history to be neither unread nor open the window, got %v", contact)
	}
	if contact.GetString("lastMessage") != "Sí, a las nueve" {
		t.Fatalf("Expected the newest history message as the preview, got %q", contact.GetString("lastMessage"))
	}

	// the thread is in the order the messages were sent, not stored
	res := env.do(http.MethodGet, "/api/whatsapp/inbox/"+contact.Id, nil, env.superuserToken)
	messages, _ := res.body["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("Expected 2 messages, got %s", res.raw)
	}
	first := messages[0].(map[string]any)
	if first["messageId"] != "wamid.H1" || first["origin"] != OriginHistory {
		t.Fatalf("Expected the older history message first, got %v", first)
	}

	// a live message after the history opens the window and counts
	env.postEvent(fake.secret, receivedEvent("evt_l", "wamid.LIVE", testContactPhone, "Hola de nuevo"))
	contact, _ = env.app.FindFirstRecordByData(CollectionNameContacts, "phone", testContactPhone)
	if contact.GetInt("unread") != 1 || !windowOpen(contact, time.Now()) {
		t.Fatalf("Unexpected contact %v", contact)
	}
}

func TestCoexistenceContacts(t *testing.T) {
	env, fake := newIncomingEnv(t, "https://app.example.com")
	env.postEvent(fake.secret, receivedEvent("evt_1", "wamid.IN1", testContactPhone, "Hola"))

	sync := func(id string, contacts ...map[string]any) {
		if res := env.postEvent(fake.secret, map[string]any{
			"id": id, "type": "contact.sync", "data": map[string]any{"contacts": contacts},
		}); res.status != http.StatusNoContent {
			t.Fatalf("Expected 204, got %d: %s", res.status, res.raw)
		}
	}
	customer := strings.TrimPrefix(testContactPhone, "+")
	sync("evt_c1",
		map[string]any{"phone": customer, "fullName": "Ana Pérez", "firstName": "Ana", "action": "add"},
		map[string]any{"phone": "447700900777", "fullName": "Luis", "action": "add"},
	)

	contact, _ := env.app.FindFirstRecordByData(CollectionNameContacts, "phone", testContactPhone)
	if contact.GetString("savedName") != "Ana Pérez" || contact.GetString("name") != "Ana" {
		t.Fatalf("Expected the saved and the profile name, got %v", contact)
	}
	res := env.do(http.MethodGet, "/api/whatsapp/inbox", nil, env.superuserToken)
	if listed, _ := res.body["contacts"].([]any); len(listed) != 1 {
		t.Fatalf("Expected address book entries without messages to stay out of the inbox, got %s", res.raw)
	}

	sync("evt_c2", map[string]any{"phone": customer, "action": "remove"})
	contact, _ = env.app.FindFirstRecordByData(CollectionNameContacts, "phone", testContactPhone)
	if contact.GetString("savedName") != "" || !contact.GetBool("savedRemoved") {
		t.Fatalf("Expected the saved name to go, got %v", contact)
	}
	if messages, _ := env.app.FindAllRecords(CollectionNameMessages); len(messages) != 1 {
		t.Fatal("Expected the contact's messages to stay")
	}
}

func TestCoexistenceHistoryStatus(t *testing.T) {
	env, fake := newIncomingEnv(t, "https://app.example.com")
	env.postEvent(fake.secret, map[string]any{"id": "evt_s1", "type": "history.status", "data": map[string]any{"phase": 0, "progress": 55}})
	env.postEvent(fake.secret, map[string]any{"id": "evt_s2", "type": "history.status", "data": map[string]any{"phase": 0, "progress": 30}})

	res := env.do(http.MethodGet, "/api/whatsapp/inbox", nil, env.superuserToken)
	incoming, _ := res.body["incoming"].(map[string]any)
	history, _ := incoming["history"].(map[string]any)
	if incoming["onBizApp"] != true || history["progress"] != float64(55) {
		t.Fatalf("Expected the import progress to only move forward, got %s", res.raw)
	}
}

func TestCoexistencePolling(t *testing.T) {
	env, fake := newIncomingEnv(t, "http://localhost:8090")
	now := time.Now()

	// an upgrade from a version that listed only incoming messages
	state, _ := env.plugin.loadIncomingState(env.app)
	state.Cursor = "c2"
	if err := env.plugin.saveIncomingState(env.app, state); err != nil {
		t.Fatal(err)
	}

	in := historyMessage("wamid.P1", "in", "Hola", now.Add(-time.Minute))
	in["origin"] = "api"
	echo := historyMessage("wamid.P2", "out", "Te esperamos", now)
	echo["origin"] = OriginApp
	old := historyMessage("wamid.P0", "in", "Hace un mes", now.Add(-20*24*time.Hour))
	old["origin"] = OriginHistory
	fake.inbound = []map[string]any{old, in, echo}
	fake.contacts = []map[string]any{{"phone": strings.TrimPrefix(testContactPhone, "+"), "fullName": "Ana Pérez", "removed": false}}
	fake.status = map[string]any{"onBizApp": true, "history": map[string]any{"progress": 100}}

	if err := env.plugin.pollOnce(env.app); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(fake.queries[0], "conversation=1") || strings.Contains(fake.queries[0], "after=") {
		t.Fatalf("Expected the first poll after the upgrade to list the conversation from the start, got %q", fake.queries[0])
	}
	messages, _ := env.app.FindAllRecords(CollectionNameMessages)
	if len(messages) != 3 {
		t.Fatalf("Expected 3 messages, got %d", len(messages))
	}
	stored, _ := env.app.FindFirstRecordByData(CollectionNameMessages, "messageId", "wamid.P2")
	if stored.GetString("origin") != OriginApp || stored.GetString("direction") != "out" {
		t.Fatalf("Unexpected echo %v", stored)
	}
	contact, _ := env.app.FindFirstRecordByData(CollectionNameContacts, "phone", testContactPhone)
	if contact.GetString("savedName") != "Ana Pérez" || contact.GetInt("unread") != 0 {
		t.Fatalf("Unexpected contact %v", contact)
	}
	state, _ = env.plugin.loadIncomingState(env.app)
	if !state.OnBizApp || state.History == nil || state.History.Progress != 100 || state.CursorVersion != conversationCursorVersion || state.ContactsCursor != "k1" {
		t.Fatalf("Unexpected state %+v", state)
	}
}
