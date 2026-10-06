package whatsapp

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strings"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/security"
)

const (
	storeKeySettings         = "settings"
	storeKeyCollectionPrefix = "collection:"

	encryptedValuePrefix = "enc:"

	// redactedValue is the placeholder returned in place of the stored secrets
	// (the same as the one used by the core settings API).
	redactedValue = "******"
)

// Transport modes.
const (
	ModeOff       = ""
	ModeDev       = "dev"
	ModeVelastack = "velastack"
	ModeDirect    = "direct"
)

// Settings are the instance-wide sender settings
// (managed from the superuser UI "Settings > WhatsApp" page).
type Settings struct {
	Mode      string            `json:"mode"`
	Velastack VelastackSettings `json:"velastack"`
	Direct    DirectSettings    `json:"direct"`
}

// VelastackSettings are the [ModeVelastack] relay transport settings.
type VelastackSettings struct {
	APIKey   string `json:"apiKey"`
	RelayURL string `json:"relayURL"`

	// ForwardClientIP enables sending the end user IP to the relay
	// (used for cross-tenant fraud detection).
	ForwardClientIP bool `json:"forwardClientIP"`
}

// DirectSettings are the [ModeDirect] Cloud API transport settings.
type DirectSettings struct {
	PhoneNumberID string   `json:"phoneNumberId"`
	WABAID        string   `json:"wabaId"`
	AccessToken   string   `json:"accessToken"`
	TemplateName  string   `json:"templateName"`
	Languages     []string `json:"languages"`
	GraphVersion  string   `json:"graphVersion"`
}

// envOverrides maps the env variables to the settings fields they override.
//
// The overridden fields are reported as "locked" in the superuser UI.
var envOverrides = []struct {
	env   string
	field string
	apply func(s *Settings, v string)
}{
	{"WHATSAPP_MODE", "mode", func(s *Settings, v string) { s.Mode = v }},
	{"VELASTACK_API_KEY", "velastack.apiKey", func(s *Settings, v string) { s.Velastack.APIKey = v }},
	{"VELASTACK_RELAY_URL", "velastack.relayURL", func(s *Settings, v string) { s.Velastack.RelayURL = v }},
	{"WHATSAPP_PHONE_NUMBER_ID", "direct.phoneNumberId", func(s *Settings, v string) { s.Direct.PhoneNumberID = v }},
	{"WHATSAPP_WABA_ID", "direct.wabaId", func(s *Settings, v string) { s.Direct.WABAID = v }},
	{"WHATSAPP_ACCESS_TOKEN", "direct.accessToken", func(s *Settings, v string) { s.Direct.AccessToken = v }},
	{"WHATSAPP_TEMPLATE_NAME", "direct.templateName", func(s *Settings, v string) { s.Direct.TemplateName = v }},
	{"WHATSAPP_TEMPLATE_LANGUAGES", "direct.languages", func(s *Settings, v string) { s.Direct.Languages = splitList(v) }},
	{"WHATSAPP_GRAPH_VERSION", "direct.graphVersion", func(s *Settings, v string) { s.Direct.GraphVersion = v }},
}

// applyEnv applies the env overrides and returns the list with the locked fields.
func (s *Settings) applyEnv() []string {
	locked := []string{}

	for _, o := range envOverrides {
		if v, ok := os.LookupEnv(o.env); ok {
			o.apply(s, strings.TrimSpace(v))
			locked = append(locked, o.field)
		}
	}

	return locked
}

// redacted returns a copy of the settings with masked secrets.
func (s Settings) redacted() Settings {
	if s.Velastack.APIKey != "" {
		s.Velastack.APIKey = redactedValue
	}
	if s.Direct.AccessToken != "" {
		s.Direct.AccessToken = redactedValue
	}
	if s.Direct.Languages == nil {
		s.Direct.Languages = []string{}
	}
	return s
}

// CollectionConfig is the WhatsApp auth config of a single auth collection.
type CollectionConfig struct {
	Enabled bool `json:"enabled"`

	// PhoneField is the name of the text field with the E.164 user phone number.
	// It must have a single column UNIQUE index.
	PhoneField string `json:"phoneField"`

	// PhoneVerifiedField is an optional bool field name that is set to true
	// when the phone number ownership is confirmed with a WhatsApp code.
	PhoneVerifiedField string `json:"phoneVerifiedField"`

	// AllowSignup allows creating new auth records on successful
	// WhatsApp auth for unknown phone numbers.
	AllowSignup bool `json:"allowSignup"`

	// CodeLength is the number of the code digits (default to 6).
	CodeLength int `json:"codeLength"`

	// Duration is the code validity duration in seconds (default to 300).
	Duration int `json:"duration"`

	// AllowedCountryCodes is an optional list of allowed country calling codes (eg. "1", "52").
	AllowedCountryCodes []string `json:"allowedCountryCodes"`
}

const maxCodeDuration = 3600

func defaultCollectionConfig() *CollectionConfig {
	return &CollectionConfig{
		PhoneField:          "phone",
		CodeLength:          6,
		Duration:            300,
		AllowedCountryCodes: []string{},
	}
}

func (c *CollectionConfig) normalize() {
	if c.CodeLength == 0 {
		c.CodeLength = 6
	}
	if c.Duration == 0 {
		c.Duration = 300
	}
	codes := []string{}
	for _, code := range c.AllowedCountryCodes {
		code = strings.TrimPrefix(strings.TrimSpace(code), "+")
		if code != "" {
			codes = append(codes, code)
		}
	}
	c.AllowedCountryCodes = codes
}

// -------------------------------------------------------------------
// store helpers
// -------------------------------------------------------------------

func (p *plugin) loadStoreValue(app core.App, key string, dst any) (bool, error) {
	record, err := app.FindFirstRecordByData(CollectionNameStore, "key", key)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}

	raw := record.GetString("value")
	if strings.HasPrefix(raw, encryptedValuePrefix) {
		decrypted, err := security.Decrypt(strings.TrimPrefix(raw, encryptedValuePrefix), p.encryptionKey(app))
		if err != nil {
			return false, errors.New("failed to decrypt the stored WhatsApp settings (has the encryption key changed?)")
		}
		raw = string(decrypted)
	}

	if raw == "" {
		return false, nil
	}

	return true, json.Unmarshal([]byte(raw), dst)
}

func (p *plugin) saveStoreValue(app core.App, key string, value any, encrypt bool) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}

	str := string(raw)
	if encrypt {
		if k := p.encryptionKey(app); k != "" {
			encrypted, err := security.Encrypt(raw, k)
			if err != nil {
				return err
			}
			str = encryptedValuePrefix + encrypted
		}
	}

	record, err := app.FindFirstRecordByData(CollectionNameStore, "key", key)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}

		collection, err := app.FindCachedCollectionByNameOrId(CollectionNameStore)
		if err != nil {
			return err
		}
		record = core.NewRecord(collection)
		record.Set("key", key)
	}

	record.Set("value", str)

	return app.Save(record)
}

func (p *plugin) deleteStoreValue(app core.App, key string) error {
	_, err := app.DB().Delete(CollectionNameStore, dbx.HashExp{"key": key}).Execute()
	return err
}

// encryptionKey returns the app encryption key (the same one used for the core settings).
func (p *plugin) encryptionKey(app core.App) string {
	return os.Getenv(app.EncryptionEnv())
}

// loadSettings loads the stored settings without the env overrides.
func (p *plugin) loadStoredSettings(app core.App) (*Settings, error) {
	s := &Settings{}
	if _, err := p.loadStoreValue(app, storeKeySettings, s); err != nil {
		return nil, err
	}
	return s, nil
}

// loadSettings loads the effective settings (stored + env overrides).
func (p *plugin) loadSettings(app core.App) (*Settings, []string, error) {
	s, err := p.loadStoredSettings(app)
	if err != nil {
		return nil, nil, err
	}

	locked := s.applyEnv()

	return s, locked, nil
}

func (p *plugin) loadCollectionConfig(app core.App, collectionId string) (*CollectionConfig, error) {
	cfg := defaultCollectionConfig()
	if _, err := p.loadStoreValue(app, storeKeyCollectionPrefix+collectionId, cfg); err != nil {
		return nil, err
	}
	cfg.normalize()
	return cfg, nil
}

func (p *plugin) saveCollectionConfig(app core.App, collectionId string, cfg *CollectionConfig) error {
	cfg.normalize()
	return p.saveStoreValue(app, storeKeyCollectionPrefix+collectionId, cfg, false)
}

func splitList(v string) []string {
	result := []string{}
	for _, part := range strings.Split(v, ",") {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	return result
}
