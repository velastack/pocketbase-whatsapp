package whatsapp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
	"github.com/pocketbase/pocketbase/tools/types"
)

func init() {
	// send the codes synchronously to simplify the assertions
	asyncSend = func(f func(), _ ...*sync.WaitGroup) { f() }
}

const testMemberPhone = "+16165550100"

// fakeTransport records the sent messages.
type fakeTransport struct {
	mu       sync.Mutex
	messages []AuthCode
	err      error
}

func (f *fakeTransport) SendAuthCode(ctx context.Context, m AuthCode) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.err != nil {
		return "", f.err
	}

	f.messages = append(f.messages, m)

	return "wamid.test" + m.ChallengeID, nil
}

func (f *fakeTransport) sent() []AuthCode {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]AuthCode(nil), f.messages...)
}

func (f *fakeTransport) last(t *testing.T) AuthCode {
	t.Helper()
	msgs := f.sent()
	if len(msgs) == 0 {
		t.Fatal("Expected at least one sent message")
	}
	return msgs[len(msgs)-1]
}

type testEnv struct {
	t              *testing.T
	app            *tests.TestApp
	handler        http.Handler
	transport      *fakeTransport
	superuserToken string
	members        *core.Collection
	member         *core.Record
	memberToken    string
}

type testResponse struct {
	status     int
	retryAfter string
	body       map[string]any
	raw        string
}

// newTestEnv creates a test app with a "members" auth collection
// with enabled WhatsApp auth and a single existing member.
func newTestEnv(t *testing.T, config Config, cfg *CollectionConfig) *testEnv {
	t.Helper()
	return newTestEnvOpts(t, config, cfg, true)
}

// newTestEnvWithoutFake is similar to newTestEnv but uses the UI/env configured transport.
func newTestEnvWithoutFake(t *testing.T, config Config, cfg *CollectionConfig) *testEnv {
	t.Helper()
	return newTestEnvOpts(t, config, cfg, false)
}

func newTestEnvOpts(t *testing.T, config Config, cfg *CollectionConfig, withFake bool) *testEnv {
	t.Helper()

	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Cleanup)

	env := &testEnv{t: t, app: app}

	if config.Transport == nil && withFake {
		env.transport = &fakeTransport{}
		config.Transport = env.transport
	} else if ft, ok := config.Transport.(*fakeTransport); ok {
		env.transport = ft
	}

	if err := Register(app, config); err != nil {
		t.Fatal(err)
	}

	members := core.NewAuthCollection("members")
	members.CreateRule = types.Pointer("")
	members.UpdateRule = types.Pointer("id = @request.auth.id")
	members.PasswordAuth.Enabled = true
	members.Fields.Add(&core.TextField{Name: "phone", Pattern: PhonePattern, Max: 16})
	members.Fields.Add(&core.BoolField{Name: "phoneVerified"})
	members.AddIndex("idx_members_phone", true, "`phone`", "`phone` != ''")
	members.Fields.GetByName(core.FieldNameEmail).(*core.EmailField).Required = false
	if err := app.Save(members); err != nil {
		t.Fatal(err)
	}
	env.members = members

	member := core.NewRecord(members)
	member.SetEmail("member@example.com")
	member.SetPassword("1234567890")
	member.Set("phone", testMemberPhone)
	if err := app.Save(member); err != nil {
		t.Fatal(err)
	}
	env.member = member

	env.memberToken, err = member.NewAuthToken()
	if err != nil {
		t.Fatal(err)
	}

	superuser, err := app.FindAuthRecordByEmail(core.CollectionNameSuperusers, "test@example.com")
	if err != nil {
		t.Fatal(err)
	}
	env.superuserToken, err = superuser.NewAuthToken()
	if err != nil {
		t.Fatal(err)
	}

	baseRouter, err := apis.NewRouter(app)
	if err != nil {
		t.Fatal(err)
	}
	serveEvent := &core.ServeEvent{App: app, Router: baseRouter}
	err = app.OnServe().Trigger(serveEvent, func(e *core.ServeEvent) error {
		mux, err := e.Router.BuildMux()
		env.handler = mux
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	if cfg != nil {
		res := env.do(http.MethodPatch, "/api/whatsapp/collections/members", cfg, env.superuserToken)
		if res.status != http.StatusOK {
			t.Fatalf("Failed to configure the members collection: %d %s", res.status, res.raw)
		}
	}

	return env
}

func defaultTestConfig() *CollectionConfig {
	return &CollectionConfig{
		Enabled:            true,
		PhoneField:         "phone",
		PhoneVerifiedField: "phoneVerified",
		CodeLength:         6,
		Duration:           300,
	}
}

func (env *testEnv) do(method string, url string, body any, token string) testResponse {
	env.t.Helper()

	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			env.t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}

	req := httptest.NewRequest(method, url, reader)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", token)
	}

	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)

	res := testResponse{
		status:     rec.Code,
		retryAfter: rec.Header().Get("Retry-After"),
		raw:        rec.Body.String(),
		body:       map[string]any{},
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &res.body)

	return res
}

// requestCode requests a WhatsApp code and returns the otpId and the sent code.
func (env *testEnv) requestCode(phone string) (string, string) {
	env.t.Helper()

	before := len(env.transport.sent())

	res := env.do(http.MethodPost, "/api/collections/members/request-whatsapp-otp", map[string]any{"phone": phone}, "")
	if res.status != http.StatusOK {
		env.t.Fatalf("Expected 200, got %d: %s", res.status, res.raw)
	}

	otpId, _ := res.body["otpId"].(string)
	if otpId == "" {
		env.t.Fatalf("Missing otpId: %s", res.raw)
	}

	msgs := env.transport.sent()
	if len(msgs) != before+1 {
		env.t.Fatalf("Expected a sent message, got %d new", len(msgs)-before)
	}

	return otpId, msgs[len(msgs)-1].Code
}
