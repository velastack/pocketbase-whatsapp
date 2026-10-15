package whatsapp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	validation "github.com/pocketbase/ozzo-validation/v4"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/security"
)

// -------------------------------------------------------------------
// webhook (public, signed by VelaStack)
// -------------------------------------------------------------------

func (p *plugin) receiveWebhook(e *core.RequestEvent) error {
	body, err := io.ReadAll(io.LimitReader(e.Request.Body, 2<<20))
	if err != nil {
		return e.BadRequestError("Failed to read the request body.", err)
	}

	state, err := p.loadIncomingState(e.App)
	if err != nil {
		return e.InternalServerError("", err)
	}
	if err := verifySignature(e.Request.Header.Get("X-VelaStack-Signature"), body, state.Secret, time.Now()); err != nil {
		return e.UnauthorizedError("Invalid signature.", nil)
	}

	event := forwardedEvent{}
	if err := json.Unmarshal(body, &event); err != nil {
		return e.BadRequestError("Invalid event.", err)
	}

	if err := p.handleEvent(e.App, event); err != nil {
		// a non-2xx answer makes VelaStack retry
		e.App.Logger().Warn("Failed to handle a WhatsApp event", "id", event.ID, "type", event.Type, "error", err)
		return e.InternalServerError("Failed to handle the event.", nil)
	}

	return e.NoContent(http.StatusNoContent)
}

// -------------------------------------------------------------------
// inbox (superusers)
// -------------------------------------------------------------------

type inboxContact struct {
	Id    string `json:"id"`
	Phone string `json:"phone"`
	Name  string `json:"name"`
	// SavedName is the name in the WhatsApp Business app's address book.
	SavedName      string `json:"savedName"`
	UserCollection string `json:"userCollection"`
	User           string `json:"user"`
	LastMessage    string `json:"lastMessage"`
	LastMessageAt  string `json:"lastMessageAt"`
	LastInbound    string `json:"lastInbound"`
	Unread         int    `json:"unread"`
	WindowOpen     bool   `json:"windowOpen"`
}

func toInboxContact(app core.App, r *core.Record, now time.Time) inboxContact {
	// the dashboard links records by collection name
	userCollection := ""
	if id := r.GetString("userCollection"); id != "" {
		if c, err := app.FindCachedCollectionByNameOrId(id); err == nil {
			userCollection = c.Name
		}
	}
	return inboxContact{
		Id:             r.Id,
		Phone:          r.GetString("phone"),
		Name:           r.GetString("name"),
		SavedName:      r.GetString("savedName"),
		UserCollection: userCollection,
		User:           r.GetString("user"),
		LastMessage:    r.GetString("lastMessage"),
		LastMessageAt:  r.GetString("lastMessageAt"),
		LastInbound:    r.GetString("lastInbound"),
		Unread:         r.GetInt("unread"),
		WindowOpen:     windowOpen(r, now),
	}
}

const inboxContactsLimit = 200

// GET /api/whatsapp/inbox
func (p *plugin) inboxContacts(e *core.RequestEvent) error {
	state, err := p.loadIncomingState(e.App)
	if err != nil {
		return e.InternalServerError("", err)
	}

	// address book entries without a conversation stay out of the list
	records, err := e.App.FindRecordsByFilter(CollectionNameContacts, "lastMessageAt != ''", "-lastMessageAt", inboxContactsLimit, 0)
	if err != nil {
		return e.InternalServerError("", err)
	}

	now := time.Now()
	contacts := make([]inboxContact, 0, len(records))
	for _, r := range records {
		contacts = append(contacts, toInboxContact(e.App, r, now))
	}

	return e.JSON(http.StatusOK, map[string]any{
		"incoming": map[string]any{
			"mode":       state.Mode,
			"webhookURL": state.WebhookURL,
			"error":      state.Error,
			"onBizApp":   state.OnBizApp,
			"history":    state.History,
		},
		"contacts": contacts,
	})
}

const inboxThreadLimit = 200

// GET /api/whatsapp/inbox/{contact}
func (p *plugin) inboxThread(e *core.RequestEvent) error {
	contact, err := e.App.FindRecordById(CollectionNameContacts, e.Request.PathValue("contact"))
	if err != nil {
		return e.NotFoundError("", err)
	}

	// the newest messages, shown oldest first; by when they were sent, since
	// the synced history is stored long after
	records, err := e.App.FindRecordsByFilter(
		CollectionNameMessages,
		"contact = {:contact}",
		"-sentAt,-created",
		inboxThreadLimit,
		0,
		map[string]any{"contact": contact.Id},
	)
	if err != nil {
		return e.InternalServerError("", err)
	}
	messages := make([]map[string]any, 0, len(records))
	for i := len(records) - 1; i >= 0; i-- {
		r := records[i]
		messages = append(messages, map[string]any{
			"id":        r.Id,
			"direction": r.GetString("direction"),
			"origin":    r.GetString("origin"),
			"messageId": r.GetString("messageId"),
			"type":      r.GetString("type"),
			"text":      r.GetString("text"),
			"template":  r.GetString("template"),
			"mediaId":   r.GetString("mediaId"),
			"mediaMime": r.GetString("mediaMime"),
			"mediaName": r.GetString("mediaName"),
			"replyTo":   r.GetString("replyTo"),
			"status":    r.GetString("status"),
			"error":     r.GetString("error"),
			"sentAt":    r.GetString("sentAt"),
			"created":   r.GetString("created"),
		})
	}

	return e.JSON(http.StatusOK, map[string]any{
		"contact":  toInboxContact(e.App, contact, time.Now()),
		"messages": messages,
	})
}

// POST /api/whatsapp/inbox/{contact}/read
func (p *plugin) inboxRead(e *core.RequestEvent) error {
	contact, err := e.App.FindRecordById(CollectionNameContacts, e.Request.PathValue("contact"))
	if err != nil {
		return e.NotFoundError("", err)
	}
	if contact.GetInt("unread") == 0 {
		return e.NoContent(http.StatusNoContent)
	}

	contact.Set("unread", 0)
	if err := e.App.Save(contact); err != nil {
		return e.InternalServerError("", err)
	}

	// blue ticks for the customer, best effort
	latest, _ := e.App.FindRecordsByFilter(
		CollectionNameMessages,
		"contact = {:contact} && direction = 'in' && origin = ''",
		"-created",
		1,
		0,
		map[string]any{"contact": contact.Id},
	)
	if client, _ := p.velastack(e.App); client != nil && len(latest) == 1 {
		ctx, cancel := context.WithTimeout(e.Request.Context(), 15*time.Second)
		defer cancel()
		if err := client.markRead(ctx, latest[0].GetString("messageId")); err != nil {
			e.App.Logger().Debug("Failed to mark a WhatsApp message as read", "error", err)
		}
	}

	return e.NoContent(http.StatusNoContent)
}

type inboxSendForm struct {
	Text     string `json:"text"`
	Template struct {
		Name     string   `json:"name"`
		Language string   `json:"language"`
		Params   []string `json:"params"`
	} `json:"template"`
}

// POST /api/whatsapp/inbox/{contact}
//
// Sends free text while the customer service window is open, or a template.
func (p *plugin) inboxSend(e *core.RequestEvent) error {
	contact, err := e.App.FindRecordById(CollectionNameContacts, e.Request.PathValue("contact"))
	if err != nil {
		return e.NotFoundError("", err)
	}

	client, err := p.velastack(e.App)
	if err != nil {
		return e.InternalServerError("", err)
	}
	if client == nil {
		return e.BadRequestError("Replying needs the VelaStack sender.", nil)
	}

	form := &inboxSendForm{}
	if err := e.BindBody(form); err != nil {
		return e.BadRequestError("An error occurred while loading the submitted data.", err)
	}
	form.Text = strings.TrimSpace(form.Text)

	to := contact.GetString("phone")
	var message map[string]any
	record := map[string]any{"direction": "out", "status": "accepted", "contact": contact.Id}

	switch {
	case form.Template.Name != "":
		tpl := map[string]any{
			"name":     form.Template.Name,
			"language": map[string]any{"code": form.Template.Language},
		}
		if len(form.Template.Params) > 0 {
			params := make([]map[string]any, 0, len(form.Template.Params))
			for _, v := range form.Template.Params {
				params = append(params, map[string]any{"type": "text", "text": v})
			}
			tpl["components"] = []map[string]any{{"type": "body", "parameters": params}}
		}
		message = map[string]any{"to": to, "type": "template", "template": tpl}
		record["type"] = "template"
		record["template"] = form.Template.Name + "|" + form.Template.Language
		record["text"] = strings.Join(form.Template.Params, " · ")
	case form.Text != "":
		if len([]rune(form.Text)) > 4096 {
			return e.BadRequestError("An error occurred while validating the submitted data.", validation.Errors{
				"text": validation.NewError("validation_length_too_long", "The message can be at most 4096 characters."),
			})
		}
		message = map[string]any{"to": to, "type": "text", "text": map[string]any{"body": form.Text}}
		record["type"] = "text"
		record["text"] = form.Text
	default:
		return e.BadRequestError("Write a message or pick a template.", nil)
	}

	ctx, cancel := context.WithTimeout(e.Request.Context(), 30*time.Second)
	defer cancel()

	messageID, err := client.sendMessage(ctx, "inbox_"+security.PseudorandomString(16), message)
	if err != nil {
		var te *TransportError
		if errors.As(err, &te) && te.Code == "window_closed" {
			return e.JSON(http.StatusUnprocessableEntity, map[string]any{
				"status":  http.StatusUnprocessableEntity,
				"code":    te.Code,
				"message": "The customer last wrote more than 24 hours ago, so WhatsApp only allows a template.",
				"data":    map[string]any{},
			})
		}
		return e.BadRequestError("WhatsApp didn't send the message: "+err.Error(), nil)
	}
	record["messageId"] = messageID

	collection, err := e.App.FindCachedCollectionByNameOrId(CollectionNameMessages)
	if err != nil {
		return e.InternalServerError("", err)
	}
	now := time.Now().UTC()
	sent := core.NewRecord(collection)
	sent.Load(record)
	sent.Set("sentAt", now)
	if err := e.App.Save(sent); err != nil {
		return e.InternalServerError("The message was sent but couldn't be stored.", err)
	}

	contact.Set("lastMessageAt", now)
	contact.Set("lastMessage", preview(sent.GetString("text"), sent.GetString("type")))
	if err := e.App.Save(contact); err != nil {
		return e.InternalServerError("", err)
	}

	return e.JSON(http.StatusOK, map[string]any{"id": sent.Id, "messageId": messageID})
}

// GET /api/whatsapp/inbox/templates
//
// The approved templates a reply can use (sign-in code templates excluded).
func (p *plugin) inboxTemplates(e *core.RequestEvent) error {
	client, err := p.velastack(e.App)
	if err != nil {
		return e.InternalServerError("", err)
	}
	if client == nil {
		return e.JSON(http.StatusOK, map[string]any{"templates": []template{}})
	}

	ctx, cancel := context.WithTimeout(e.Request.Context(), 30*time.Second)
	defer cancel()

	all, err := client.templates(ctx)
	if err != nil {
		return e.BadRequestError("Couldn't load the templates: "+err.Error(), nil)
	}
	approved := []template{}
	for _, t := range all {
		if strings.EqualFold(t.Status, "APPROVED") && !strings.EqualFold(t.Category, "AUTHENTICATION") {
			approved = append(approved, t)
		}
	}

	return e.JSON(http.StatusOK, map[string]any{"templates": approved})
}

// GET /api/whatsapp/inbox/media/{media}
//
// Streams a message attachment from VelaStack, which fetches it from Meta.
// VelaStack only serves media of messages it still keeps (30 days).
func (p *plugin) inboxMedia(e *core.RequestEvent) error {
	mediaID := e.Request.PathValue("media")
	if exists, _ := e.App.FindFirstRecordByData(CollectionNameMessages, "mediaId", mediaID); exists == nil {
		return e.NotFoundError("", nil)
	}

	client, err := p.velastack(e.App)
	if err != nil {
		return e.InternalServerError("", err)
	}
	if client == nil {
		return e.NotFoundError("Media needs the VelaStack sender.", nil)
	}

	ctx, cancel := context.WithTimeout(e.Request.Context(), 90*time.Second)
	defer cancel()

	res, err := client.media(ctx, mediaID)
	if err != nil {
		if transportErrorCode(err) == "not_found" {
			return e.NotFoundError("The media is no longer available.", nil)
		}
		return e.BadRequestError("Couldn't load the media: "+err.Error(), nil)
	}
	defer res.Body.Close()

	header := e.Response.Header()
	for _, h := range []string{"Content-Type", "Content-Length", "Content-Disposition"} {
		if v := res.Header.Get(h); v != "" {
			header.Set(h, v)
		}
	}
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Content-Security-Policy", "sandbox")
	header.Set("Cache-Control", "private, max-age=3600")
	e.Response.WriteHeader(http.StatusOK)
	_, err = io.Copy(e.Response, res.Body)
	return err
}
