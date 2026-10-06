package whatsapp

import (
	"context"
	"log/slog"

	"github.com/pocketbase/pocketbase/core"
)

// DevTransport is a development transport that only logs the auth codes
// in the app logs (console and the superuser UI Logs page).
//
// The codes are never returned in API responses.
type DevTransport struct {
	App core.App
}

// SendAuthCode implements [Transport].
func (t *DevTransport) SendAuthCode(ctx context.Context, m AuthCode) (string, error) {
	t.App.Logger().Info(
		"[WhatsApp dev] auth code for "+m.To+": "+m.Code,
		slog.String("to", m.To),
		slog.String("code", m.Code),
		slog.String("collection", m.Collection),
		slog.String("lang", m.Lang),
	)

	return "dev_" + m.ChallengeID, nil
}

// Status implements [StatusChecker].
func (t *DevTransport) Status(ctx context.Context) (*TransportStatus, error) {
	msg := "Development mode: codes are written to the logs and are not delivered to WhatsApp."
	if !t.App.IsDev() {
		msg += " Users will not receive their codes!"
	}

	return &TransportStatus{OK: true, Message: msg}, nil
}
