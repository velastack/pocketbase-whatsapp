package whatsapp

import (
	"sync"

	"github.com/pocketbase/pocketbase/core"
)

const (
	// CollectionNameStore is the name of the system collection that stores
	// the plugin settings and per-collection configs (superusers only).
	CollectionNameStore = "_whatsapp"

	// CollectionNameOTPs is the name of the system collection that stores
	// the pending WhatsApp code challenges.
	CollectionNameOTPs = "_whatsappOtps"
)

var registerMigrationsOnce sync.Once

func registerMigrations() {
	registerMigrationsOnce.Do(func() {
		core.SystemMigrations.Register(migrateUp, migrateDown, "1791000000_whatsapp_init.go")
	})
}

func migrateUp(txApp core.App) error {
	store := core.NewBaseCollection(CollectionNameStore)
	store.System = true
	store.Fields.Add(
		&core.TextField{Name: "key", System: true, Required: true, Max: 255},
		&core.TextField{Name: "value", System: true, Hidden: true, Max: 200000},
		&core.AutodateField{Name: "created", System: true, OnCreate: true},
		&core.AutodateField{Name: "updated", System: true, OnCreate: true, OnUpdate: true},
	)
	store.AddIndex("idx_whatsapp_key", true, "key", "")
	if err := txApp.Save(store); err != nil {
		return err
	}

	otps := core.NewBaseCollection(CollectionNameOTPs)
	otps.System = true
	otps.Fields.Add(
		&core.TextField{Name: "collectionRef", System: true, Required: true},
		&core.TextField{Name: "recordRef", System: true},
		&core.TextField{Name: "phone", System: true, Required: true, Hidden: true},
		&core.TextField{Name: "purpose", System: true, Required: true},
		&core.PasswordField{Name: "password", System: true, Hidden: true, Required: true, Cost: 8},
		&core.NumberField{Name: "attempts", System: true, OnlyInt: true},
		&core.TextField{Name: "messageId", System: true, Hidden: true},
		&core.AutodateField{Name: "created", System: true, OnCreate: true},
		&core.AutodateField{Name: "updated", System: true, OnCreate: true, OnUpdate: true},
	)
	otps.AddIndex("idx_whatsappOtps_collectionRef_phone", false, "collectionRef, phone", "")
	otps.AddIndex("idx_whatsappOtps_recordRef", false, "recordRef", "")
	otps.AddIndex("idx_whatsappOtps_created", false, "created", "")

	return txApp.Save(otps)
}

func migrateDown(txApp core.App) error {
	for _, name := range []string{CollectionNameOTPs, CollectionNameStore} {
		col, err := txApp.FindCollectionByNameOrId(name)
		if err != nil {
			continue
		}

		// system collections can't be deleted directly
		// (and the system flag change is not allowed by the collection validator)
		col.System = false
		if err := txApp.SaveNoValidate(col); err != nil {
			return err
		}
		if err := txApp.Delete(col); err != nil {
			return err
		}
	}

	return nil
}
