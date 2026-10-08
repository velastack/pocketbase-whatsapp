package whatsapp

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/plugins/migratecmd"
)

func TestAdminRequiresSuperuser(t *testing.T) {
	env := newTestEnv(t, Config{}, nil)

	for _, token := range []string{"", env.memberToken} {
		res := env.do(http.MethodGet, "/api/whatsapp/settings", nil, token)
		if res.status != http.StatusUnauthorized && res.status != http.StatusForbidden {
			t.Fatalf("Expected 401/403, got %d: %s", res.status, res.raw)
		}
	}
}

func TestAdminSettings(t *testing.T) {
	env := newTestEnvWithoutFake(t, Config{}, nil)

	res := env.do(http.MethodGet, "/api/whatsapp/settings", nil, env.superuserToken)
	if res.status != http.StatusOK || !strings.Contains(res.raw, `"mode":""`) {
		t.Fatalf("Expected empty settings, got %d: %s", res.status, res.raw)
	}

	// missing required fields for the selected mode
	res = env.do(http.MethodPatch, "/api/whatsapp/settings", map[string]any{"mode": "direct"}, env.superuserToken)
	if res.status != http.StatusBadRequest || !strings.Contains(res.raw, "phoneNumberId") || !strings.Contains(res.raw, "accessToken") {
		t.Fatalf("Expected 400 validation errors, got %d: %s", res.status, res.raw)
	}

	res = env.do(http.MethodPatch, "/api/whatsapp/settings", map[string]any{
		"mode": "direct",
		"direct": map[string]any{
			"phoneNumberId": "1234",
			"accessToken":   "supersecret",
			"templateName":  "login_code",
			"languages":     []string{"en-US", " es_MX ", "en_US"},
		},
	}, env.superuserToken)
	if res.status != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", res.status, res.raw)
	}
	if strings.Contains(res.raw, "supersecret") || !strings.Contains(res.raw, `"accessToken":"******"`) {
		t.Fatalf("Expected redacted access token, got %s", res.raw)
	}
	if !strings.Contains(res.raw, `"languages":["en_US","es_MX"]`) {
		t.Fatalf("Expected normalized languages, got %s", res.raw)
	}

	// masked secrets are kept on update
	res = env.do(http.MethodPatch, "/api/whatsapp/settings", map[string]any{
		"direct": map[string]any{"accessToken": "******", "templateName": "login_code_v2"},
	}, env.superuserToken)
	if res.status != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", res.status, res.raw)
	}

	p := &plugin{}
	stored, err := p.loadStoredSettings(env.app)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Direct.AccessToken != "supersecret" || stored.Direct.TemplateName != "login_code_v2" || stored.Direct.PhoneNumberID != "1234" {
		t.Fatalf("Unexpected stored settings %+v", stored.Direct)
	}

	// the stored value is never exposed through the records API
	for _, url := range []string{
		"/api/collections/" + CollectionNameStore + "/records",
		"/api/collections/" + CollectionNameStore + "/records?filter=key='settings'",
	} {
		res = env.do(http.MethodGet, url, nil, env.superuserToken)
		if res.status != http.StatusForbidden || strings.Contains(res.raw, "supersecret") {
			t.Fatalf("Expected 403 without the raw store value, got %d: %s", res.status, res.raw)
		}
	}

	record, _ := env.app.FindFirstRecordByData(CollectionNameStore, "key", storeKeySettings)
	res = env.do(http.MethodGet, "/api/collections/"+CollectionNameStore+"/records/"+record.Id, nil, env.superuserToken)
	if res.status != http.StatusForbidden {
		t.Fatalf("Expected 403 on view, got %d: %s", res.status, res.raw)
	}

	res = env.do(http.MethodPatch, "/api/collections/"+CollectionNameStore+"/records/"+record.Id, map[string]any{"value": "{}"}, env.superuserToken)
	if res.status != http.StatusForbidden {
		t.Fatalf("Expected 403 on update, got %d: %s", res.status, res.raw)
	}
}

func TestAdminSettingsEnvAndEncryption(t *testing.T) {
	env := newTestEnvWithoutFake(t, Config{}, nil)

	t.Setenv(env.app.EncryptionEnv(), "abcdefghijklmnopqrstuvwxyz123456")
	t.Setenv("WHATSAPP_MODE", "velastack")
	t.Setenv("VELASTACK_API_KEY", "envkey")

	res := env.do(http.MethodGet, "/api/whatsapp/settings", nil, env.superuserToken)
	if res.status != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", res.status, res.raw)
	}
	if !strings.Contains(res.raw, `"locked":["mode","velastack.apiKey"]`) || !strings.Contains(res.raw, `"mode":"velastack"`) || strings.Contains(res.raw, "envkey") {
		t.Fatalf("Unexpected settings response: %s", res.raw)
	}

	// the env provided api key satisfies the validation
	res = env.do(http.MethodPatch, "/api/whatsapp/settings", map[string]any{"velastack": map[string]any{"forwardClientIP": true}}, env.superuserToken)
	if res.status != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", res.status, res.raw)
	}

	record, err := env.app.FindFirstRecordByData(CollectionNameStore, "key", storeKeySettings)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(record.GetString("value"), encryptedValuePrefix) {
		t.Fatalf("Expected encrypted value, got %q", record.GetString("value"))
	}

	p := &plugin{}
	settings, _, err := p.loadSettings(env.app)
	if err != nil || !settings.Velastack.ForwardClientIP || settings.Velastack.APIKey != "envkey" {
		t.Fatalf("Unexpected decrypted settings %+v (%v)", settings, err)
	}

	tr, err := p.transport(env.app)
	if _, ok := tr.(*RelayTransport); !ok || err != nil {
		t.Fatalf("Expected relay transport, got %T (%v)", tr, err)
	}
}

func TestAdminSettingsModeInferredFromEnv(t *testing.T) {
	env := newTestEnvWithoutFake(t, Config{}, nil)

	// a stored sender that the env credentials take precedence over
	p := &plugin{}
	if err := p.saveStoreValue(env.app, storeKeySettings, &Settings{Mode: ModeDev}, true); err != nil {
		t.Fatal(err)
	}

	t.Setenv("VELASTACK_API_KEY", "envkey")

	res := env.do(http.MethodGet, "/api/whatsapp/settings", nil, env.superuserToken)
	if res.status != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", res.status, res.raw)
	}
	if !strings.Contains(res.raw, `"locked":["mode","velastack.apiKey"]`) || !strings.Contains(res.raw, `"mode":"velastack"`) {
		t.Fatalf("Unexpected settings response: %s", res.raw)
	}

	tr, err := p.transport(env.app)
	if _, ok := tr.(*RelayTransport); !ok || err != nil {
		t.Fatalf("Expected relay transport, got %T (%v)", tr, err)
	}

	// an explicit mode wins over the inference
	t.Setenv("WHATSAPP_MODE", ModeDev)
	settings, locked, err := p.loadSettings(env.app)
	if err != nil || settings.Mode != ModeDev || strings.Join(locked, ",") != "mode,velastack.apiKey" {
		t.Fatalf("Unexpected settings %+v locked %v (%v)", settings, locked, err)
	}

	// the direct token implies the direct sender
	os.Unsetenv("WHATSAPP_MODE")
	os.Unsetenv("VELASTACK_API_KEY")
	t.Setenv("WHATSAPP_ACCESS_TOKEN", "token")
	settings, locked, err = p.loadSettings(env.app)
	if err != nil || settings.Mode != ModeDirect || strings.Join(locked, ",") != "mode,direct.accessToken" {
		t.Fatalf("Unexpected settings %+v locked %v (%v)", settings, locked, err)
	}

	// no env credentials: the stored sender stays
	os.Unsetenv("WHATSAPP_ACCESS_TOKEN")
	settings, locked, err = p.loadSettings(env.app)
	if err != nil || settings.Mode != ModeDev || len(locked) != 0 {
		t.Fatalf("Unexpected settings %+v locked %v (%v)", settings, locked, err)
	}
}

func TestAdminCollections(t *testing.T) {
	env := newTestEnv(t, Config{}, nil)

	res := env.do(http.MethodGet, "/api/whatsapp/collections", nil, env.superuserToken)
	if res.status != http.StatusOK || strings.Contains(res.raw, `"_superusers"`) || !strings.Contains(res.raw, `"name":"members"`) {
		t.Fatalf("Unexpected collections list %d: %s", res.status, res.raw)
	}

	// users has no phone field
	res = env.do(http.MethodPatch, "/api/whatsapp/collections/users", map[string]any{"enabled": true}, env.superuserToken)
	if res.status != http.StatusBadRequest || !strings.Contains(res.raw, "validation_invalid_phone_field") {
		t.Fatalf("Expected 400 phone field error, got %d: %s", res.status, res.raw)
	}

	// the setup changes go through the collection update hooks (automigrate)
	migrationsDir := t.TempDir()
	migratecmd.MustRegister(env.app, nil, migratecmd.Config{Automigrate: true, Dir: migrationsDir})

	// setup creates the phone field with a unique index and makes the email optional
	res = env.do(http.MethodPost, "/api/whatsapp/collections/users/setup", map[string]any{
		"createPhoneField":         true,
		"createPhoneVerifiedField": true,
		"makeEmailOptional":        true,
	}, env.superuserToken)
	if res.status != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", res.status, res.raw)
	}
	if diag, _ := res.body["diagnostics"].(map[string]any); diag["phoneFieldIssue"] != nil || diag["emailRequired"] != false {
		t.Fatalf("Unexpected diagnostics: %s", res.raw)
	}

	users, _ := env.app.FindCollectionByNameOrId("users")
	if users.Fields.GetByName("phone") == nil || users.Fields.GetByName("phoneVerified") == nil {
		t.Fatal("Expected phone fields to be created")
	}
	if users.Fields.GetByName(core.FieldNameEmail).(*core.EmailField).Required {
		t.Fatal("Expected email to be optional")
	}

	migrations, _ := filepath.Glob(filepath.Join(migrationsDir, "*_updated_users.go"))
	if len(migrations) != 1 {
		t.Fatalf("Expected 1 users migration, got %v", migrations)
	}
	if raw, _ := os.ReadFile(migrations[0]); !strings.Contains(string(raw), `"name": "phone"`) || !strings.Contains(string(raw), "UNIQUE INDEX") {
		t.Fatalf("Expected the phone field and index in the migration:\n%s", raw)
	}

	// invalid values
	res = env.do(http.MethodPatch, "/api/whatsapp/collections/users", map[string]any{
		"enabled":             true,
		"codeLength":          2,
		"duration":            99999,
		"allowedCountryCodes": []string{"1", "abc"},
	}, env.superuserToken)
	if res.status != http.StatusBadRequest || !strings.Contains(res.raw, "codeLength") || !strings.Contains(res.raw, "duration") || !strings.Contains(res.raw, "allowedCountryCodes") {
		t.Fatalf("Expected 400 validation errors, got %d: %s", res.status, res.raw)
	}

	res = env.do(http.MethodPatch, "/api/whatsapp/collections/users", map[string]any{
		"enabled":             true,
		"allowSignup":         true,
		"allowedCountryCodes": []string{"+1", "52"},
	}, env.superuserToken)
	if res.status != http.StatusOK || !strings.Contains(res.raw, `"allowedCountryCodes":["1","52"]`) {
		t.Fatalf("Expected 200, got %d: %s", res.status, res.raw)
	}

	// signup requires a non-locked create rule
	users.CreateRule = nil
	if err := env.app.Save(users); err != nil {
		t.Fatal(err)
	}
	res = env.do(http.MethodPatch, "/api/whatsapp/collections/users", map[string]any{"allowSignup": true}, env.superuserToken)
	if res.status != http.StatusBadRequest || !strings.Contains(res.raw, "validation_create_rule_locked") {
		t.Fatalf("Expected 400 create rule error, got %d: %s", res.status, res.raw)
	}

	res = env.do(http.MethodPatch, "/api/whatsapp/collections/_superusers", map[string]any{"enabled": true}, env.superuserToken)
	if res.status != http.StatusNotFound {
		t.Fatalf("Expected 404 for superusers, got %d: %s", res.status, res.raw)
	}
}

func TestAdminSendTest(t *testing.T) {
	env := newTestEnv(t, Config{}, nil)

	res := env.do(http.MethodPost, "/api/whatsapp/test", map[string]any{"phone": "+16165550100"}, env.superuserToken)
	if res.status != http.StatusOK || res.body["messageId"] == "" {
		t.Fatalf("Expected 200, got %d: %s", res.status, res.raw)
	}
	if env.transport.last(t).To != "+16165550100" {
		t.Fatal("Expected the test code to be sent")
	}

	env.transport.err = &TransportError{Code: ErrCodeRecipientNotAllowed, Message: "not in allowed list"}
	res = env.do(http.MethodPost, "/api/whatsapp/test", map[string]any{"phone": "+16165550100"}, env.superuserToken)
	if res.status != http.StatusBadRequest || !strings.Contains(res.raw, "recipient_not_allowed") {
		t.Fatalf("Expected 400 with transport error, got %d: %s", res.status, res.raw)
	}
}

func TestAdminStatusDev(t *testing.T) {
	env := newTestEnvWithoutFake(t, Config{}, nil)

	res := env.do(http.MethodGet, "/api/whatsapp/status", nil, env.superuserToken)
	if res.status != http.StatusOK || !strings.Contains(res.raw, "not configured") {
		t.Fatalf("Unexpected status %d: %s", res.status, res.raw)
	}

	env.do(http.MethodPatch, "/api/whatsapp/settings", map[string]any{"mode": "dev"}, env.superuserToken)

	res = env.do(http.MethodGet, "/api/whatsapp/status", nil, env.superuserToken)
	if res.status != http.StatusOK || !strings.Contains(res.raw, `"ok":true`) || !strings.Contains(res.raw, "Development mode") {
		t.Fatalf("Unexpected dev status %d: %s", res.status, res.raw)
	}
}
