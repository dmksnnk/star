package admin

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"sync"
	"time"

	"github.com/dmksnnk/star/internal/registar"
	"github.com/dmksnnk/star/internal/registar/auth"
	"github.com/dmksnnk/star/web"
)

const bwWindow = 24 * time.Hour

type bwBucket struct {
	T   int64   `json:"t"`
	BPS float64 `json:"bps"`
}

// Admin serves the admin UI: stats, active hosts, bandwidth graph, and live events.
type Admin struct {
	srv    *registar.Server
	secret string
	tmpl   *template.Template

	hostsMu     sync.RWMutex
	activeHosts map[auth.Key]registar.AddrPair

	subsMu      sync.Mutex
	subscribers map[chan string]struct{}

	bwMu       sync.RWMutex
	bwRing     []bwBucket
	bwInterval time.Duration
	bwHead     int
	bwCount    int
	bwLast     uint64
	scraperWg  sync.WaitGroup

	stopped chan struct{}
}

// NewAdmin adds server callbacks and starts the bandwidth scraping goroutine.
// scrapeInterval controls how often bandwidth is sampled; the ring buffer holds 24h / scrapeInterval buckets.
// It must be called before the server starts accepting connections.
func NewAdmin(srv *registar.Server, secret string, scrapeInterval time.Duration) *Admin {
	a := &Admin{
		srv:         srv,
		secret:      secret,
		tmpl:        parseTemplates(),
		activeHosts: make(map[auth.Key]registar.AddrPair),
		subscribers: make(map[chan string]struct{}),
		bwRing:      make([]bwBucket, int(bwWindow/scrapeInterval)),
		bwInterval:  scrapeInterval,
		stopped:     make(chan struct{}),
	}

	srv.HostConnected = func(key auth.Key, addrs registar.AddrPair) {
		a.hostsMu.Lock()
		a.activeHosts[key] = addrs
		a.hostsMu.Unlock()

		a.broadcast(fmt.Sprintf("host connected key=%s public=%s private=%s",
			key, addrs.Public, addrs.Private))
	}

	srv.HostDisconnected = func(key auth.Key, _ registar.AddrPair) {
		a.hostsMu.Lock()
		delete(a.activeHosts, key)
		a.hostsMu.Unlock()

		a.broadcast(fmt.Sprintf("host disconnected key=%s", key))
	}

	srv.PeerConnected = func(key auth.Key, addrs registar.AddrPair) {
		a.broadcast(fmt.Sprintf("peer connected key=%s public=%s private=%s",
			key, addrs.Public, addrs.Private))
	}

	a.scraperWg.Go(a.runBandwidthScraper)

	return a
}

func (a *Admin) Stop() {
	close(a.stopped)
	a.scraperWg.Wait()
}

func (a *Admin) handleIndex(w http.ResponseWriter, r *http.Request) {
	if err := a.tmpl.ExecuteTemplate(w, "base", nil); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (a *Admin) handleEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	rc := http.NewResponseController(w)
	if err := rc.Flush(); err != nil {
		http.Error(w, "failed to flush response", http.StatusInternalServerError)
		return
	}

	ch := make(chan string, 10)
	a.subsMu.Lock()
	a.subscribers[ch] = struct{}{}
	a.subsMu.Unlock()

	defer func() {
		a.subsMu.Lock()
		delete(a.subscribers, ch)
		close(ch)
		a.subsMu.Unlock()
	}()

	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", msg)
			_ = rc.Flush()
		case <-r.Context().Done():
			return
		case <-a.stopped:
			return
		}
	}
}

type hostInfo struct {
	Key     string `json:"key"`
	Public  string `json:"public"`
	Private string `json:"private"`
}

type statsResponse struct {
	RegisteredConns int        `json:"registered_conns"`
	WaitingRelays   int        `json:"waiting_relays"`
	BytesRelayed    uint64     `json:"bytes_relayed"`
	P2PConns        uint64     `json:"p2p_conns"`
	RelayConns      uint64     `json:"relay_conns"`
	ActiveHosts     []hostInfo `json:"active_hosts"`
}

func (a *Admin) handleStats(w http.ResponseWriter, r *http.Request) {
	stats := a.srv.Stats()

	a.hostsMu.RLock()
	hosts := make([]hostInfo, 0, len(a.activeHosts))
	for key, addrs := range a.activeHosts {
		hosts = append(hosts, hostInfo{
			Key:     key.String(),
			Public:  addrs.Public.String(),
			Private: addrs.Private.String(),
		})
	}
	a.hostsMu.RUnlock()

	resp := statsResponse{
		RegisteredConns: stats.RegisteredConns,
		WaitingRelays:   stats.WaitingRelays,
		BytesRelayed:    stats.BytesRelayed,
		P2PConns:        stats.P2PConns,
		RelayConns:      stats.RelayConns,
		ActiveHosts:     hosts,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

type bandwidthResponse struct {
	Buckets []bwBucket `json:"buckets"`
}

func (a *Admin) handleBandwidth(w http.ResponseWriter, r *http.Request) {
	a.bwMu.RLock()
	count := a.bwCount
	head := a.bwHead
	ring := a.bwRing
	a.bwMu.RUnlock()

	size := len(ring)
	buckets := make([]bwBucket, count)
	start := (head - count + size) % size
	for i := range count {
		buckets[i] = ring[(start+i)%size]
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(bandwidthResponse{Buckets: buckets})
}

func (a *Admin) runBandwidthScraper() {
	ticker := time.NewTicker(a.bwInterval)
	defer ticker.Stop()

	for {
		select {
		case t := <-ticker.C:
			current := a.srv.Stats().BytesRelayed

			a.bwMu.Lock()
			size := len(a.bwRing)
			delta := current - a.bwLast
			a.bwLast = current
			a.bwRing[a.bwHead] = bwBucket{
				T:   t.Unix(),
				BPS: float64(delta) / a.bwInterval.Seconds(),
			}
			a.bwHead = (a.bwHead + 1) % size
			if a.bwCount < size {
				a.bwCount++
			}
			a.bwMu.Unlock()

		case <-a.stopped:
			return
		}
	}
}

func (a *Admin) broadcast(msg string) {
	a.subsMu.Lock()
	defer a.subsMu.Unlock()

	for ch := range a.subscribers {
		select {
		case ch <- msg:
		default: // drop if subscriber is slow
		}
	}
}

func (a *Admin) basicAuth(next http.HandlerFunc) http.HandlerFunc {
	if a.secret == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, password, ok := r.BasicAuth()
		if !ok || subtle.ConstantTimeCompare([]byte(password), []byte(a.secret)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="admin"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func AdminRoutes(a *Admin) *http.ServeMux {
	mux := http.NewServeMux()

	mux.Handle("GET /", a.basicAuth(a.handleIndex))
	mux.Handle("GET /events", a.basicAuth(a.handleEvents))
	mux.Handle("GET /stats", a.basicAuth(a.handleStats))
	mux.Handle("GET /bandwidth", a.basicAuth(a.handleBandwidth))
	return mux
}

func parseTemplates() *template.Template {
	base := template.Must(template.ParseFS(web.Templates, "templates/base.html"))
	base = template.Must(base.Clone())
	tmpl := template.Must(base.ParseFS(web.Templates, "templates/admin.html"))

	return tmpl
}
