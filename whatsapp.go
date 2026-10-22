// Package whatsapp adds a WhatsApp one-time code auth method to PocketBase.
//
// The codes are delivered with an approved WhatsApp AUTHENTICATION template
// either directly with the WhatsApp Cloud API or through a relay
// (eg. VelaStack, which doesn't require own Meta business verification).
//
// Example:
//
//	whatsapp.MustRegister(app, whatsapp.Config{})
//
// The delivery (sender) settings are managed from the superuser UI
// "Settings > WhatsApp" page (or with WHATSAPP_* / VELASTACK_* env variables)
// and the auth endpoints are:
//
//	POST /api/collections/{collection}/request-whatsapp-otp     {phone, lang?} → {otpId}
//	POST /api/collections/{collection}/auth-with-whatsapp       {otpId, password, mfaId?} → auth response
//	POST /api/collections/{collection}/request-whatsapp-phone-change  {newPhone, lang?} → {otpId} (auth required)
//	POST /api/collections/{collection}/confirm-whatsapp-phone-change  {otpId, password} → 204 (auth required)
//
// With the VelaStack sender it also receives the messages sent to the
// number (see incoming.go) and adds an "Inbox" dashboard page.
package whatsapp

import (
	"embed"
	"io/fs"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/hook"
	"github.com/pocketbase/pocketbase/tools/routine"
)

//go:embed all:ui
var uiFS embed.FS

// Config defines the config options of the whatsapp plugin.
type Config struct {
	// Transport is an optional custom code delivery transport
	// (eg. a different provider or a wrapper with metrics/SMS fallback).
	//
	// If set, it takes precedence over the transport configured in the superuser UI.
	Transport Transport

	// HTTPClient is the client used by the built-in transports
	// (default to a client with 30s timeout).
	HTTPClient *http.Client

	// Limits are the abuse and cost protection limits.
	Limits Limits

	// PollInterval is how often incoming messages are fetched from VelaStack
	// when the app url isn't public https, so VelaStack can't deliver
	// webhooks (default to 10s).
	PollInterval time.Duration
}

type plugin struct {
	app     core.App
	config  Config
	limiter *windowLimiter

	// incomingMu serializes the webhook registration and the polling.
	incomingMu sync.Mutex
	// registerRetryAt is when a webhook registration that failed for a
	// moment is tried again (zero: nothing to retry), registerRetryDelay
	// the last wait. Both are guarded by incomingMu.
	registerRetryAt    time.Time
	registerRetryDelay time.Duration

	// incoming is the last loaded or saved incoming mode, so the poller
	// doesn't read the store on every tick (nil until it is first known).
	incoming atomic.Pointer[string]
}

// MustRegister registers the whatsapp plugin in the provided app instance
// and panics if it fails.
func MustRegister(app core.App, config Config) {
	if err := Register(app, config); err != nil {
		panic(err)
	}
}

// Register registers the whatsapp plugin in the provided app instance.
func Register(app core.App, config Config) error {
	_, err := register(app, config)
	return err
}

func register(app core.App, config Config) (*plugin, error) {
	config.Limits.setDefaults()
	if config.PollInterval <= 0 {
		config.PollInterval = 10 * time.Second
	}
	if config.HTTPClient == nil {
		config.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}

	p := &plugin{
		app:     app,
		config:  config,
		limiter: newWindowLimiter(),
	}

	registerMigrations()

	// the system migrations are applied on bootstrap so apply them manually
	// in case the plugin is registered after that (eg. in tests)
	if app.IsBootstrapped() {
		if err := app.RunSystemMigrations(); err != nil {
			return nil, err
		}
	}

	uiExt, err := fs.Sub(uiFS, "ui")
	if err != nil {
		return nil, err
	}

	app.OnServe().BindFunc(func(se *core.ServeEvent) error {
		se.UIExtensions = append(se.UIExtensions, core.UIExtension{Name: "whatsapp", FS: uiExt})
		p.bindRoutes(se)
		if err := se.Next(); err != nil {
			return err
		}
		p.startIncoming(se.App)
		return nil
	})

	app.OnBootstrap().BindFunc(func(e *core.BootstrapEvent) error {
		if err := e.Next(); err != nil {
			return err
		}

		e.App.Cron().Add("__whatsappCleanup__", "0 * * * *", func() {
			if err := p.deleteExpiredChallenges(e.App); err != nil {
				e.App.Logger().Warn("Failed to delete expired WhatsApp code challenges", "error", err)
			}
			p.limiter.prune(24 * time.Hour)
		})

		return nil
	})

	app.OnRecordCreateRequest().BindFunc(p.guardPhoneFields)
	app.OnRecordUpdateRequest().BindFunc(p.guardPhoneFields)

	p.protectStore()
	p.protectIncoming()

	// cleanup the related plugin data
	app.OnCollectionAfterDeleteSuccess().BindFunc(func(e *core.CollectionEvent) error {
		if e.Collection.IsAuth() {
			if err := p.deleteStoreValue(e.App, storeKeyCollectionPrefix+e.Collection.Id); err != nil {
				e.App.Logger().Warn("Failed to delete the WhatsApp collection config", "error", err)
			}
			if _, err := e.App.DB().Delete(CollectionNameOTPs, dbx.HashExp{"collectionRef": e.Collection.Id}).Execute(); err != nil {
				e.App.Logger().Warn("Failed to delete the WhatsApp collection code challenges", "error", err)
			}
		}
		return e.Next()
	})
	app.OnRecordAfterDeleteSuccess().BindFunc(func(e *core.RecordEvent) error {
		if e.Record.Collection().IsAuth() {
			if _, err := e.App.DB().Delete(CollectionNameOTPs, dbx.HashExp{"recordRef": e.Record.Id}).Execute(); err != nil {
				e.App.Logger().Warn("Failed to delete the WhatsApp record code challenges", "error", err)
			}
		}
		return e.Next()
	})

	return p, nil
}

func (p *plugin) bindRoutes(se *core.ServeEvent) {
	se.Router.Bind(&hook.Handler[*core.RequestEvent]{
		Id:   "whatsappAuthMethods",
		Func: p.authMethodsMiddleware,
	})

	records := se.Router.Group("/api/collections/{collection}")
	records.POST("/request-whatsapp-otp", p.requestOTP)
	records.POST("/auth-with-whatsapp", p.authWithOTP)
	records.POST("/request-whatsapp-phone-change", p.requestPhoneChange).Bind(apis.RequireAuth())
	records.POST("/confirm-whatsapp-phone-change", p.confirmPhoneChange).Bind(apis.RequireAuth())

	admin := se.Router.Group("/api/whatsapp")
	admin.Bind(apis.RequireSuperuserAuth())
	admin.GET("/settings", p.getSettings)
	admin.PATCH("/settings", p.updateSettings)
	admin.GET("/status", p.getStatus)
	admin.POST("/test", p.sendTest)
	admin.GET("/collections", p.listCollections)
	admin.PATCH("/collections/{collection}", p.updateCollectionConfig)
	admin.POST("/collections/{collection}/setup", p.setupCollection)

	se.Router.POST(webhookPath, p.receiveWebhook)

	admin.GET("/inbox", p.inboxContacts).Bind(apis.SkipSuccessActivityLog())
	admin.GET("/inbox/templates", p.inboxTemplates)
	admin.GET("/inbox/media/{media}", p.inboxMedia)
	admin.GET("/inbox/{contact}", p.inboxThread).Bind(apis.SkipSuccessActivityLog())
	admin.POST("/inbox/{contact}", p.inboxSend)
	admin.POST("/inbox/{contact}/read", p.inboxRead)
}

// webhookPollEvery is how many poll intervals pass between the safety-net
// polls while VelaStack delivers webhooks.
const webhookPollEvery = 6

// incomingMode returns the incoming mode, from memory once it's known.
func (p *plugin) incomingMode(app core.App) string {
	if mode := p.incoming.Load(); mode != nil {
		return *mode
	}
	state, err := p.loadIncomingState(app)
	if err != nil {
		return IncomingOff
	}
	return state.Mode
}

// startIncoming registers the webhook (or picks polling) and starts the
// poller, which only fetches while VelaStack can't deliver webhooks.
func (p *plugin) startIncoming(app core.App) {
	asyncSend(func() {
		if err := p.syncIncoming(app); err != nil {
			app.Logger().Warn("Failed to set up incoming WhatsApp messages", "error", err)
		}
	})

	ticker := time.NewTicker(p.config.PollInterval)
	done := make(chan struct{})
	app.OnTerminate().BindFunc(func(e *core.TerminateEvent) error {
		ticker.Stop()
		close(done)
		return e.Next()
	})

	go func() {
		tick := 0
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				tick++
				// waits, so polls never overlap
				var wg sync.WaitGroup
				routine.FireAndForget(func() {
					if p.registrationDue(time.Now()) {
						if err := p.syncIncoming(app); err != nil {
							app.Logger().Warn("Failed to set up incoming WhatsApp messages", "error", err)
						}
					}
					// with webhooks, only a slow safety net (see pollOnce)
					switch mode := p.incomingMode(app); {
					case mode == IncomingOff:
						return
					case mode == IncomingWebhook && tick%webhookPollEvery != 0:
						return
					}
					if err := p.pollOnce(app); err != nil {
						app.Logger().Debug("Failed to poll incoming WhatsApp messages", "error", err)
					}
				}, &wg)
				wg.Wait()
			}
		}
	}()
}

// transport returns the active code delivery transport
// (nil if WhatsApp delivery is not configured).
func (p *plugin) transport(app core.App) (Transport, error) {
	if p.config.Transport != nil {
		return p.config.Transport, nil
	}

	settings, _, err := p.loadSettings(app)
	if err != nil {
		return nil, err
	}

	switch settings.Mode {
	case ModeDev:
		return &DevTransport{App: app}, nil
	case ModeVelastack:
		return &RelayTransport{
			URL:        settings.Velastack.RelayURL,
			APIKey:     settings.Velastack.APIKey,
			HTTPClient: p.config.HTTPClient,
		}, nil
	case ModeDirect:
		return &DirectTransport{
			PhoneNumberID: settings.Direct.PhoneNumberID,
			WABAID:        settings.Direct.WABAID,
			AccessToken:   settings.Direct.AccessToken,
			TemplateName:  settings.Direct.TemplateName,
			Languages:     settings.Direct.Languages,
			GraphVersion:  settings.Direct.GraphVersion,
			HTTPClient:    p.config.HTTPClient,
		}, nil
	default:
		return nil, nil
	}
}

func (p *plugin) forwardClientIP(app core.App) bool {
	if p.config.Transport != nil {
		return false
	}

	settings, _, err := p.loadSettings(app)
	if err != nil {
		return false
	}

	return settings.Mode == ModeVelastack && settings.Velastack.ForwardClientIP
}
