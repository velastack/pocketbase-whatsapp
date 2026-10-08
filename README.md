# pocketbase-whatsapp

WhatsApp one-time code login for [PocketBase](https://pocketbase.io), next to password, OTP and OAuth2.

Users enter their phone number and get a code from an approved WhatsApp **authentication template** (copy code button, with iOS keyboard autofill). The plugin uses only public PocketBase APIs, so it works with vanilla PocketBase (v0.40.4+) as well as [velabase](https://github.com/velastack/velabase).

## Install

```go
import "github.com/velastack/pocketbase-whatsapp"

whatsapp.MustRegister(app, whatsapp.Config{})
```

Then open the superuser UI at **Settings > WhatsApp**. On that page you:

1. **Pick a sender:**

   | Sender | You need | Notes |
   |---|---|---|
   | Development | nothing | Codes go to the app logs. For local development only. |
   | VelaStack | a VelaStack API key | No Meta business verification needed. VelaStack sends from its shared verified number, or from your own number once you connect it in the VelaStack dashboard (Embedded Signup). Switching numbers needs no change here. |
   | WhatsApp Cloud API (direct) | phone number ID, access token, approved AUTHENTICATION template name with a copy code button, WABA ID (recommended) | Meta grants the authentication template category only to verified businesses. Without the WABA ID the status check can't confirm the template is approved. |

   For the direct sender, create a system user in Meta Business Settings, assign it the Meta app and the WhatsApp account, then generate a token with the `whatsapp_business_messaging` and `whatsapp_business_management` permissions. Generate token lists no permissions until the app is assigned to the system user.

2. **Enable WhatsApp auth per auth collection.** Pick the phone field, which must be a text field with a UNIQUE index; the "Create phone field" button adds one with an E.164 pattern (through the same hooks as the collections API, so automigrate writes a migration for it). Then choose whether new numbers can sign up, set the code length and lifetime, and optionally restrict country calling codes.

The sender can also be set with env variables. These override the UI and show as "env" there. Hosted instances are provisioned this way. When `WHATSAPP_MODE` is not set, the credentials pick the sender: `VELASTACK_API_KEY` selects VelaStack and `WHATSAPP_ACCESS_TOKEN` the direct Cloud API, so a deployment only needs the secret in its environment.

```
WHATSAPP_MODE=dev|velastack|direct
VELASTACK_API_KEY, VELASTACK_RELAY_URL
WHATSAPP_PHONE_NUMBER_ID, WHATSAPP_WABA_ID, WHATSAPP_ACCESS_TOKEN,
WHATSAPP_TEMPLATE_NAME, WHATSAPP_TEMPLATE_LANGUAGES (comma separated), WHATSAPP_GRAPH_VERSION
```

Secrets are stored encrypted when the app has an encryption key (`--encryptionEnv`). They are never returned by the API, superusers included.

## Client usage

```js
const { otpId } = await pb.send("/api/collections/users/request-whatsapp-otp", {
    method: "POST",
    body: { phone: "+16165550123", lang: "es_MX" }, // international format
});

// <input autocomplete="one-time-code"> ...

const auth = await pb.send("/api/collections/users/auth-with-whatsapp", {
    method: "POST",
    body: { otpId, password: code }, // + mfaId when completing MFA
});
pb.authStore.save(auth.token, auth.record);
// auth.meta.isNew is true for signups
```

`GET /api/collections/{collection}/auth-methods` includes `"whatsapp": {"enabled", "allowSignup", "codeLength", "duration"}`. `allowSignup` is true only when WhatsApp auth is enabled and new numbers can sign up.

| Endpoint | Body | Result |
|---|---|---|
| `POST /api/collections/{c}/request-whatsapp-otp` | `phone`, `lang?` | `{otpId}` |
| `POST /api/collections/{c}/auth-with-whatsapp` | `otpId`, `password`, `mfaId?` | auth response |
| `POST /api/collections/{c}/request-whatsapp-phone-change` (auth) | `newPhone`, `lang?` | `{otpId}` |
| `POST /api/collections/{c}/confirm-whatsapp-phone-change` (auth) | `otpId`, `password` | 204 |

## Behavior

- **MFA:** WhatsApp logins report the auth method `"whatsapp"`, so when MFA is on it counts as its own factor. Password + WhatsApp works; WhatsApp + WhatsApp is rejected. Core PocketBase only lets you enable MFA when at least 2 of password/OTP/OAuth2 are enabled, so for now enable OTP or OAuth2 too.
- **Signup:**
  - Requires a non-locked create rule and an optional email field.
  - New records have a blank email and a random password, and `verified` stays false (in PocketBase it means the *email* is verified).
  - Use the optional "phone verified" bool field instead.
  - Other required fields on the collection (eg. `username`) make signup fail, and the client gets their validation errors.
  - The create rule itself is not evaluated.
- **Phone field protection:** only superusers can write the phone (and phone verified) field directly. Users set or change it through the phone change endpoints, which prove ownership with a code. That stops anyone from claiming someone else's number.
- **Enumeration:**
  - Unknown numbers get a dummy `otpId` when signup is off.
  - Codes are sent in the background, so known and unknown numbers take the same time to answer.
  - Rate limits apply the same way to both.
- **Cost and abuse limits:** every code is a paid message, so these always apply, on top of the app rate limit rules. The counters are in memory: they are per process and reset on restart.
  - a 60s resend cooldown per number
  - 10 requests per number per day
  - 10 requests per IP per 10 minutes
  - 5 wrong attempts per code

  Configure them with `Config.Limits`.
- **Storage:** challenges live in `_whatsappOtps` (hashed codes). An hourly job deletes challenges older than 1 hour; codes stop working after their own duration either way. Settings live in `_whatsapp`, which is blocked from the records and realtime APIs.

## Custom transports

`Config.Transport` replaces the UI-configured sender, e.g. to use another provider or add an SMS fallback:

```go
type Transport interface {
    SendAuthCode(ctx context.Context, m whatsapp.AuthCode) (messageID string, err error)
}
```

You can also implement the relay contract over HTTP and point the VelaStack sender's relay URL at your own service:

```
POST {relayURL}/auth-codes
Authorization: Bearer <apiKey>
Idempotency-Key: <challengeId>
{"to":"+16165550123","code":"482913","lang":"es_MX","ttlSeconds":300,
 "collection":"users","app":{"name":"Acme","url":"https://acme.example"},"clientIp":"203.0.113.4"}

200 {"messageId":"wamid...","sender":{"phone":"+1555...","displayName":"Acme","kind":"shared|own"}}
4xx/5xx {"code":"unauthorized|invalid_phone|country_not_allowed|rate_limited|budget_exceeded|template_unavailable","message":"...","retryAfter":60}

GET {relayURL}/status → {"ok":true,"message":"...","sender":{...},"languages":["en_US"]}
```

`clientIp` is sent only when "Forward the end user IP" is enabled.

## Development

```sh
go test ./...
# against a local PocketBase checkout: create a go.work (gitignored) with
#   use .
#   replace github.com/pocketbase/pocketbase => ../pocketbase
go run ./examples/base serve --dev
```

## License

MIT
