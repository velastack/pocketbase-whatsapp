package main

import (
	"log"

	"github.com/pocketbase/pocketbase"
	"github.com/velastack/pocketbase-whatsapp"
)

func main() {
	app := pocketbase.New()

	whatsapp.MustRegister(app, whatsapp.Config{})

	if err := app.Start(); err != nil {
		log.Fatal(err)
	}
}
