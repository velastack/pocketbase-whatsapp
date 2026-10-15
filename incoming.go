package whatsapp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
)

// Incoming messages (VelaStack mode only).
//
// VelaStack receives Meta's webhooks for the project's number and forwards
// each incoming message as a signed `message.received` event. The plugin
// registers its own webhook URL with VelaStack (`PUT {relay}/webhook`) when
// the app URL is public https, and otherwise (eg. localhost) polls
// `GET {relay}/messages?conversation=1`. Both paths store the messages in the
// [CollectionNameContacts] and [CollectionNameMessages] collections, so apps
// react to them with the usual record hooks (eg.
// `app.OnRecordAfterCreateSuccess(whatsapp.CollectionNameMessages)`).
//
// Media stays at Meta and is fetched through VelaStack on request.
//
// A number also used in the WhatsApp Business app adds the owner's replies
// from the app and the synced chat history; see coexistence.go.

const (
	// CollectionNameContacts is the name of the collection with the people
	// who wrote to (or were messaged from) the WhatsApp number.
	CollectionNameContacts = "_whatsappContacts"

	// CollectionNameMessages is the name of the collection with the incoming
	// and outgoing (inbox) messages.
	CollectionNameMessages = "_whatsappMessages"

	storeKeyIncoming = "incoming"

	// webhookTolerance is how far the signature timestamp may be from now.
	webhookTolerance = 5 * time.Minute

	// customerServiceWindow is how long after a customer's last message a
	// free-form reply is allowed; after that only templates go out.
	customerServiceWindow = 24 * time.Hour
)

// Incoming delivery modes.
const (
	IncomingOff     = ""
	IncomingWebhook = "webhook"
	IncomingPolling = "polling"
)

// incomingState is what the plugin keeps about receiving messages
// (stored encrypted, it holds the webhook signing secret).
type incomingState struct {
	Mode       string `json:"mode"`
	WebhookURL string `json:"webhookURL"`
	Secret     string `json:"secret"`
	// KeyID is the id part of the VelaStack key the secret belongs to.
	KeyID    string `json:"keyId"`
	Cursor   string `json:"cursor"`
	Error    string `json:"error"`
	SyncedAt string `json:"syncedAt"`
	// CursorVersion is the listing Cursor belongs to; see [conversationCursorVersion].
	CursorVersion  int    `json:"cursorVersion"`
	ContactsCursor string `json:"contactsCursor"`
	// OnBizApp: the number is also in the WhatsApp Business app (coexistence).
	OnBizApp bool          `json:"onBizApp"`
	History  *historyState `json:"history,omitempty"`
	// StatusAt is when OnBizApp and History were last read from VelaStack.
	StatusAt string `json:"statusAt"`
}

func (p *plugin) loadIncomingState(app core.App) (*incomingState, error) {
	s := &incomingState{}
	if _, err := p.loadStoreValue(app, storeKeyIncoming, s); err != nil {
		return nil, err
	}
	return s, nil
}

func (p *plugin) saveIncomingState(app core.App, s *incomingState) error {
	return p.saveStoreValue(app, storeKeyIncoming, s, true)
}

// -------------------------------------------------------------------
// collections
// -------------------------------------------------------------------

func migrateIncomingUp(txApp core.App) error {
	// Not system collections, so apps can set API rules (PocketBase locks the
	// rules of system ones); the fields are, and protectIncoming stops the
	// collections from being deleted or renamed.
	contacts := core.NewBaseCollection(CollectionNameContacts)
	contacts.Fields.Add(
		&core.TextField{Name: "phone", System: true, Required: true, Max: 16},
		&core.TextField{Name: "name", System: true, Max: 255},
		// the auth record with this (verified) phone number, if any
		&core.TextField{Name: "userCollection", System: true},
		&core.TextField{Name: "user", System: true},
		&core.DateField{Name: "lastInbound", System: true},
		&core.DateField{Name: "lastMessageAt", System: true},
		&core.TextField{Name: "lastMessage", System: true, Max: 255},
		&core.NumberField{Name: "unread", System: true, OnlyInt: true},
		&core.AutodateField{Name: "created", System: true, OnCreate: true},
		&core.AutodateField{Name: "updated", System: true, OnCreate: true, OnUpdate: true},
	)
	contacts.AddIndex("idx_whatsappContacts_phone", true, "phone", "")
	contacts.AddIndex("idx_whatsappContacts_lastMessageAt", false, "lastMessageAt", "")
	if err := txApp.Save(contacts); err != nil {
		return err
	}

	messages := core.NewBaseCollection(CollectionNameMessages)
	messages.Fields.Add(
		&core.RelationField{Name: "contact", System: true, Required: true, CollectionId: contacts.Id, MaxSelect: 1, CascadeDelete: true},
		&core.SelectField{Name: "direction", System: true, Required: true, Values: []string{"in", "out"}, MaxSelect: 1},
		// the WhatsApp message id (wamid)
		&core.TextField{Name: "messageId", System: true, Required: true},
		// the VelaStack event id, for deduplicating webhook retries
		&core.TextField{Name: "eventId", System: true},
		&core.TextField{Name: "type", System: true},
		&core.TextField{Name: "text", System: true, Max: 5000},
		&core.TextField{Name: "template", System: true},
		&core.TextField{Name: "mediaId", System: true},
		&core.TextField{Name: "mediaMime", System: true},
		&core.TextField{Name: "mediaName", System: true},
		&core.TextField{Name: "replyTo", System: true},
		&core.TextField{Name: "status", System: true},
		&core.TextField{Name: "error", System: true, Max: 500},
		&core.JSONField{Name: "raw", System: true, MaxSize: 1 << 20},
		// when the message was sent (Meta's timestamp for incoming messages)
		&core.DateField{Name: "sentAt", System: true},
		&core.AutodateField{Name: "created", System: true, OnCreate: true},
		&core.AutodateField{Name: "updated", System: true, OnCreate: true, OnUpdate: true},
	)
	messages.AddIndex("idx_whatsappMessages_messageId", true, "messageId", "")
	messages.AddIndex("idx_whatsappMessages_eventId", true, "eventId", "eventId != ''")
	messages.AddIndex("idx_whatsappMessages_contact_created", false, "contact, created", "")

	return txApp.Save(messages)
}

func migrateIncomingDown(txApp core.App) error {
	for _, name := range []string{CollectionNameMessages, CollectionNameContacts} {
		col, err := txApp.FindCollectionByNameOrId(name)
		if err != nil {
			continue
		}
		col.System = false
		if err := txApp.SaveNoValidate(col); err != nil {
			return err
		}
		if err := txApp.Delete(col); err != nil {
			return err
		}
	}
	return nil
}

// -------------------------------------------------------------------
// ingest
// -------------------------------------------------------------------

// inboundMessage is an incoming message, as VelaStack forwards it
// (the `message.received` event data and the messages listing share it).
type inboundMessage struct {
	MessageID string         `json:"messageId"`
	From      string         `json:"from"`
	Name      string         `json:"name"`
	Type      string         `json:"type"`
	Timestamp string         `json:"timestamp"`
	Text      string         `json:"text"`
	MediaID   string         `json:"mediaId"`
	ReplyTo   string         `json:"replyTo"`
	Message   map[string]any `json:"message"`
}

// sentAt is when the customer sent the message (Meta's unix timestamp),
// falling back to now.
func (m *inboundMessage) sentAt() time.Time {
	ts := m.Timestamp
	if ts == "" {
		ts, _ = m.Message["timestamp"].(string)
	}
	if sec, err := strconv.ParseInt(ts, 10, 64); err == nil && sec > 0 {
		return time.Unix(sec, 0).UTC()
	}
	return time.Now().UTC()
}

// media returns the mime type and file name Meta sent with a media message.
func (m *inboundMessage) media() (mime string, name string) {
	if obj, ok := m.Message[m.Type].(map[string]any); ok {
		mime, _ = obj["mime_type"].(string)
		name, _ = obj["filename"].(string)
	}
	return mime, name
}

// preview is the contact list line for a message.
func preview(text, kind string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		text = "[" + kind + "]"
	}
	if r := []rune(text); len(r) > 120 {
		text = string(r[:119]) + "…"
	}
	return text
}

// contactPhone turns WhatsApp's bare digits (wa_id) into E.164.
//
// Note that the wa_id can differ from the number a user typed (eg. old
// Mexican +521 numbers or a missing Brazilian ninth digit), so such a contact
// isn't linked to its user.
func contactPhone(from string) (string, error) {
	if !strings.HasPrefix(from, "+") {
		from = "+" + from
	}
	return NormalizePhone(from)
}

// upsertContact finds or creates the contact for an E.164 phone number.
func (p *plugin) upsertContact(txApp core.App, phone string) (*core.Record, error) {
	contact, err := txApp.FindFirstRecordByData(CollectionNameContacts, "phone", phone)
	if err == nil {
		return contact, nil
	}

	collection, err := txApp.FindCachedCollectionByNameOrId(CollectionNameContacts)
	if err != nil {
		return nil, err
	}
	contact = core.NewRecord(collection)
	contact.Set("phone", phone)
	return contact, nil
}

// linkUser attaches the contact to the auth record with its phone number, in
// the first auth collection with WhatsApp auth enabled that has one.
func (p *plugin) linkUser(txApp core.App, contact *core.Record) {
	if contact.GetString("user") != "" {
		return
	}

	collections, err := txApp.FindAllCollections(core.CollectionTypeAuth)
	if err != nil {
		return
	}
	for _, collection := range collections {
		if collection.Name == core.CollectionNameSuperusers {
			continue
		}
		cfg, err := p.loadCollectionConfig(txApp, collection.Id)
		if err != nil || !cfg.Enabled || collection.Fields.GetByName(cfg.PhoneField) == nil {
			continue
		}
		record, err := txApp.FindFirstRecordByData(collection, cfg.PhoneField, contact.GetString("phone"))
		if err != nil {
			continue
		}
		if cfg.PhoneVerifiedField != "" && !record.GetBool(cfg.PhoneVerifiedField) {
			continue
		}
		contact.Set("userCollection", collection.Id)
		contact.Set("user", record.Id)
		return
	}
}

// ingestInbound stores an incoming message once (by event and message id)
// and updates its contact. It reports whether the message was new.
//
// A message from the synced history (origin [OriginHistory]) is neither
// unread nor a reason to think the customer service window is open: Meta's
// window only counts Cloud API conversations.
func (p *plugin) ingestInbound(app core.App, eventID string, m inboundMessage, origin string) (bool, error) {
	if m.MessageID == "" || m.From == "" {
		return false, errors.New("incoming message without id or sender")
	}
	phone, err := contactPhone(m.From)
	if err != nil {
		return false, fmt.Errorf("incoming message from %q: %w", m.From, err)
	}

	created := false
	err = app.RunInTransaction(func(txApp core.App) error {
		if exists, _ := txApp.FindFirstRecordByData(CollectionNameMessages, "messageId", m.MessageID); exists != nil {
			return nil
		}

		contact, err := p.upsertContact(txApp, phone)
		if err != nil {
			return err
		}
		sentAt := m.sentAt()
		text := preview(m.Text, m.Type)

		if m.Name != "" {
			contact.Set("name", m.Name)
		}
		if origin != OriginHistory && contact.GetDateTime("lastInbound").Time().Before(sentAt) {
			contact.Set("lastInbound", sentAt)
		}
		if contact.GetDateTime("lastMessageAt").Time().Before(sentAt) {
			contact.Set("lastMessageAt", sentAt)
			contact.Set("lastMessage", text)
		}
		if origin != OriginHistory {
			contact.Set("unread", contact.GetInt("unread")+1)
		}
		p.linkUser(txApp, contact)
		if err := txApp.Save(contact); err != nil {
			return err
		}

		collection, err := txApp.FindCachedCollectionByNameOrId(CollectionNameMessages)
		if err != nil {
			return err
		}
		mime, name := m.media()
		message := core.NewRecord(collection)
		message.Load(map[string]any{
			"contact":   contact.Id,
			"direction": "in",
			"origin":    origin,
			"messageId": m.MessageID,
			"eventId":   eventID,
			"type":      m.Type,
			"text":      m.Text,
			"mediaId":   m.MediaID,
			"mediaMime": mime,
			"mediaName": name,
			"replyTo":   m.ReplyTo,
			"status":    "received",
			"raw":       m.Message,
			"sentAt":    sentAt,
		})
		if err := txApp.Save(message); err != nil {
			return err
		}
		created = true
		return nil
	})

	return created, err
}

// statusOrder is the order statuses move in; a late `delivered` must not
// undo `read`, but `failed` always applies.
var statusOrder = []string{"accepted", "sent", "delivered", "read"}

func statusRank(s string) int {
	for i, v := range statusOrder {
		if v == s {
			return i
		}
	}
	return -1
}

type statusUpdate struct {
	MessageID string `json:"messageId"`
	Status    string `json:"status"`
	Errors    []struct {
		Code    int    `json:"code"`
		Title   string `json:"title"`
		Message string `json:"message"`
	} `json:"errors"`
}

// applyStatus moves an outgoing message forward to the status VelaStack reports.
func (p *plugin) applyStatus(app core.App, u statusUpdate) error {
	message, err := app.FindFirstRecordByData(CollectionNameMessages, "messageId", u.MessageID)
	if err != nil {
		return nil // not sent from the inbox
	}
	if message.GetString("direction") != "out" {
		return nil
	}
	if u.Status != "failed" && statusRank(u.Status) <= statusRank(message.GetString("status")) {
		return nil
	}

	message.Set("status", u.Status)
	if len(u.Errors) > 0 {
		e := u.Errors[0]
		detail := e.Title
		if detail == "" {
			detail = e.Message
		}
		if e.Code != 0 {
			detail = fmt.Sprintf("%d %s", e.Code, detail)
		}
		message.Set("error", strings.TrimSpace(detail))
	}
	return app.Save(message)
}

// -------------------------------------------------------------------
// webhook
// -------------------------------------------------------------------

type forwardedEvent struct {
	ID   string          `json:"id"`
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

var errBadSignature = errors.New("invalid signature")

// verifySignature checks a VelaStack `X-VelaStack-Signature: t=…,v1=…`
// header: v1 is the HMAC-SHA256 of "<t>.<body>" with the webhook secret.
func verifySignature(header string, body []byte, secret string, now time.Time) error {
	if secret == "" {
		return errBadSignature
	}

	var ts string
	var signatures []string
	for _, part := range strings.Split(header, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch k {
		case "t":
			ts = v
		case "v1":
			signatures = append(signatures, v)
		}
	}

	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || len(signatures) == 0 {
		return errBadSignature
	}
	if d := now.Sub(time.Unix(sec, 0)); d > webhookTolerance || d < -webhookTolerance {
		return errBadSignature
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts))
	mac.Write([]byte("."))
	mac.Write(body)
	expected := mac.Sum(nil)

	for _, s := range signatures {
		if got, err := hex.DecodeString(s); err == nil && hmac.Equal(got, expected) {
			return nil
		}
	}
	return errBadSignature
}

// handleEvent applies one VelaStack event.
func (p *plugin) handleEvent(app core.App, event forwardedEvent) error {
	switch event.Type {
	case "message.received":
		m := inboundMessage{}
		if err := json.Unmarshal(event.Data, &m); err != nil {
			return err
		}
		_, err := p.ingestInbound(app, event.ID, m, "")
		return err
	case "message.status":
		u := statusUpdate{}
		if err := json.Unmarshal(event.Data, &u); err != nil {
			return err
		}
		return p.applyStatus(app, u)
	default:
		// webhook.test and the account/template events need nothing here
		_, err := p.handleCoexistenceEvent(app, event)
		return err
	}
}

// -------------------------------------------------------------------
// helpers
// -------------------------------------------------------------------

// windowOpen reports whether a free-form reply can still reach the contact.
func windowOpen(contact *core.Record, now time.Time) bool {
	last := contact.GetDateTime("lastInbound")
	return !last.IsZero() && now.Sub(last.Time()) < customerServiceWindow
}

// recentPendingOutbound returns the inbox messages whose delivery isn't
// settled yet, to refresh while polling (webhooks deliver statuses instead).
// Messages from the WhatsApp Business app aren't the inbox's sends.
func recentPendingOutbound(app core.App, limit int) ([]*core.Record, error) {
	since, _ := types.ParseDateTime(time.Now().Add(-customerServiceWindow))
	return app.FindRecordsByFilter(
		CollectionNameMessages,
		"direction = 'out' && origin = '' && status != 'read' && status != 'failed' && created >= {:since}",
		"-created",
		limit,
		0,
		dbx.Params{"since": since.String()},
	)
}
