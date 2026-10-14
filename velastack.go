package whatsapp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase/core"
)

// The VelaStack relay calls behind incoming messages and the inbox. They use
// the same base url and project key as the code delivery ([RelayTransport]).
//
//	GET    {URL}/messages?direction=in&after=<cursor>&limit=   → {data: [...], next}
//	POST   {URL}/messages                 (Cloud API message, Idempotency-Key) → 202 {messageId}
//	GET    {URL}/messages/{messageId}     → message
//	POST   {URL}/messages/{messageId}/read {typing?}
//	GET    {URL}/media/{mediaId}          → the file
//	GET    {URL}/templates                → {data: [...]}
//	GET    {URL}/webhook                  → {url, delivery}
//	PUT    {URL}/webhook {url, rotate?}   → {url, secret?}

// velastack returns the relay client when the sender is VelaStack
// (nil otherwise, incoming messages need it).
func (p *plugin) velastack(app core.App) (*RelayTransport, error) {
	settings, _, err := p.loadSettings(app)
	if err != nil {
		return nil, err
	}
	if settings.Mode != ModeVelastack || settings.Velastack.APIKey == "" {
		return nil, nil
	}
	return &RelayTransport{
		URL:        settings.Velastack.RelayURL,
		APIKey:     settings.Velastack.APIKey,
		HTTPClient: p.config.HTTPClient,
	}, nil
}

// keyID is the id part of a `pk_<id>.<secret>` project key.
func keyID(apiKey string) string {
	id, _, _ := strings.Cut(apiKey, ".")
	return id
}

type listedMessage struct {
	inboundMessage
	Direction string `json:"direction"`
	Status    string `json:"status"`
	Error     string `json:"error"`
}

type messagePage struct {
	Data []listedMessage `json:"data"`
	Next string          `json:"next"`
}

func (t *RelayTransport) listInbound(ctx context.Context, after string, limit int) (*messagePage, error) {
	q := url.Values{"direction": {"in"}, "limit": {strconv.Itoa(limit)}}
	if after != "" {
		q.Set("after", after)
	}
	page := &messagePage{}
	return page, t.do(ctx, http.MethodGet, "/messages?"+q.Encode(), nil, nil, page)
}

func (t *RelayTransport) getMessage(ctx context.Context, messageID string) (*listedMessage, error) {
	m := &listedMessage{}
	return m, t.do(ctx, http.MethodGet, "/messages/"+url.PathEscape(messageID), nil, nil, m)
}

func (t *RelayTransport) sendMessage(ctx context.Context, idempotencyKey string, message map[string]any) (string, error) {
	result := struct {
		MessageID string `json:"messageId"`
	}{}
	err := t.do(ctx, http.MethodPost, "/messages", map[string]string{"Idempotency-Key": idempotencyKey}, message, &result)
	return result.MessageID, err
}

func (t *RelayTransport) markRead(ctx context.Context, messageID string) error {
	return t.do(ctx, http.MethodPost, "/messages/"+url.PathEscape(messageID)+"/read", nil, map[string]any{}, nil)
}

type template struct {
	Name       string           `json:"name"`
	Language   string           `json:"language"`
	Status     string           `json:"status"`
	Category   string           `json:"category"`
	Components []map[string]any `json:"components"`
}

func (t *RelayTransport) templates(ctx context.Context) ([]template, error) {
	result := struct {
		Data []template `json:"data"`
	}{}
	return result.Data, t.do(ctx, http.MethodGet, "/templates", nil, nil, &result)
}

type webhookInfo struct {
	URL      string         `json:"url"`
	Secret   string         `json:"secret"`
	Delivery map[string]any `json:"delivery"`
}

func (t *RelayTransport) getWebhook(ctx context.Context) (*webhookInfo, error) {
	info := &webhookInfo{}
	return info, t.do(ctx, http.MethodGet, "/webhook", nil, nil, info)
}

func (t *RelayTransport) putWebhook(ctx context.Context, webhookURL string, rotate bool) (*webhookInfo, error) {
	info := &webhookInfo{}
	body := map[string]any{"url": webhookURL}
	if rotate {
		body["rotate"] = true
	}
	return info, t.do(ctx, http.MethodPut, "/webhook", nil, body, info)
}

// media opens a media download; the caller closes the body.
func (t *RelayTransport) media(ctx context.Context, mediaID string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.baseURL()+"/media/"+url.PathEscape(mediaID), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+t.APIKey)

	res, err := t.client().Do(req)
	if err != nil {
		return nil, &TransportError{Code: ErrCodeUnavailable, Message: err.Error()}
	}
	if res.StatusCode >= 400 {
		defer res.Body.Close()
		te := &TransportError{}
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<16))
		if json.Unmarshal(raw, te) != nil || te.Code == "" {
			te.Code = relayStatusCode(res.StatusCode)
		}
		if res.StatusCode == http.StatusNotFound {
			te.Code = "not_found"
		}
		return nil, te
	}
	return res, nil
}

// -------------------------------------------------------------------
// receiving: webhook registration and polling
// -------------------------------------------------------------------

// webhookPath is where VelaStack posts the events.
const webhookPath = "/api/whatsapp/webhook"

// publicHTTPS reports whether VelaStack could deliver webhooks to the app url.
func publicHTTPS(appURL string) bool {
	u, err := url.Parse(appURL)
	if err != nil || u.Scheme != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") ||
		strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".test") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified())
	}
	return strings.Contains(host, ".")
}

// syncIncoming decides how messages arrive and registers the webhook with
// VelaStack when the app is reachable. It runs when the server starts and
// after the settings change.
//
// Each instance must use its own project key: a second public instance with
// the same key takes the webhook over.
func (p *plugin) syncIncoming(app core.App) error {
	p.incomingMu.Lock()
	defer p.incomingMu.Unlock()

	state, err := p.loadIncomingState(app)
	if err != nil {
		return err
	}

	client, err := p.velastack(app)
	if err != nil {
		return err
	}

	if client == nil && state.Mode == IncomingOff {
		return nil
	}

	state.Error = ""
	state.SyncedAt = time.Now().UTC().Format(time.RFC3339)

	switch {
	case client == nil:
		state.Mode = IncomingOff

	case !publicHTTPS(app.Settings().Meta.AppURL):
		state.Mode = IncomingPolling
		state.WebhookURL = ""

	default:
		webhookURL := strings.TrimRight(app.Settings().Meta.AppURL, "/") + webhookPath
		// Always ask for a new secret: the plugin owns this URL, and the one it
		// kept may be stale (another key, or rolled in the VelaStack
		// dashboard). VelaStack re-reads the secret for each delivery attempt.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		info, err := client.putWebhook(ctx, webhookURL, true)
		if err == nil && info.Secret == "" {
			err = errors.New("VelaStack returned no signing secret")
		}
		if err != nil {
			// keep receiving by polling until the webhook can be registered
			state.Mode = IncomingPolling
			state.Error = "Couldn't register the webhook with VelaStack: " + err.Error()
			break
		}
		state.Secret = info.Secret
		state.KeyID = keyID(client.APIKey)
		state.Mode = IncomingWebhook
		state.WebhookURL = webhookURL
	}

	return p.saveIncomingState(app, state)
}

const pollPageSize = 100

// pollOnce fetches the messages that arrived since the last poll, and
// refreshes the statuses of recent inbox sends.
//
// It also runs, less often, when VelaStack delivers webhooks: a registered
// URL can still be unreachable (eg. a proxy in front of PocketBase that
// doesn't forward it), and both paths deduplicate by message id.
func (p *plugin) pollOnce(app core.App) error {
	p.incomingMu.Lock()
	defer p.incomingMu.Unlock()

	state, err := p.loadIncomingState(app)
	if err != nil || state.Mode == IncomingOff {
		return err
	}
	client, err := p.velastack(app)
	if err != nil || client == nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	for range 20 {
		page, err := client.listInbound(ctx, state.Cursor, pollPageSize)
		if err != nil {
			var te *TransportError
			// a cursor VelaStack no longer knows (eg. a new key): start over
			if errors.As(err, &te) && te.Code == "invalid_request" && state.Cursor != "" {
				state.Cursor = ""
				continue
			}
			return err
		}
		for _, m := range page.Data {
			if _, err := p.ingestInbound(app, "", m.inboundMessage); err != nil {
				app.Logger().Warn("Failed to store an incoming WhatsApp message", "messageId", m.MessageID, "error", err)
			}
		}
		if page.Next != "" && page.Next != state.Cursor {
			state.Cursor = page.Next
			if err := p.saveIncomingState(app, state); err != nil {
				return err
			}
		}
		if len(page.Data) < pollPageSize {
			break
		}
	}

	pending, err := recentPendingOutbound(app, 20)
	if err != nil {
		return err
	}
	for _, record := range pending {
		m, err := client.getMessage(ctx, record.GetString("messageId"))
		if err != nil {
			continue
		}
		u := statusUpdate{MessageID: m.MessageID, Status: m.Status}
		if m.Error != "" {
			u.Errors = append(u.Errors, struct {
				Code    int    `json:"code"`
				Title   string `json:"title"`
				Message string `json:"message"`
			}{Message: m.Error})
		}
		if err := p.applyStatus(app, u); err != nil {
			return err
		}
	}

	return nil
}
