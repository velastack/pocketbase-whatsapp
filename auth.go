package whatsapp

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/pocketbase/dbx"
	validation "github.com/pocketbase/ozzo-validation/v4"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/routine"
	"github.com/pocketbase/pocketbase/tools/security"
	"github.com/pocketbase/pocketbase/tools/types"
)

// AuthMethod is the auth method name reported to the core auth response
// helpers (MFA, auth origins, OnRecordAuthRequest hook, etc.).
const AuthMethod = "whatsapp"

const (
	purposeAuth        = "auth"
	purposePhoneChange = "phoneChange"
)

const msgInvalidCode = "Invalid or expired code."

// asyncSend is replaced in the tests to send the codes synchronously.
var asyncSend = routine.FireAndForget

type requestCodeForm struct {
	Phone string `form:"phone" json:"phone"`
	Lang  string `form:"lang" json:"lang"`
}

type verifyCodeForm struct {
	OTPId    string `form:"otpId" json:"otpId"`
	Password string `form:"password" json:"password"`
}

func (form *verifyCodeForm) validate() error {
	return validation.ValidateStruct(form,
		validation.Field(&form.OTPId, validation.Required, validation.Length(1, 255)),
		validation.Field(&form.Password, validation.Required, validation.Length(1, 20)),
	)
}

// resolveAuthCollection returns the path auth collection and its
// WhatsApp config (returns an error if WhatsApp auth is not enabled).
func (p *plugin) resolveAuthCollection(e *core.RequestEvent) (*core.Collection, *CollectionConfig, error) {
	collection, err := e.App.FindCachedCollectionByNameOrId(e.Request.PathValue("collection"))
	if err != nil || !collection.IsAuth() {
		return nil, nil, e.NotFoundError("Missing or invalid auth collection context.", err)
	}

	if collection.Name == core.CollectionNameSuperusers {
		return nil, nil, e.ForbiddenError("WhatsApp auth is not supported for superusers.", nil)
	}

	cfg, err := p.loadCollectionConfig(e.App, collection.Id)
	if err != nil {
		return nil, nil, e.InternalServerError("", err)
	}

	if !cfg.Enabled {
		return nil, nil, e.ForbiddenError("The collection is not configured to allow WhatsApp authentication.", nil)
	}

	return collection, cfg, nil
}

// normalizeFormPhone normalizes and validates the submitted phone number.
func normalizeFormPhone(e *core.RequestEvent, field string, raw string, cfg *CollectionConfig) (string, error) {
	phone, err := NormalizePhone(raw)
	if err != nil {
		return "", e.BadRequestError("An error occurred while validating the submitted data.", validation.Errors{
			field: validation.NewError("validation_invalid_phone", err.Error()),
		})
	}

	if !isCountryAllowed(phone, cfg.AllowedCountryCodes) {
		return "", e.BadRequestError("An error occurred while validating the submitted data.", validation.Errors{
			field: validation.NewError("validation_country_not_allowed", errCountryNotAllowed.Error()),
		})
	}

	return phone, nil
}

// checkSendLimits enforces the per IP, per phone cooldown and per phone daily limits.
//
// The limits are applied regardless of whether the phone belongs
// to an existing record to avoid leaking that information.
func (p *plugin) checkSendLimits(e *core.RequestEvent, phone string) error {
	checks := []struct {
		key    string
		max    int
		window time.Duration
		msg    string
	}{
		{"req:ip:" + e.RealIP(), p.config.Limits.MaxRequestsPerIP, p.config.Limits.IPWindow, "Too many requests, please try again later."},
		{"req:cooldown:" + phone, 1, p.config.Limits.ResendCooldown, "Please wait before requesting a new code."},
		{"req:daily:" + phone, p.config.Limits.MaxRequestsPerPhone, 24 * time.Hour, "Too many codes were requested for this phone number, please try again later."},
	}

	for _, c := range checks {
		ok, retryAfter := p.limiter.allow(c.key, c.max, c.window)
		if !ok {
			// note: the core api error data is reserved for validation errors
			e.Response.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())+1))
			return e.TooManyRequestsError(c.msg, nil)
		}
	}

	return nil
}

// createChallenge persists a new code challenge and sends the code in the background.
func (p *plugin) createChallenge(
	e *core.RequestEvent,
	transport Transport,
	collection *core.Collection,
	cfg *CollectionConfig,
	record *core.Record,
	phone string,
	purpose string,
	lang string,
) (string, error) {
	otpsCollection, err := e.App.FindCachedCollectionByNameOrId(CollectionNameOTPs)
	if err != nil {
		return "", err
	}

	code := security.RandomStringWithAlphabet(cfg.CodeLength, "0123456789")

	challenge := core.NewRecord(otpsCollection)
	challenge.Set("collectionRef", collection.Id)
	if record != nil {
		challenge.Set("recordRef", record.Id)
	}
	challenge.Set("phone", phone)
	challenge.Set("purpose", purpose)
	challenge.Set("password", code)

	if err := e.App.Save(challenge); err != nil {
		return "", err
	}

	msg := AuthCode{
		To:          phone,
		Code:        code,
		Lang:        lang,
		TTL:         time.Duration(cfg.Duration) * time.Second,
		AppName:     e.App.Settings().Meta.AppName,
		AppURL:      e.App.Settings().Meta.AppURL,
		Collection:  collection.Name,
		ChallengeID: challenge.Id,
	}
	if p.forwardClientIP(e.App) {
		msg.ClientIP = e.RealIP()
	}

	app := e.App

	// send in the background to minimize the response time differences
	// between existing and nonexisting phone numbers
	asyncSend(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		messageId, err := transport.SendAuthCode(ctx, msg)
		if err != nil {
			app.Logger().Error(
				"Failed to send WhatsApp auth code",
				"error", err,
				"errorCode", transportErrorCode(err),
				"challengeId", challenge.Id,
				"collection", collection.Name,
			)

			if err := app.Delete(challenge); err != nil {
				app.Logger().Warn("Failed to delete unsent WhatsApp code challenge", "error", err, "challengeId", challenge.Id)
			}
			return
		}

		challenge.Set("messageId", messageId)
		if err := app.Save(challenge); err != nil {
			app.Logger().Warn("Failed to store the WhatsApp message id", "error", err, "challengeId", challenge.Id)
		}
	})

	return challenge.Id, nil
}

// verifyChallenge loads and validates the code challenge.
//
// Failed attempts are counted and the challenge is deleted once the limit is reached.
func (p *plugin) verifyChallenge(
	e *core.RequestEvent,
	collection *core.Collection,
	cfg *CollectionConfig,
	purpose string,
	form *verifyCodeForm,
) (*core.Record, error) {
	if ok, _ := p.limiter.allow("verify:ip:"+e.RealIP(), 30, 10*time.Minute); !ok {
		return nil, e.TooManyRequestsError("Too many attempts, please try again later.", nil)
	}

	challenge, err := e.App.FindRecordById(CollectionNameOTPs, form.OTPId)
	if err != nil {
		return nil, e.BadRequestError(msgInvalidCode, err)
	}

	if challenge.GetString("collectionRef") != collection.Id || challenge.GetString("purpose") != purpose {
		return nil, e.BadRequestError(msgInvalidCode, errors.New("the code is for a different collection or purpose"))
	}

	expires := challenge.GetDateTime("created").Time().Add(time.Duration(cfg.Duration) * time.Second)
	if time.Now().After(expires) {
		return nil, e.BadRequestError(msgInvalidCode, errors.New("the code has expired"))
	}

	if !challenge.ValidatePassword(form.Password) {
		attempts := challenge.GetInt("attempts") + 1
		if attempts >= p.config.Limits.MaxAttempts {
			if err := e.App.Delete(challenge); err != nil {
				e.App.Logger().Warn("Failed to delete WhatsApp code challenge", "error", err, "challengeId", challenge.Id)
			}
		} else {
			// the password field must not be revalidated (it stores only the hash)
			challenge.Set("attempts", attempts)
			if err := e.App.Save(challenge); err != nil {
				return nil, e.InternalServerError("", err)
			}
		}

		return nil, e.BadRequestError(msgInvalidCode, errors.New("incorrect code"))
	}

	// eagerly delete the used challenge
	if err := e.App.Delete(challenge); err != nil {
		e.App.Logger().Error("Failed to delete used WhatsApp code challenge", "error", err, "challengeId", challenge.Id)
	}

	return challenge, nil
}

// -------------------------------------------------------------------

// requestOTP handles POST /api/collections/{collection}/request-whatsapp-otp.
func (p *plugin) requestOTP(e *core.RequestEvent) error {
	collection, cfg, err := p.resolveAuthCollection(e)
	if err != nil {
		return err
	}

	form := &requestCodeForm{}
	if err := e.BindBody(form); err != nil {
		return e.BadRequestError("An error occurred while loading the submitted data.", err)
	}

	phone, err := normalizeFormPhone(e, "phone", form.Phone, cfg)
	if err != nil {
		return err
	}

	transport, err := p.transport(e.App)
	if err != nil {
		return e.InternalServerError("", err)
	}
	if transport == nil {
		return e.Error(http.StatusServiceUnavailable, "WhatsApp delivery is not configured.", nil)
	}

	if err := p.checkSendLimits(e, phone); err != nil {
		return err
	}

	record, err := e.App.FindFirstRecordByData(collection, cfg.PhoneField, phone)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return e.InternalServerError("", err)
	}

	if record == nil && !cfg.AllowSignup {
		// write a dummy response as a very rudimentary phone numbers enumeration "protection"
		return e.JSON(http.StatusOK, map[string]string{"otpId": core.GenerateDefaultRandomId()})
	}

	otpId, err := p.createChallenge(e, transport, collection, cfg, record, phone, purposeAuth, form.Lang)
	if err != nil {
		return e.InternalServerError("Failed to create the WhatsApp code.", err)
	}

	return e.JSON(http.StatusOK, map[string]string{"otpId": otpId})
}

// authWithOTP handles POST /api/collections/{collection}/auth-with-whatsapp.
func (p *plugin) authWithOTP(e *core.RequestEvent) error {
	collection, cfg, err := p.resolveAuthCollection(e)
	if err != nil {
		return err
	}

	form := &verifyCodeForm{}
	if err := e.BindBody(form); err != nil {
		return e.BadRequestError("An error occurred while loading the submitted data.", err)
	}
	if err := form.validate(); err != nil {
		return e.BadRequestError("An error occurred while validating the submitted data.", err)
	}

	e.Set(core.RequestEventKeyInfoContext, AuthMethod)

	challenge, err := p.verifyChallenge(e, collection, cfg, purposeAuth, form)
	if err != nil {
		return err
	}

	phone := challenge.GetString("phone")

	var record *core.Record
	isNew := false

	if recordRef := challenge.GetString("recordRef"); recordRef != "" {
		record, err = e.App.FindRecordById(collection, recordRef)
		if err != nil {
			return e.BadRequestError(msgInvalidCode, fmt.Errorf("missing auth record: %w", err))
		}

		// the phone could have been changed after the code was sent
		if record.GetString(cfg.PhoneField) != phone {
			return e.BadRequestError(msgInvalidCode, errors.New("the record phone number has changed"))
		}
	} else {
		// another code for the same phone could have been used in the meantime
		record, err = e.App.FindFirstRecordByData(collection, cfg.PhoneField, phone)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return e.InternalServerError("", err)
		}

		if record == nil {
			if !cfg.AllowSignup || collection.CreateRule == nil {
				return e.BadRequestError(msgInvalidCode, errors.New("signup is not allowed"))
			}

			record = core.NewRecord(collection)
			record.Set(cfg.PhoneField, phone)
			record.SetRandomPassword()
			isNew = true
		}
	}

	if cfg.PhoneVerifiedField != "" && !record.GetBool(cfg.PhoneVerifiedField) {
		record.Set(cfg.PhoneVerifiedField, true)
		if !isNew {
			if err := e.App.Save(record); err != nil {
				e.App.Logger().Error("Failed to update the record phone verified state", "error", err, "recordId", record.Id)
			}
		}
	}

	if isNew {
		if err := e.App.Save(record); err != nil {
			return e.BadRequestError("Failed to create the auth record.", err)
		}
	}

	return apis.RecordAuthResponse(e, record, AuthMethod, map[string]any{"isNew": isNew})
}

// requestPhoneChange handles POST /api/collections/{collection}/request-whatsapp-phone-change.
func (p *plugin) requestPhoneChange(e *core.RequestEvent) error {
	collection, cfg, err := p.resolveAuthCollection(e)
	if err != nil {
		return err
	}

	if e.Auth == nil || e.Auth.Collection().Id != collection.Id {
		return e.UnauthorizedError("The request requires valid record authorization token.", nil)
	}

	form := &struct {
		NewPhone string `form:"newPhone" json:"newPhone"`
		Lang     string `form:"lang" json:"lang"`
	}{}
	if err := e.BindBody(form); err != nil {
		return e.BadRequestError("An error occurred while loading the submitted data.", err)
	}

	phone, err := normalizeFormPhone(e, "newPhone", form.NewPhone, cfg)
	if err != nil {
		return err
	}

	existing, err := e.App.FindFirstRecordByData(collection, cfg.PhoneField, phone)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return e.InternalServerError("", err)
	}
	if existing != nil {
		return e.BadRequestError("An error occurred while validating the submitted data.", validation.Errors{
			"newPhone": validation.NewError("validation_phone_exists", "The phone number is already in use."),
		})
	}

	transport, err := p.transport(e.App)
	if err != nil {
		return e.InternalServerError("", err)
	}
	if transport == nil {
		return e.Error(http.StatusServiceUnavailable, "WhatsApp delivery is not configured.", nil)
	}

	if err := p.checkSendLimits(e, phone); err != nil {
		return err
	}

	otpId, err := p.createChallenge(e, transport, collection, cfg, e.Auth, phone, purposePhoneChange, form.Lang)
	if err != nil {
		return e.InternalServerError("Failed to create the WhatsApp code.", err)
	}

	return e.JSON(http.StatusOK, map[string]string{"otpId": otpId})
}

// confirmPhoneChange handles POST /api/collections/{collection}/confirm-whatsapp-phone-change.
func (p *plugin) confirmPhoneChange(e *core.RequestEvent) error {
	collection, cfg, err := p.resolveAuthCollection(e)
	if err != nil {
		return err
	}

	if e.Auth == nil || e.Auth.Collection().Id != collection.Id {
		return e.UnauthorizedError("The request requires valid record authorization token.", nil)
	}

	form := &verifyCodeForm{}
	if err := e.BindBody(form); err != nil {
		return e.BadRequestError("An error occurred while loading the submitted data.", err)
	}
	if err := form.validate(); err != nil {
		return e.BadRequestError("An error occurred while validating the submitted data.", err)
	}

	challenge, err := p.verifyChallenge(e, collection, cfg, purposePhoneChange, form)
	if err != nil {
		return err
	}

	if challenge.GetString("recordRef") != e.Auth.Id {
		return e.BadRequestError(msgInvalidCode, errors.New("the code was requested by a different record"))
	}

	record, err := e.App.FindRecordById(collection, e.Auth.Id)
	if err != nil {
		return e.NotFoundError("", err)
	}

	phone := challenge.GetString("phone")

	// the number could have been taken by another record after the code was sent
	existing, err := e.App.FindFirstRecordByData(collection, cfg.PhoneField, phone)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return e.InternalServerError("", err)
	}
	if existing != nil && existing.Id != record.Id {
		return e.BadRequestError("An error occurred while validating the submitted data.", validation.Errors{
			"newPhone": validation.NewError("validation_phone_exists", "The phone number is already in use."),
		})
	}

	record.Set(cfg.PhoneField, phone)
	if cfg.PhoneVerifiedField != "" {
		record.Set(cfg.PhoneVerifiedField, true)
	}

	if err := e.App.Save(record); err != nil {
		return e.BadRequestError("Failed to update the phone number.", err)
	}

	return e.NoContent(http.StatusNoContent)
}

// deleteExpiredChallenges deletes all code challenges older than the max allowed code duration.
func (p *plugin) deleteExpiredChallenges(app core.App) error {
	minDate, err := types.ParseDateTime(time.Now().Add(-maxCodeDuration * time.Second))
	if err != nil {
		return err
	}

	records := []*core.Record{}
	err = app.RecordQuery(CollectionNameOTPs).
		AndWhere(dbx.NewExp("[[created]] < {:date}", dbx.Params{"date": minDate})).
		All(&records)
	if err != nil {
		return err
	}

	var errs []error
	for _, r := range records {
		if err := app.Delete(r); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}
