package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi"
	"github.com/go-chi/chi/middleware"
	"github.com/shoobyban/slog"
)

func server(ctx context.Context, d Docker, conf map[string]string) {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	r.Get("/running", func(w http.ResponseWriter, r *http.Request) {
		var list []string
		for _, v := range d.List() {
			list = append(list, strings.TrimPrefix(v.Names[0], "/"))
		}
		w.Write([]byte(strings.Join(list, "\n")))
	})
	r.Get("/details", func(w http.ResponseWriter, r *http.Request) {
		b, _ := json.MarshalIndent(d.List(), "", "  ")
		w.Write(b)
	})
	r.Handle("/", http.FileServer(http.Dir("./webroot/")))

	slog.Infof("Listening on http://localhost:%s/", conf["port"])
	http.ListenAndServe(":"+conf["port"], r)
}
