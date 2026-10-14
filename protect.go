package whatsapp

import (
	"strings"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/hook"
)

// protectStore denies the records API and realtime access to the store
// collection for everyone (incl. superusers, who otherwise could read
// the hidden fields) so that the stored secrets could be accessed only
// through the plugin settings endpoints that redact them.
func (p *plugin) protectStore() {
	deny := func(e *core.RequestEvent) error {
		return e.ForbiddenError("The "+CollectionNameStore+" collection can be managed only from the WhatsApp settings.", nil)
	}

	p.app.OnRecordsListRequest(CollectionNameStore).BindFunc(func(e *core.RecordsListRequestEvent) error {
		return deny(e.RequestEvent)
	})
	p.app.OnRecordViewRequest(CollectionNameStore).BindFunc(func(e *core.RecordRequestEvent) error {
		return deny(e.RequestEvent)
	})
	p.app.OnRecordCreateRequest(CollectionNameStore).BindFunc(func(e *core.RecordRequestEvent) error {
		return deny(e.RequestEvent)
	})
	p.app.OnRecordUpdateRequest(CollectionNameStore).BindFunc(func(e *core.RecordRequestEvent) error {
		return deny(e.RequestEvent)
	})
	p.app.OnRecordDeleteRequest(CollectionNameStore).BindFunc(func(e *core.RecordRequestEvent) error {
		return deny(e.RequestEvent)
	})

	p.app.OnRealtimeMessageSend().Bind(&hook.Handler[*core.RealtimeMessageEvent]{
		Func: func(e *core.RealtimeMessageEvent) error {
			if e.Message != nil && p.isStoreTopic(e.App, e.Message.Name) {
				return nil // skip
			}
			return e.Next()
		},
	})
}

func (p *plugin) isStoreTopic(app core.App, topic string) bool {
	if strings.HasPrefix(topic, CollectionNameStore+"/") {
		return true
	}

	collection, err := app.FindCachedCollectionByNameOrId(CollectionNameStore)
	if err != nil {
		return false
	}

	return strings.HasPrefix(topic, collection.Id+"/")
}

// protectIncoming stops the incoming messages collections from being deleted
// or renamed through the collections API. They are regular collections, so
// that apps can open them up with API rules.
func (p *plugin) protectIncoming() {
	protected := func(app core.App, id string) (*core.Collection, bool) {
		original, err := app.FindCollectionByNameOrId(id)
		if err != nil {
			return nil, false
		}
		return original, original.Name == CollectionNameContacts || original.Name == CollectionNameMessages
	}

	p.app.OnCollectionDeleteRequest().BindFunc(func(e *core.CollectionRequestEvent) error {
		if _, ok := protected(e.App, e.Collection.Id); ok {
			return e.BadRequestError("The "+e.Collection.Name+" collection is managed by the WhatsApp plugin and can't be deleted.", nil)
		}
		return e.Next()
	})

	p.app.OnCollectionUpdateRequest().BindFunc(func(e *core.CollectionRequestEvent) error {
		if original, ok := protected(e.App, e.Collection.Id); ok && original.Name != e.Collection.Name {
			return e.BadRequestError("The "+original.Name+" collection is managed by the WhatsApp plugin and can't be renamed.", nil)
		}
		return e.Next()
	})
}
