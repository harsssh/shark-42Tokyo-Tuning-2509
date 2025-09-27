package chi

import (
	"github.com/go-chi/chi/v5"
	"github.com/kaz/pprotein/integration"
)

func Integrate(r *chi.Mux) {
	EnableDebugHandler(r)
	EnableDebugMode(r)
}

func EnableDebugHandler(r *chi.Mux) {
	r.Handle("/debug/*", integration.NewDebugHandler())
}

func EnableDebugMode(r *chi.Mux) {
	return
}
