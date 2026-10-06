package whatsapp

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	validation "github.com/pocketbase/ozzo-validation/v4"
	"github.com/pocketbase/ozzo-validation/v4/is"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/dbutils"
	"github.com/pocketbase/pocketbase/tools/security"
)

// PhonePattern is the regex pattern used for the phone field created with the setup helper.
const PhonePattern = `^\+[1-9][0-9]{7,14}$`

var callingCodeRegex = regexp.MustCompile(`^[1-9][0-9]{0,3}$`)

// -------------------------------------------------------------------
// settings
// -------------------------------------------------------------------

type settingsResponse struct {
	Settings        Settings `json:"settings"`
	Locked          []string `json:"locked"`
	CustomTransport bool     `json:"customTransport"`
	Encrypted       bool     `json:"encrypted"`
	IsDev           bool     `json:"isDev"`
}

func (p *plugin) settingsResponse(e *core.RequestEvent) (*settingsResponse, error) {
	settings, locked, err := p.loadSettings(e.App)
	if err != nil {
		return nil, err
	}

	return &settingsResponse{
		Settings:        settings.redacted(),
		Locked:          locked,
		CustomTransport: p.config.Transport != nil,
		Encrypted:       p.encryptionKey(e.App) != "",
		IsDev:           e.App.IsDev(),
	}, nil
}

func (p *plugin) getSettings(e *core.RequestEvent) error {
	result, err := p.settingsResponse(e)
	if err != nil {
		return e.InternalServerError("Failed to load the WhatsApp settings.", err)
	}

	return e.JSON(http.StatusOK, result)
}

func (p *plugin) updateSettings(e *core.RequestEvent) error {
	stored, err := p.loadStoredSettings(e.App)
	if err != nil {
		return e.InternalServerError("Failed to load the WhatsApp settings.", err)
	}

	original := *stored

	// partial update (missing keys keep their stored values)
	if err := e.BindBody(stored); err != nil {
		return e.BadRequestError("An error occurred while loading the submitted data.", err)
	}

	// masked secrets are never stored
	if stored.Velastack.APIKey == redactedValue {
		stored.Velastack.APIKey = original.Velastack.APIKey
	}
	if stored.Direct.AccessToken == redactedValue {
		stored.Direct.AccessToken = original.Direct.AccessToken
	}

	stored.Direct.Languages = normalizeLanguages(stored.Direct.Languages)

	// validate the effective settings (env overrides could provide some of the required values)
	effective := *stored
	effective.Direct.Languages = slices.Clone(stored.Direct.Languages)
	effective.applyEnv()

	if err := validateSettings(&effective); err != nil {
		return e.BadRequestError("An error occurred while validating the submitted data.", err)
	}

	if err := p.saveStoreValue(e.App, storeKeySettings, stored, true); err != nil {
		return e.BadRequestError("Failed to save the WhatsApp settings.", err)
	}

	return p.getSettings(e)
}

func validateSettings(s *Settings) error {
	return validation.ValidateStruct(s,
		validation.Field(&s.Mode, validation.In(ModeOff, ModeDev, ModeVelastack, ModeDirect)),
		validation.Field(&s.Velastack, validation.By(func(any) error {
			if s.Mode != ModeVelastack {
				return nil
			}
			return validation.ValidateStruct(&s.Velastack,
				validation.Field(&s.Velastack.APIKey, validation.Required),
				validation.Field(&s.Velastack.RelayURL, is.URL),
			)
		})),
		validation.Field(&s.Direct, validation.By(func(any) error {
			if s.Mode != ModeDirect {
				return nil
			}
			return validation.ValidateStruct(&s.Direct,
				validation.Field(&s.Direct.PhoneNumberID, validation.Required, is.Digit),
				validation.Field(&s.Direct.WABAID, is.Digit),
				validation.Field(&s.Direct.AccessToken, validation.Required),
				validation.Field(&s.Direct.TemplateName, validation.Required, validation.Match(regexp.MustCompile(`^[a-z0-9_]+$`))),
				validation.Field(&s.Direct.GraphVersion, validation.Match(regexp.MustCompile(`^v\d+\.\d+$`))),
			)
		})),
	)
}

func normalizeLanguages(langs []string) []string {
	result := []string{}
	for _, l := range langs {
		l = strings.ReplaceAll(strings.TrimSpace(l), "-", "_")
		if l != "" && !slices.Contains(result, l) {
			result = append(result, l)
		}
	}
	return result
}

// -------------------------------------------------------------------
// status and test send
// -------------------------------------------------------------------

func (p *plugin) getStatus(e *core.RequestEvent) error {
	settings, _, err := p.loadSettings(e.App)
	if err != nil {
		return e.InternalServerError("Failed to load the WhatsApp settings.", err)
	}

	mode := settings.Mode
	if p.config.Transport != nil {
		mode = "custom"
	}

	transport, err := p.transport(e.App)
	if err != nil {
		return e.InternalServerError("", err)
	}

	status := &TransportStatus{Message: "WhatsApp delivery is not configured."}

	if transport != nil {
		if checker, ok := transport.(StatusChecker); ok {
			ctx, cancel := context.WithTimeout(e.Request.Context(), 15*time.Second)
			defer cancel()

			status, err = checker.Status(ctx)
			if err != nil {
				status = &TransportStatus{Message: err.Error()}
			}
		} else {
			status = &TransportStatus{OK: true, Message: "Custom transport (no status check available)."}
		}
	}

	return e.JSON(http.StatusOK, map[string]any{
		"mode":   mode,
		"status": status,
	})
}

func (p *plugin) sendTest(e *core.RequestEvent) error {
	form := &requestCodeForm{}
	if err := e.BindBody(form); err != nil {
		return e.BadRequestError("An error occurred while loading the submitted data.", err)
	}

	phone, err := NormalizePhone(form.Phone)
	if err != nil {
		return e.BadRequestError("An error occurred while validating the submitted data.", validation.Errors{
			"phone": validation.NewError("validation_invalid_phone", err.Error()),
		})
	}

	transport, err := p.transport(e.App)
	if err != nil {
		return e.InternalServerError("", err)
	}
	if transport == nil {
		return e.BadRequestError("WhatsApp delivery is not configured.", nil)
	}

	ctx, cancel := context.WithTimeout(e.Request.Context(), 30*time.Second)
	defer cancel()

	messageId, err := transport.SendAuthCode(ctx, AuthCode{
		To:          phone,
		Code:        security.RandomStringWithAlphabet(6, "0123456789"),
		Lang:        form.Lang,
		TTL:         5 * time.Minute,
		AppName:     e.App.Settings().Meta.AppName,
		AppURL:      e.App.Settings().Meta.AppURL,
		ChallengeID: "test_" + security.PseudorandomString(10),
	})
	if err != nil {
		var te *TransportError
		if errors.As(err, &te) {
			return e.BadRequestError("Failed to send the test code: "+te.Error(), nil)
		}
		return e.BadRequestError("Failed to send the test code: "+err.Error(), nil)
	}

	return e.JSON(http.StatusOK, map[string]any{"messageId": messageId})
}

// -------------------------------------------------------------------
// collection configs
// -------------------------------------------------------------------

type collectionDiagnostics struct {
	PhoneFieldIssue         string `json:"phoneFieldIssue,omitempty"`
	PhoneVerifiedFieldIssue string `json:"phoneVerifiedFieldIssue,omitempty"`
	EmailRequired           bool   `json:"emailRequired"`
	CreateRuleLocked        bool   `json:"createRuleLocked"`
	MFAEnabled              bool   `json:"mfaEnabled"`
}

type collectionItem struct {
	Id          string                `json:"id"`
	Name        string                `json:"name"`
	Config      *CollectionConfig     `json:"config"`
	Diagnostics collectionDiagnostics `json:"diagnostics"`
	TextFields  []string              `json:"textFields"`
	BoolFields  []string              `json:"boolFields"`
}

func (p *plugin) collectionItem(app core.App, collection *core.Collection) (*collectionItem, error) {
	cfg, err := p.loadCollectionConfig(app, collection.Id)
	if err != nil {
		return nil, err
	}

	item := &collectionItem{
		Id:         collection.Id,
		Name:       collection.Name,
		Config:     cfg,
		TextFields: []string{},
		BoolFields: []string{},
	}

	for _, f := range collection.Fields {
		switch f.(type) {
		case *core.TextField:
			if f.GetName() != core.FieldNameId && f.GetName() != core.FieldNameTokenKey {
				item.TextFields = append(item.TextFields, f.GetName())
			}
		case *core.BoolField:
			if f.GetName() != core.FieldNameVerified && f.GetName() != core.FieldNameEmailVisibility {
				item.BoolFields = append(item.BoolFields, f.GetName())
			}
		}
	}

	item.Diagnostics.PhoneFieldIssue = phoneFieldIssue(collection, cfg.PhoneField)
	item.Diagnostics.PhoneVerifiedFieldIssue = phoneVerifiedFieldIssue(collection, cfg.PhoneVerifiedField)
	item.Diagnostics.CreateRuleLocked = collection.CreateRule == nil
	item.Diagnostics.MFAEnabled = collection.MFA.Enabled
	if email, ok := collection.Fields.GetByName(core.FieldNameEmail).(*core.EmailField); ok {
		item.Diagnostics.EmailRequired = email.Required
	}

	return item, nil
}

func phoneFieldIssue(collection *core.Collection, name string) string {
	if name == "" {
		return "Missing phone field."
	}

	field := collection.Fields.GetByName(name)
	if field == nil {
		return "The field \"" + name + "\" doesn't exist."
	}

	if _, ok := field.(*core.TextField); !ok {
		return "The field \"" + name + "\" must be a text field."
	}

	if _, ok := dbutils.FindSingleColumnUniqueIndex(collection.Indexes, name); !ok {
		return "The field \"" + name + "\" must have a UNIQUE index."
	}

	return ""
}

func phoneVerifiedFieldIssue(collection *core.Collection, name string) string {
	if name == "" {
		return ""
	}

	field := collection.Fields.GetByName(name)
	if field == nil {
		return "The field \"" + name + "\" doesn't exist."
	}

	if _, ok := field.(*core.BoolField); !ok {
		return "The field \"" + name + "\" must be a bool field."
	}

	return ""
}

func validateCollectionConfig(collection *core.Collection, cfg *CollectionConfig) error {
	return validation.ValidateStruct(cfg,
		validation.Field(&cfg.PhoneField, validation.When(cfg.Enabled, validation.Required, validation.By(func(any) error {
			if issue := phoneFieldIssue(collection, cfg.PhoneField); issue != "" {
				return validation.NewError("validation_invalid_phone_field", issue)
			}
			return nil
		}))),
		validation.Field(&cfg.PhoneVerifiedField, validation.By(func(any) error {
			if cfg.PhoneVerifiedField != "" && cfg.PhoneVerifiedField == cfg.PhoneField {
				return validation.NewError("validation_same_field", "Must be different from the phone field.")
			}
			if issue := phoneVerifiedFieldIssue(collection, cfg.PhoneVerifiedField); issue != "" {
				return validation.NewError("validation_invalid_phone_verified_field", issue)
			}
			return nil
		})),
		validation.Field(&cfg.AllowSignup, validation.By(func(any) error {
			if !cfg.Enabled || !cfg.AllowSignup {
				return nil
			}
			if collection.CreateRule == nil {
				return validation.NewError("validation_create_rule_locked", "Signup requires a non-locked (superusers only) collection create API rule.")
			}
			if email, ok := collection.Fields.GetByName(core.FieldNameEmail).(*core.EmailField); ok && email.Required {
				return validation.NewError("validation_email_required", "Signup requires the email field to be optional.")
			}
			return nil
		})),
		validation.Field(&cfg.CodeLength, validation.Min(4), validation.Max(10)),
		validation.Field(&cfg.Duration, validation.Min(30), validation.Max(maxCodeDuration)),
		validation.Field(&cfg.AllowedCountryCodes, validation.Each(validation.Match(callingCodeRegex).Error("Must be a country calling code (eg. 1, 52, 244)."))),
	)
}

func (p *plugin) authCollectionFromPath(e *core.RequestEvent) (*core.Collection, error) {
	collection, err := e.App.FindCollectionByNameOrId(e.Request.PathValue("collection"))
	if err != nil || !collection.IsAuth() || collection.Name == core.CollectionNameSuperusers {
		return nil, e.NotFoundError("Missing or invalid auth collection.", err)
	}
	return collection, nil
}

func (p *plugin) listCollections(e *core.RequestEvent) error {
	collections, err := e.App.FindAllCollections(core.CollectionTypeAuth)
	if err != nil {
		return e.InternalServerError("", err)
	}

	items := []*collectionItem{}
	for _, c := range collections {
		if c.Name == core.CollectionNameSuperusers {
			continue
		}

		item, err := p.collectionItem(e.App, c)
		if err != nil {
			return e.InternalServerError("", err)
		}
		items = append(items, item)
	}

	slices.SortFunc(items, func(a, b *collectionItem) int { return strings.Compare(a.Name, b.Name) })

	return e.JSON(http.StatusOK, items)
}

func (p *plugin) updateCollectionConfig(e *core.RequestEvent) error {
	collection, err := p.authCollectionFromPath(e)
	if err != nil {
		return err
	}

	cfg, err := p.loadCollectionConfig(e.App, collection.Id)
	if err != nil {
		return e.InternalServerError("", err)
	}

	if err := e.BindBody(cfg); err != nil {
		return e.BadRequestError("An error occurred while loading the submitted data.", err)
	}
	cfg.normalize()

	if err := validateCollectionConfig(collection, cfg); err != nil {
		return e.BadRequestError("An error occurred while validating the submitted data.", err)
	}

	if err := p.saveCollectionConfig(e.App, collection.Id, cfg); err != nil {
		return e.BadRequestError("Failed to save the WhatsApp config.", err)
	}

	item, err := p.collectionItem(e.App, collection)
	if err != nil {
		return e.InternalServerError("", err)
	}

	return e.JSON(http.StatusOK, item)
}

// setupCollection applies the common schema changes required by WhatsApp auth.
func (p *plugin) setupCollection(e *core.RequestEvent) error {
	collection, err := p.authCollectionFromPath(e)
	if err != nil {
		return err
	}

	form := struct {
		CreatePhoneField         bool `json:"createPhoneField"`
		CreatePhoneVerifiedField bool `json:"createPhoneVerifiedField"`
		MakeEmailOptional        bool `json:"makeEmailOptional"`
	}{}
	if err := e.BindBody(&form); err != nil {
		return e.BadRequestError("An error occurred while loading the submitted data.", err)
	}

	cfg, err := p.loadCollectionConfig(e.App, collection.Id)
	if err != nil {
		return e.InternalServerError("", err)
	}

	err = e.App.RunInTransaction(func(txApp core.App) error {
		if form.CreatePhoneField {
			name := "phone"
			if collection.Fields.GetByName(name) == nil {
				collection.Fields.Add(&core.TextField{
					Name:    name,
					Pattern: PhonePattern,
					Max:     16,
				})
			}
			if _, ok := dbutils.FindSingleColumnUniqueIndex(collection.Indexes, name); !ok {
				collection.AddIndex("idx_"+security.PseudorandomString(10), true, "`"+name+"`", "`"+name+"` != ''")
			}
			cfg.PhoneField = name
		}

		if form.CreatePhoneVerifiedField {
			name := "phoneVerified"
			if collection.Fields.GetByName(name) == nil {
				collection.Fields.Add(&core.BoolField{Name: name})
			}
			cfg.PhoneVerifiedField = name
		}

		if form.MakeEmailOptional {
			if email, ok := collection.Fields.GetByName(core.FieldNameEmail).(*core.EmailField); ok {
				email.Required = false
			}
		}

		if err := txApp.Save(collection); err != nil {
			return err
		}

		return p.saveCollectionConfig(txApp, collection.Id, cfg)
	})
	if err != nil {
		return e.BadRequestError("Failed to update the collection.", err)
	}

	item, err := p.collectionItem(e.App, collection)
	if err != nil {
		return e.InternalServerError("", err)
	}

	return e.JSON(http.StatusOK, item)
}
