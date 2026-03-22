package admin

import (
	"html/template"
	"net/http"
	"time"

	"github.com/dmksnnk/star/web"
)

func Landing(every time.Duration, burst int) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", newLandingHandler(every, burst))

	return mux
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
