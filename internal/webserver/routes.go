package webserver

import "net/http"

func AddRoutes(mux *http.ServeMux, ws *WebServer) {
	mux.HandleFunc("/", ws.Index)
	mux.HandleFunc("/start-host", ws.StartHost)
	mux.HandleFunc("/start-host-advanced", ws.StartHostAdvanced)
	mux.HandleFunc("/stop-host", ws.StopServer)
	mux.HandleFunc("/connect", ws.Connect)
	mux.HandleFunc("/disconnect", ws.Disconnect)
	mux.HandleFunc("/close", ws.Close)
	mux.HandleFunc("/logs", ws.Logs)
}
