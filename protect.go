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
