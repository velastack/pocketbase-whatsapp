# pocketbase-whatsapp

WhatsApp one-time code login for [PocketBase](https://pocketbase.io), next to password, OTP and OAuth2.

Users enter their phone number and get a code from an approved WhatsApp **authentication template** (copy code button, with iOS keyboard autofill). The plugin uses only public PocketBase APIs, so it works with vanilla PocketBase (v0.40.4+) as well as [velabase](https://github.com/velastack/velabase).

With the VelaStack sender it also **receives** the messages people send to your WhatsApp number, stores them in PocketBase and adds an **Inbox** page to the dashboard (see [Incoming messages](#incoming-messages)).

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

## Incoming messages

Incoming messages need the VelaStack sender. The project behind the key must send from your own WhatsApp number, connected in the VelaStack dashboard. Direct mode stays sign-in codes only.

**How they arrive:**
- **Webhook:** when the app URL (Settings > Application) is public https, the plugin registers `{appURL}/api/whatsapp/webhook` with VelaStack. It does this at startup and whenever the WhatsApp settings are saved (a registration that fails because VelaStack is briefly unavailable is retried, from 15 seconds up to every 5 minutes, while it polls), and keeps the signing secret encrypted in `_whatsapp`. Events are signed `X-VelaStack-Signature: t=…,v1=…` (HMAC-SHA256 of `t.body`, 5 minutes tolerance) and deduplicated by event id, so VelaStack's retries are safe.
- **Polling:** otherwise (localhost, a private address), the plugin polls VelaStack's message listing every 10 seconds (`Config.PollInterval`). It also refreshes the delivery status of recent inbox replies.
- **Safety-net polling:** with a webhook registered, the plugin still polls once a minute, in case the URL isn't actually reachable (eg. a proxy in front of PocketBase that doesn't forward `/api/whatsapp/webhook`). Messages are deduplicated, so nothing arrives twice.
- **One key per instance:** each instance needs its own project key. Another public instance using the same key takes the webhook over.

**Storage.** Both collections are created by the plugin with no API rules, so only superusers can read them until you add rules. You can add rules and fields, but you can't remove the plugin's fields or rename or delete the collections:

| Collection | Holds |
|---|---|
| `_whatsappContacts` | `phone` (E.164), `name` (WhatsApp profile name, webhook only), `savedName`/`savedRemoved` (the WhatsApp Business app's address book, see below), `lastInbound` (the 24-hour reply window starts here), `lastMessage`, `unread`, and `userCollection`/`user` when the number matches an auth record with WhatsApp auth enabled (and the phone verified, if that field is set) |
| `_whatsappMessages` | `contact`, `direction` (`in`/`out`), `origin` (empty, `app` or `history`, see below), `messageId` (wamid), `type`, `text`, `template`, `mediaId`/`mediaMime`/`mediaName`, `replyTo`, `status`, `error`, `raw` (Meta's message), `sentAt` |

React to new messages with the usual record hooks:

```go
app.OnRecordAfterCreateSuccess(whatsapp.CollectionNameMessages).BindFunc(func(e *core.RecordEvent) error {
    // origin is empty for live messages; skip the synced chat history
    if e.Record.GetString("direction") == "in" && e.Record.GetString("origin") == "" {
        // auto-reply, create a lead, hand it to an agent...
    }
    return e.Next()
})
```

Notes:
- **Media** isn't stored. It's fetched from Meta through VelaStack when someone opens it, and VelaStack keeps messages for 30 days, so older attachments can no longer be opened.
- **Linking contacts to users:** WhatsApp identifies people by the number Meta has for them. That can differ from the number a user typed (eg. old Mexican `+521` numbers), in which case the contact isn't linked.

**WhatsApp Business app numbers (coexistence).** A number connected in VelaStack with "Use my WhatsApp Business app number" stays in the app on the owner's phone. The plugin then also stores:
- **The owner's replies from the app** as outgoing messages with `origin: "app"`. They clear the contact's unread count (the owner has answered) and never open the reply window. A bot should treat one as "the owner is handling this".
- **The chat history** Meta syncs once after onboarding, as `origin: "history"` (VelaStack keeps the last 30 days). History is neither unread nor a reason to think the window is open, so the Inbox offers templates for those threads until the customer writes again.
- **The address book** as the contact's `savedName`. A contact the owner deletes keeps its messages, with `savedRemoved` set.

These arrive as the `message.echo`, `message.history` and `contact.sync` webhook events, or through `GET {relay}/messages?conversation=1` and `GET {relay}/contacts` when polling. After upgrading, the first poll lists the conversation from the start; message ids dedupe what was already stored.

**Inbox.** The superuser dashboard gets an **Inbox** page. It's meant for checking what the plugin stores, not as a customer-service tool for business owners.
- **Conversation list:** unread counts and a dot while the 24-hour reply window is open.
- **Thread:** text, images and documents, plus quoted replies and delivery statuses. On a WhatsApp Business app number, replies sent from the app have a phone icon and the imported history is set apart. Names saved in the app come first.
- **Reply box:** sends free text while the window is open, or an approved template at any time.
- **Read receipts:** opening a conversation marks it read for the customer (blue ticks).

The superuser API behind it: `GET /api/whatsapp/inbox`, `GET|POST /api/whatsapp/inbox/{contact}`, `POST /api/whatsapp/inbox/{contact}/read`, `GET /api/whatsapp/inbox/templates`, `GET /api/whatsapp/inbox/media/{mediaId}`.

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
npm install && npm run build   # after changing web/ (the built ui/ is committed)
# against a local PocketBase checkout: create a go.work (gitignored) with
#   use .
#   replace github.com/pocketbase/pocketbase => ../pocketbase
go run ./examples/base serve --dev
```

## License

MIT
