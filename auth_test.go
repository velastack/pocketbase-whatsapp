package whatsapp

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
)

func noCooldown() Config {
	return Config{Limits: Limits{ResendCooldown: time.Nanosecond}}
}

func TestRequestOTPDisabled(t *testing.T) {
	env := newTestEnv(t, Config{}, nil)

	res := env.do(http.MethodPost, "/api/collections/members/request-whatsapp-otp", map[string]any{"phone": testMemberPhone}, "")
	if res.status != http.StatusForbidden {
		t.Fatalf("Expected 403, got %d: %s", res.status, res.raw)
	}

	res = env.do(http.MethodPost, "/api/collections/_superusers/request-whatsapp-otp", map[string]any{"phone": testMemberPhone}, "")
	if res.status != http.StatusForbidden {
		t.Fatalf("Expected 403 for superusers, got %d: %s", res.status, res.raw)
	}

	res = env.do(http.MethodPost, "/api/collections/missing/request-whatsapp-otp", map[string]any{"phone": testMemberPhone}, "")
	if res.status != http.StatusNotFound {
		t.Fatalf("Expected 404, got %d: %s", res.status, res.raw)
	}
}

func TestRequestOTPWithoutTransport(t *testing.T) {
	env := newTestEnvWithoutFake(t, Config{}, defaultTestConfig())

	res := env.do(http.MethodPost, "/api/collections/members/request-whatsapp-otp", map[string]any{"phone": testMemberPhone}, "")
	if res.status != http.StatusServiceUnavailable {
		t.Fatalf("Expected 503, got %d: %s", res.status, res.raw)
	}

	res = env.do(http.MethodGet, "/api/collections/members/auth-methods", nil, "")
	if wa, _ := res.body["whatsapp"].(map[string]any); wa["enabled"] != false {
		t.Fatalf("Expected whatsapp to be reported as disabled without a transport: %s", res.raw)
	}
}

func TestRequestOTPValidation(t *testing.T) {
	env := newTestEnv(t, Config{}, &CollectionConfig{
		Enabled:             true,
		PhoneField:          "phone",
		AllowedCountryCodes: []string{"1"},
	})

	scenarios := []struct {
		phone string
		code  string
	}{
		{"", "validation_invalid_phone"},
		{"6165550100", "validation_invalid_phone"},
		{"+1abc", "validation_invalid_phone"},
		{"+52 55 1234 5678", "validation_country_not_allowed"},
	}

	for _, s := range scenarios {
		res := env.do(http.MethodPost, "/api/collections/members/request-whatsapp-otp", map[string]any{"phone": s.phone}, "")
		if res.status != http.StatusBadRequest || !strings.Contains(res.raw, s.code) {
			t.Errorf("[%s] Expected 400 with %s, got %d: %s", s.phone, s.code, res.status, res.raw)
		}
	}

	if n := len(env.transport.sent()); n != 0 {
		t.Fatalf("Expected no sent messages, got %d", n)
	}
}

func TestRequestOTPUnknownPhoneWithoutSignup(t *testing.T) {
	env := newTestEnv(t, Config{}, defaultTestConfig())

	res := env.do(http.MethodPost, "/api/collections/members/request-whatsapp-otp", map[string]any{"phone": "+16165550199"}, "")
	if res.status != http.StatusOK || res.body["otpId"] == "" {
		t.Fatalf("Expected 200 with dummy otpId, got %d: %s", res.status, res.raw)
	}

	if n := len(env.transport.sent()); n != 0 {
		t.Fatalf("Expected no sent messages, got %d", n)
	}

	total, _ := env.app.CountRecords(CollectionNameOTPs)
	if total != 0 {
		t.Fatalf("Expected no stored challenges, got %d", total)
	}
}

func TestRequestOTPLimits(t *testing.T) {
	env := newTestEnv(t, Config{Limits: Limits{MaxRequestsPerIP: 3}}, defaultTestConfig())

	env.requestCode(testMemberPhone)

	// cooldown applies to unknown numbers too (no enumeration oracle)
	for _, phone := range []string{testMemberPhone} {
		res := env.do(http.MethodPost, "/api/collections/members/request-whatsapp-otp", map[string]any{"phone": phone}, "")
		if res.status != http.StatusTooManyRequests {
			t.Fatalf("[%s] Expected 429 cooldown, got %d: %s", phone, res.status, res.raw)
		}
		if res.retryAfter == "" {
			t.Fatalf("[%s] Expected Retry-After header", phone)
		}
	}

	// the IP limit counts every request (incl. the rejected ones above)
	res := env.do(http.MethodPost, "/api/collections/members/request-whatsapp-otp", map[string]any{"phone": "+16165550111"}, "")
	if res.status != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", res.status, res.raw)
	}

	res = env.do(http.MethodPost, "/api/collections/members/request-whatsapp-otp", map[string]any{"phone": "+16165550122"}, "")
	if res.status != http.StatusTooManyRequests {
		t.Fatalf("Expected 429 IP limit, got %d: %s", res.status, res.raw)
	}
}

func TestRequestOTPDailyCap(t *testing.T) {
	cfg := noCooldown()
	cfg.Limits.MaxRequestsPerPhone = 2
	env := newTestEnv(t, cfg, defaultTestConfig())

	env.requestCode(testMemberPhone)
	env.requestCode(testMemberPhone)

	res := env.do(http.MethodPost, "/api/collections/members/request-whatsapp-otp", map[string]any{"phone": testMemberPhone}, "")
	if res.status != http.StatusTooManyRequests {
		t.Fatalf("Expected 429, got %d: %s", res.status, res.raw)
	}
}

func TestRequestOTPSendFailure(t *testing.T) {
	transport := &fakeTransport{err: &TransportError{Code: ErrCodeTemplateUnavailable}}
	env := newTestEnv(t, Config{Transport: transport}, defaultTestConfig())

	res := env.do(http.MethodPost, "/api/collections/members/request-whatsapp-otp", map[string]any{"phone": testMemberPhone}, "")
	if res.status != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", res.status, res.raw)
	}

	// the unsent challenge is deleted
	total, _ := env.app.CountRecords(CollectionNameOTPs)
	if total != 0 {
		t.Fatalf("Expected the unsent challenge to be deleted, got %d", total)
	}
}

func TestAuthWithWhatsApp(t *testing.T) {
	env := newTestEnv(t, Config{}, defaultTestConfig())

	otpId, code := env.requestCode("+1 (616) 555-0100")

	msg := env.transport.last(t)
	if msg.To != testMemberPhone || msg.Collection != "members" || msg.TTL != 300*time.Second || len(code) != 6 {
		t.Fatalf("Unexpected message %+v", msg)
	}

	res := env.do(http.MethodPost, "/api/collections/members/auth-with-whatsapp", map[string]any{"otpId": otpId, "password": code}, "")
	if res.status != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", res.status, res.raw)
	}
	if res.body["token"] == "" {
		t.Fatalf("Missing token: %s", res.raw)
	}
	record, _ := res.body["record"].(map[string]any)
	if record["id"] != env.member.Id {
		t.Fatalf("Expected member %q, got %v", env.member.Id, record["id"])
	}
	if meta, _ := res.body["meta"].(map[string]any); meta["isNew"] != false {
		t.Fatalf("Expected isNew false, got %v", res.body["meta"])
	}

	member, _ := env.app.FindRecordById("members", env.member.Id)
	if !member.GetBool("phoneVerified") {
		t.Fatal("Expected phoneVerified to be set")
	}
	if member.Verified() {
		t.Fatal("Expected the email verified state to be unchanged")
	}

	// the code is single use
	res = env.do(http.MethodPost, "/api/collections/members/auth-with-whatsapp", map[string]any{"otpId": otpId, "password": code}, "")
	if res.status != http.StatusBadRequest {
		t.Fatalf("Expected 400 on reuse, got %d: %s", res.status, res.raw)
	}

	// auth origin is tracked with the custom method name
	origins, err := env.app.FindAllAuthOriginsByRecord(member)
	if err != nil || len(origins) == 0 {
		t.Fatalf("Expected an auth origin, got %v (%v)", len(origins), err)
	}
}

func TestAuthWithWhatsAppAttempts(t *testing.T) {
	env := newTestEnv(t, Config{Limits: Limits{MaxAttempts: 3}}, defaultTestConfig())

	otpId, code := env.requestCode(testMemberPhone)

	wrong := "000000"
	if code == wrong {
		wrong = "111111"
	}

	for i := 0; i < 3; i++ {
		res := env.do(http.MethodPost, "/api/collections/members/auth-with-whatsapp", map[string]any{"otpId": otpId, "password": wrong}, "")
		if res.status != http.StatusBadRequest {
			t.Fatalf("[%d] Expected 400, got %d: %s", i, res.status, res.raw)
		}
	}

	// invalidated after the max attempts even with the correct code
	res := env.do(http.MethodPost, "/api/collections/members/auth-with-whatsapp", map[string]any{"otpId": otpId, "password": code}, "")
	if res.status != http.StatusBadRequest {
		t.Fatalf("Expected 400 after max attempts, got %d: %s", res.status, res.raw)
	}
}

func TestAuthWithWhatsAppExpired(t *testing.T) {
	env := newTestEnv(t, Config{}, defaultTestConfig())

	otpId, code := env.requestCode(testMemberPhone)

	past := types.NowDateTime().Add(-301 * time.Second)
	_, err := env.app.DB().Update(CollectionNameOTPs, dbx.Params{"created": past}, dbx.HashExp{"id": otpId}).Execute()
	if err != nil {
		t.Fatal(err)
	}

	res := env.do(http.MethodPost, "/api/collections/members/auth-with-whatsapp", map[string]any{"otpId": otpId, "password": code}, "")
	if res.status != http.StatusBadRequest {
		t.Fatalf("Expected 400, got %d: %s", res.status, res.raw)
	}
}

func TestAuthWithWhatsAppDifferentCollection(t *testing.T) {
	env := newTestEnv(t, Config{}, defaultTestConfig())

	otpId, code := env.requestCode(testMemberPhone)

	// core OTP endpoint must not accept WhatsApp challenges
	res := env.do(http.MethodPost, "/api/collections/members/auth-with-otp", map[string]any{"otpId": otpId, "password": code}, "")
	if res.status == http.StatusOK {
		t.Fatalf("Expected the core OTP endpoint to reject the WhatsApp code: %s", res.raw)
	}
}

func TestSignup(t *testing.T) {
	cfg := defaultTestConfig()
	cfg.AllowSignup = true
	env := newTestEnv(t, Config{}, cfg)

	otpId, code := env.requestCode("+52 55 1234 5678")

	res := env.do(http.MethodPost, "/api/collections/members/auth-with-whatsapp", map[string]any{"otpId": otpId, "password": code}, "")
	if res.status != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", res.status, res.raw)
	}
	if meta, _ := res.body["meta"].(map[string]any); meta["isNew"] != true {
		t.Fatalf("Expected isNew true, got %v", res.body["meta"])
	}

	record, err := env.app.FindFirstRecordByData("members", "phone", "+525512345678")
	if err != nil {
		t.Fatal(err)
	}
	if record.Email() != "" || record.Verified() || !record.GetBool("phoneVerified") {
		t.Fatalf("Unexpected new record state: email=%q verified=%v phoneVerified=%v", record.Email(), record.Verified(), record.GetBool("phoneVerified"))
	}
}

func TestSignupLockedCreateRule(t *testing.T) {
	cfg := defaultTestConfig()
	cfg.AllowSignup = true
	env := newTestEnv(t, Config{}, cfg)

	otpId, code := env.requestCode("+525512345678")

	// lock the create rule after the code was sent
	env.members.CreateRule = nil
	if err := env.app.Save(env.members); err != nil {
		t.Fatal(err)
	}

	res := env.do(http.MethodPost, "/api/collections/members/auth-with-whatsapp", map[string]any{"otpId": otpId, "password": code}, "")
	if res.status != http.StatusBadRequest {
		t.Fatalf("Expected 400, got %d: %s", res.status, res.raw)
	}
}

func TestMFA(t *testing.T) {
	env := newTestEnv(t, noCooldown(), defaultTestConfig())

	// MFA requires 2 core auth methods (password + otp)
	env.members.OTP.Enabled = true
	env.members.MFA.Enabled = true
	if err := env.app.Save(env.members); err != nil {
		t.Fatal(err)
	}

	// password first
	res := env.do(http.MethodPost, "/api/collections/members/auth-with-password", map[string]any{
		"identity": "member@example.com",
		"password": "1234567890",
	}, "")
	if res.status != http.StatusUnauthorized {
		t.Fatalf("Expected 401 mfa, got %d: %s", res.status, res.raw)
	}
	mfaId, _ := res.body["mfaId"].(string)
	if mfaId == "" {
		t.Fatalf("Missing mfaId: %s", res.raw)
	}

	// then WhatsApp
	otpId, code := env.requestCode(testMemberPhone)
	res = env.do(http.MethodPost, "/api/collections/members/auth-with-whatsapp", map[string]any{
		"otpId":    otpId,
		"password": code,
		"mfaId":    mfaId,
	}, "")
	if res.status != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", res.status, res.raw)
	}

	// WhatsApp first, then password
	otpId, code = env.requestCode(testMemberPhone)
	res = env.do(http.MethodPost, "/api/collections/members/auth-with-whatsapp", map[string]any{"otpId": otpId, "password": code}, "")
	if res.status != http.StatusUnauthorized {
		t.Fatalf("Expected 401 mfa, got %d: %s", res.status, res.raw)
	}
	res = env.do(http.MethodPost, "/api/collections/members/auth-with-password", map[string]any{
		"identity": "member@example.com",
		"password": "1234567890",
		"mfaId":    res.body["mfaId"],
	}, "")
	if res.status != http.StatusOK {
		t.Fatalf("Expected 200 for whatsapp + password, got %d: %s", res.status, res.raw)
	}

	// WhatsApp twice is not a valid MFA pair
	otpId, code = env.requestCode(testMemberPhone)
	res = env.do(http.MethodPost, "/api/collections/members/auth-with-whatsapp", map[string]any{"otpId": otpId, "password": code}, "")
	if res.status != http.StatusUnauthorized {
		t.Fatalf("Expected 401 mfa, got %d: %s", res.status, res.raw)
	}
	mfaId, _ = res.body["mfaId"].(string)

	otpId, code = env.requestCode(testMemberPhone)
	res = env.do(http.MethodPost, "/api/collections/members/auth-with-whatsapp", map[string]any{
		"otpId":    otpId,
		"password": code,
		"mfaId":    mfaId,
	}, "")
	if res.status != http.StatusBadRequest {
		t.Fatalf("Expected 400 for the same MFA method, got %d: %s", res.status, res.raw)
	}
}

func TestPhoneFieldGuard(t *testing.T) {
	env := newTestEnv(t, Config{}, defaultTestConfig())

	url := "/api/collections/members/records/" + env.member.Id

	res := env.do(http.MethodPatch, url, map[string]any{"phone": "+16165550199"}, env.memberToken)
	if res.status != http.StatusBadRequest || !strings.Contains(res.raw, "validation_whatsapp_phone_locked") {
		t.Fatalf("Expected 400 locked, got %d: %s", res.status, res.raw)
	}

	res = env.do(http.MethodPatch, url, map[string]any{"phoneVerified": true}, env.memberToken)
	if res.status != http.StatusBadRequest {
		t.Fatalf("Expected 400 locked, got %d: %s", res.status, res.raw)
	}

	// other fields are allowed
	res = env.do(http.MethodPatch, url, map[string]any{"emailVisibility": true}, env.memberToken)
	if res.status != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", res.status, res.raw)
	}

	// public create with a phone is rejected
	res = env.do(http.MethodPost, "/api/collections/members/records", map[string]any{
		"password":        "1234567890",
		"passwordConfirm": "1234567890",
		"phone":           "+16165550188",
	}, "")
	if res.status != http.StatusBadRequest || !strings.Contains(res.raw, "validation_whatsapp_phone_locked") {
		t.Fatalf("Expected 400 locked, got %d: %s", res.status, res.raw)
	}

	// superusers can change it
	res = env.do(http.MethodPatch, url, map[string]any{"phone": "+16165550199"}, env.superuserToken)
	if res.status != http.StatusOK {
		t.Fatalf("Expected 200 for superusers, got %d: %s", res.status, res.raw)
	}
}

func TestPhoneChange(t *testing.T) {
	env := newTestEnv(t, Config{}, defaultTestConfig())

	// requires auth
	res := env.do(http.MethodPost, "/api/collections/members/request-whatsapp-phone-change", map[string]any{"newPhone": "+16165550177"}, "")
	if res.status != http.StatusUnauthorized {
		t.Fatalf("Expected 401, got %d: %s", res.status, res.raw)
	}

	// existing phone
	res = env.do(http.MethodPost, "/api/collections/members/request-whatsapp-phone-change", map[string]any{"newPhone": testMemberPhone}, env.memberToken)
	if res.status != http.StatusBadRequest || !strings.Contains(res.raw, "validation_phone_exists") {
		t.Fatalf("Expected 400 phone exists, got %d: %s", res.status, res.raw)
	}

	res = env.do(http.MethodPost, "/api/collections/members/request-whatsapp-phone-change", map[string]any{"newPhone": "+16165550177"}, env.memberToken)
	if res.status != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", res.status, res.raw)
	}
	otpId, _ := res.body["otpId"].(string)
	msg := env.transport.last(t)
	if msg.To != "+16165550177" {
		t.Fatalf("Expected the code to be sent to the new phone, got %q", msg.To)
	}

	// a phone change code can't be used to login
	res = env.do(http.MethodPost, "/api/collections/members/auth-with-whatsapp", map[string]any{"otpId": otpId, "password": msg.Code}, "")
	if res.status != http.StatusBadRequest {
		t.Fatalf("Expected 400, got %d: %s", res.status, res.raw)
	}

	res = env.do(http.MethodPost, "/api/collections/members/confirm-whatsapp-phone-change", map[string]any{"otpId": otpId, "password": msg.Code}, env.memberToken)
	if res.status != http.StatusNoContent {
		t.Fatalf("Expected 204, got %d: %s", res.status, res.raw)
	}

	member, _ := env.app.FindRecordById("members", env.member.Id)
	if member.GetString("phone") != "+16165550177" || !member.GetBool("phoneVerified") {
		t.Fatalf("Unexpected member phone state: %q %v", member.GetString("phone"), member.GetBool("phoneVerified"))
	}
}

func TestPhoneChangeRace(t *testing.T) {
	env := newTestEnv(t, noCooldown(), defaultTestConfig())

	other := core.NewRecord(env.members)
	other.SetEmail("other@example.com")
	other.SetPassword("1234567890")
	if err := env.app.Save(other); err != nil {
		t.Fatal(err)
	}
	otherToken, _ := other.NewAuthToken()

	request := func(token string) (string, string) {
		res := env.do(http.MethodPost, "/api/collections/members/request-whatsapp-phone-change", map[string]any{"newPhone": "+16165550166"}, token)
		if res.status != http.StatusOK {
			t.Fatalf("Expected 200, got %d: %s", res.status, res.raw)
		}
		return res.body["otpId"].(string), env.transport.last(t).Code
	}

	// both request the same free number before any of them confirms
	id1, code1 := request(env.memberToken)
	id2, code2 := request(otherToken)

	res := env.do(http.MethodPost, "/api/collections/members/confirm-whatsapp-phone-change", map[string]any{"otpId": id1, "password": code1}, env.memberToken)
	if res.status != http.StatusNoContent {
		t.Fatalf("Expected 204, got %d: %s", res.status, res.raw)
	}

	res = env.do(http.MethodPost, "/api/collections/members/confirm-whatsapp-phone-change", map[string]any{"otpId": id2, "password": code2}, otherToken)
	if res.status != http.StatusBadRequest || !strings.Contains(res.raw, "validation_phone_exists") {
		t.Fatalf("Expected 400 phone exists, got %d: %s", res.status, res.raw)
	}
}

func TestAuthMethods(t *testing.T) {
	env := newTestEnv(t, Config{}, defaultTestConfig())

	res := env.do(http.MethodGet, "/api/collections/members/auth-methods", nil, "")
	if res.status != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", res.status, res.raw)
	}

	wa, _ := res.body["whatsapp"].(map[string]any)
	if wa["enabled"] != true || wa["allowSignup"] != false || wa["codeLength"] != float64(6) || wa["duration"] != float64(300) {
		t.Fatalf("Unexpected whatsapp auth methods info: %s", res.raw)
	}

	// signup enabled
	res = env.do(http.MethodPatch, "/api/whatsapp/collections/members", map[string]any{"allowSignup": true}, env.superuserToken)
	if res.status != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", res.status, res.raw)
	}
	res = env.do(http.MethodGet, "/api/collections/members/auth-methods", nil, "")
	wa, _ = res.body["whatsapp"].(map[string]any)
	if wa["enabled"] != true || wa["allowSignup"] != true {
		t.Fatalf("Expected allowSignup, got %s", res.raw)
	}

	// the core keys are preserved
	if _, ok := res.body["password"]; !ok {
		t.Fatalf("Missing core password key: %s", res.raw)
	}

	// disabled collection
	res = env.do(http.MethodGet, "/api/collections/users/auth-methods", nil, "")
	wa, _ = res.body["whatsapp"].(map[string]any)
	if res.status != http.StatusOK || wa["enabled"] != false || wa["allowSignup"] != false {
		t.Fatalf("Expected disabled whatsapp key, got %d: %s", res.status, res.raw)
	}

	// other routes are untouched
	res = env.do(http.MethodGet, "/api/collections/missing/auth-methods", nil, "")
	if res.status != http.StatusNotFound || strings.Contains(res.raw, "whatsapp") {
		t.Fatalf("Expected untouched 404, got %d: %s", res.status, res.raw)
	}
}

func TestDeleteRecordAndCollectionCleanup(t *testing.T) {
	env := newTestEnv(t, Config{}, defaultTestConfig())

	env.requestCode(testMemberPhone)

	if err := env.app.Delete(env.member); err != nil {
		t.Fatal(err)
	}

	total, _ := env.app.CountRecords(CollectionNameOTPs)
	if total != 0 {
		t.Fatalf("Expected the record challenges to be deleted, got %d", total)
	}

	if err := env.app.Delete(env.members); err != nil {
		t.Fatal(err)
	}

	_, err := env.app.FindFirstRecordByData(CollectionNameStore, "key", storeKeyCollectionPrefix+env.members.Id)
	if err == nil {
		t.Fatal("Expected the collection config to be deleted")
	}
}

func TestDeleteExpiredChallenges(t *testing.T) {
	env := newTestEnv(t, noCooldown(), defaultTestConfig())

	oldId, _ := env.requestCode(testMemberPhone)
	newId, _ := env.requestCode(testMemberPhone)

	past := types.NowDateTime().Add(-2 * time.Hour)
	if _, err := env.app.DB().Update(CollectionNameOTPs, dbx.Params{"created": past}, dbx.HashExp{"id": oldId}).Execute(); err != nil {
		t.Fatal(err)
	}

	p := &plugin{}
	if err := p.deleteExpiredChallenges(env.app); err != nil {
		t.Fatal(err)
	}

	if _, err := env.app.FindRecordById(CollectionNameOTPs, oldId); err == nil {
		t.Fatal("Expected the old challenge to be deleted")
	}
	if _, err := env.app.FindRecordById(CollectionNameOTPs, newId); err != nil {
		t.Fatal("Expected the new challenge to remain")
	}
}
