package whatsapp

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/pocketbase/pocketbase/core"
)

// Coexistence: a number that stays in the WhatsApp Business app on the
// owner's phone while it is also connected to VelaStack.
//
// VelaStack then forwards, besides `message.received`:
//
//   - `message.echo`: a message the owner sent from the app. Stored as an
//     outgoing message with `origin: "app"`; it never opens the customer
//     service window and it clears the contact's unread count, since the owner
//     has answered.
//   - `message.history`: up to 100 messages of the last 30 days of chats,
//     synced once after onboarding. Stored with `origin: "history"`; they
//     neither count as unread nor open the window.
//   - `contact.sync`: the app's address book. The name the owner saved goes
//     into the contact's `savedName`.
//   - `history.status`: how far the history import is, for the Inbox banner.
//
// The polling path gets the same through `GET {relay}/messages?conversation=1`
// and `GET {relay}/contacts`.

// Message origins (the `origin` field of [CollectionNameMessages]); empty
// means the Cloud API: live incoming messages and the inbox's own sends.
const (
	OriginApp     = "app"
	OriginHistory = "history"
)

func migrateCoexistenceUp(txApp core.App) error {
	messages, err := txApp.FindCollectionByNameOrId(CollectionNameMessages)
	if err != nil {
		return err
	}
	messages.Fields.Add(
		// where an outgoing message came from on a coexistence number
		&core.SelectField{Name: "origin", System: true, Values: []string{OriginApp, OriginHistory}, MaxSelect: 1},
	)
	messages.AddIndex("idx_whatsappMessages_contact_sentAt", false, "contact, sentAt", "")
	if err := txApp.Save(messages); err != nil {
		return err
	}

	contacts, err := txApp.FindCollectionByNameOrId(CollectionNameContacts)
	if err != nil {
		return err
	}
	contacts.Fields.Add(
		// the name in the WhatsApp Business app's address book
		&core.TextField{Name: "savedName", System: true, Max: 255},
		// the owner deleted the contact from the address book
		&core.BoolField{Name: "savedRemoved", System: true},
	)
	return txApp.Save(contacts)
}

func migrateCoexistenceDown(txApp core.App) error {
	if messages, err := txApp.FindCollectionByNameOrId(CollectionNameMessages); err == nil {
		messages.RemoveIndex("idx_whatsappMessages_contact_sentAt")
		messages.Fields.RemoveByName("origin")
		if err := txApp.SaveNoValidate(messages); err != nil {
			return err
		}
	}
	if contacts, err := txApp.FindCollectionByNameOrId(CollectionNameContacts); err == nil {
		contacts.Fields.RemoveByName("savedName")
		contacts.Fields.RemoveByName("savedRemoved")
		if err := txApp.SaveNoValidate(contacts); err != nil {
			return err
		}
	}
	return nil
}

// historyState is the last `history.status` VelaStack reported.
type historyState struct {
	Progress int    `json:"progress"`
	Error    string `json:"error,omitempty"`
}

// ingestOutbound stores a message the business sent from the WhatsApp
// Business app (an echo, or history) once, by message id. It reports whether
// the message was new.
func (p *plugin) ingestOutbound(app core.App, eventID string, m listedMessage, origin string) (bool, error) {
	if m.MessageID == "" || m.To == "" {
		return false, errors.New("outgoing message without id or recipient")
	}
	phone, err := contactPhone(m.To)
	if err != nil {
		return false, fmt.Errorf("outgoing message to %q: %w", m.To, err)
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
		if contact.GetDateTime("lastMessageAt").Time().Before(sentAt) {
			contact.Set("lastMessageAt", sentAt)
			contact.Set("lastMessage", preview(m.Text, m.Type))
		}
		// the owner answered from the phone, so the thread has been seen
		if origin == OriginApp {
			contact.Set("unread", 0)
		}
		p.linkUser(txApp, contact)
		if err := txApp.Save(contact); err != nil {
			return err
		}

		collection, err := txApp.FindCachedCollectionByNameOrId(CollectionNameMessages)
		if err != nil {
			return err
		}
		status := m.Status
		if status == "" {
			status = "sent"
		}
		mime, name := m.media()
		message := core.NewRecord(collection)
		message.Load(map[string]any{
			"contact":   contact.Id,
			"direction": "out",
			"origin":    origin,
			"messageId": m.MessageID,
			"eventId":   eventID,
			"type":      m.Type,
			"text":      m.Text,
			"mediaId":   m.MediaID,
			"mediaMime": mime,
			"mediaName": name,
			"replyTo":   m.ReplyTo,
			"status":    status,
			"error":     m.Error,
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

// ingestConversation stores a message from the conversation listing or a
// `message.history` event, whichever way it went. The inbox's own sends
// (direction out, no origin) are already stored when they are sent.
func (p *plugin) ingestConversation(app core.App, eventID string, m listedMessage) error {
	switch {
	case m.Direction == "out" && (m.Origin == OriginApp || m.Origin == OriginHistory):
		_, err := p.ingestOutbound(app, eventID, m, m.Origin)
		return err
	case m.Direction == "in" || m.Direction == "":
		_, err := p.ingestInbound(app, eventID, m.inboundMessage, m.Origin)
		return err
	default:
		return nil
	}
}

// syncedContact is one address book entry, from `contact.sync` or the listing.
type syncedContact struct {
	Phone     string `json:"phone"`
	FullName  string `json:"fullName"`
	FirstName string `json:"firstName"`
	// add or remove (events); the listing sets Removed instead
	Action  string `json:"action"`
	Removed bool   `json:"removed"`
}

// applySavedName keeps the name the owner saved for a contact. A removal
// keeps the contact (deleting it would delete its messages).
func (p *plugin) applySavedName(app core.App, c syncedContact) error {
	phone, err := contactPhone(c.Phone)
	if err != nil {
		return nil // not a phone number we can message, nothing to show
	}
	return app.RunInTransaction(func(txApp core.App) error {
		contact, err := p.upsertContact(txApp, phone)
		if err != nil {
			return err
		}
		removed := c.Removed || c.Action == "remove"
		name := c.FullName
		if name == "" {
			name = c.FirstName
		}
		if removed {
			name = ""
		}
		if !contact.IsNew() && contact.GetString("savedName") == name && contact.GetBool("savedRemoved") == removed {
			return nil
		}
		if contact.IsNew() && removed {
			return nil
		}
		contact.Set("savedName", name)
		contact.Set("savedRemoved", removed)
		p.linkUser(txApp, contact)
		return txApp.Save(contact)
	})
}

// handleCoexistenceEvent applies the coexistence event types; it reports
// false for any other type.
func (p *plugin) handleCoexistenceEvent(app core.App, event forwardedEvent) (bool, error) {
	switch event.Type {
	case "message.echo":
		m := listedMessage{}
		if err := json.Unmarshal(event.Data, &m); err != nil {
			return true, err
		}
		_, err := p.ingestOutbound(app, event.ID, m, OriginApp)
		return true, err

	case "message.history":
		data := struct {
			Messages []listedMessage `json:"messages"`
		}{}
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return true, err
		}
		// One event holds many messages, so none of them takes its event id
		// (it's unique per message); the message ids dedupe a resend.
		for _, m := range data.Messages {
			m.Origin = OriginHistory
			if err := p.ingestConversation(app, "", m); err != nil {
				app.Logger().Warn("Failed to store a WhatsApp history message", "messageId", m.MessageID, "error", err)
			}
		}
		return true, nil

	case "contact.sync":
		data := struct {
			Contacts []syncedContact `json:"contacts"`
		}{}
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return true, err
		}
		for _, c := range data.Contacts {
			if err := p.applySavedName(app, c); err != nil {
				return true, err
			}
		}
		return true, nil

	case "history.status":
		data := struct {
			Progress *int   `json:"progress"`
			Error    string `json:"error"`
		}{}
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return true, err
		}
		p.incomingMu.Lock()
		defer p.incomingMu.Unlock()
		state, err := p.loadIncomingState(app)
		if err != nil {
			return true, err
		}
		history := historyState{Error: data.Error}
		if state.History != nil {
			history.Progress = state.History.Progress
		}
		if data.Progress != nil && *data.Progress > history.Progress {
			history.Progress = *data.Progress
		}
		state.History = &history
		state.OnBizApp = true
		return true, p.saveIncomingState(app, state)
	}
	return false, nil
}

// statusRefreshEvery is how often polling re-reads the sender's status from
// VelaStack (whether the number is on the WhatsApp Business app, and how far
// its history import is).
const statusRefreshEvery = 30 * time.Minute
