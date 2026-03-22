package admin

import (
	"html/template"
	"io/fs"
	"net/http"
	"time"

	"github.com/dmksnnk/star/web"
)

func AddLanding(mux *http.ServeMux, every time.Duration, burst int) {
	staticFS, _ := fs.Sub(web.Static, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))
	mux.HandleFunc("GET /", newLandingHandler(every, burst))
}

type landingData struct {
	ServerURL          string
	RateLimitPerSecond float64
	RateLimitBurst     int
}

func newLandingHandler(every time.Duration, burst int) http.HandlerFunc {
	base := template.Must(template.ParseFS(web.Templates, "templates/base.html"))
	base = template.Must(base.Clone())
	t := template.Must(base.ParseFS(web.Templates, "templates/registar-landing.html"))

	return func(w http.ResponseWriter, r *http.Request) {
		data := landingData{
			ServerURL:          "https://" + r.Host,
			RateLimitPerSecond: float64(time.Second) / float64(every),
			RateLimitBurst:     burst,
		}
		if err := t.ExecuteTemplate(w, "base", data); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}
