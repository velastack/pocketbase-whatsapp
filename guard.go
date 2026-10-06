package whatsapp

import (
	validation "github.com/pocketbase/ozzo-validation/v4"
	"github.com/pocketbase/pocketbase/core"
)

// guardPhoneFields rejects non-superuser writes to the configured
// phone (and phone verified) fields.
//
// Otherwise a user could claim someone else's phone number
// (occupying the unique slot or "pre-hijacking" the WhatsApp login of the
// real owner) so the phone could be set only with the WhatsApp auth or
// the phone change flow.
func (p *plugin) guardPhoneFields(e *core.RecordRequestEvent) error {
	if e.Collection == nil || !e.Collection.IsAuth() || e.HasSuperuserAuth() {
		return e.Next()
	}

	cfg, err := p.loadCollectionConfig(e.App, e.Collection.Id)
	if err != nil {
		return e.InternalServerError("", err)
	}

	if !cfg.Enabled {
		return e.Next()
	}

	errs := validation.Errors{}

	for _, field := range []string{cfg.PhoneField, cfg.PhoneVerifiedField} {
		if field == "" {
			continue
		}

		var changed bool
		if e.Record.IsNew() {
			changed = !isZero(e.Record.Get(field))
		} else {
			changed = e.Record.Get(field) != e.Record.Original().Get(field)
		}

		if changed {
			errs[field] = validation.NewError(
				"validation_whatsapp_phone_locked",
				"The field can be changed only with the WhatsApp phone change flow.",
			)
		}
	}

	if len(errs) > 0 {
		return e.BadRequestError("Failed to save the record.", errs)
	}

	return e.Next()
}

func isZero(v any) bool {
	switch val := v.(type) {
	case nil:
		return true
	case string:
		return val == ""
	case bool:
		return !val
	default:
		return false
	}
}
