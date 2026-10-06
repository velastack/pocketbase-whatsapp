package whatsapp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/pocketbase/pocketbase/core"
)

const authMethodsPattern = "GET /api/collections/{collection}/auth-methods"

// authMethodsInfo is the "whatsapp" key appended to the auth-methods response.
type authMethodsInfo struct {
	Enabled    bool `json:"enabled"`
	CodeLength int  `json:"codeLength"`
	Duration   int  `json:"duration"`
}

// authMethodsMiddleware appends a "whatsapp" key to the core
// GET /api/collections/{collection}/auth-methods response
// (core doesn't have a dedicated hook for it).
func (p *plugin) authMethodsMiddleware(e *core.RequestEvent) error {
	if e.Request.Pattern != authMethodsPattern {
		return e.Next()
	}

	collection, err := e.App.FindCachedCollectionByNameOrId(e.Request.PathValue("collection"))
	if err != nil || !collection.IsAuth() || collection.Name == core.CollectionNameSuperusers {
		return e.Next()
	}

	cfg, err := p.loadCollectionConfig(e.App, collection.Id)
	if err != nil {
		e.App.Logger().Warn("Failed to load the WhatsApp collection config", "error", err)
		return e.Next()
	}

	info := authMethodsInfo{CodeLength: cfg.CodeLength, Duration: cfg.Duration}
	if cfg.Enabled {
		transport, _ := p.transport(e.App)
		info.Enabled = transport != nil
	}

	original := e.Response
	buffered := &bufferedResponseWriter{ResponseWriter: original}
	e.Response = buffered

	err = e.Next()

	e.Response = original

	body := buffered.body.Bytes()
	if err == nil && buffered.Status() == http.StatusOK {
		body = appendJSONKey(body, "whatsapp", info)
		original.Header().Set("Content-Length", strconv.Itoa(len(body)))
	}

	if buffered.Written() {
		original.WriteHeader(buffered.Status())
		_, _ = original.Write(body)
	}

	return err
}

// appendJSONKey sets key in the raw JSON object (returns the original on failure).
func appendJSONKey(raw []byte, key string, value any) []byte {
	obj := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return raw
	}

	encoded, err := json.Marshal(value)
	if err != nil {
		return raw
	}
	obj[key] = encoded

	result, err := json.Marshal(obj)
	if err != nil {
		return raw
	}

	return result
}

// bufferedResponseWriter buffers the response body and status so that
// it could be modified before writing it to the wrapped writer.
type bufferedResponseWriter struct {
	http.ResponseWriter
	body    bytes.Buffer
	status  int
	written bool
}

func (w *bufferedResponseWriter) WriteHeader(status int) {
	if w.written {
		return
	}
	w.status = status
	w.written = true
}

func (w *bufferedResponseWriter) Write(b []byte) (int, error) {
	if !w.written {
		w.WriteHeader(http.StatusOK)
	}
	return w.body.Write(b)
}

// Written implements the router.WriteTracker interface.
func (w *bufferedResponseWriter) Written() bool {
	return w.written
}

// Status implements the router.StatusTracker interface.
func (w *bufferedResponseWriter) Status() int {
	return w.status
}
