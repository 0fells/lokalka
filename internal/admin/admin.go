package admin

import (
	"embed"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

//go:embed web/index.html
var webFS embed.FS

type PeerView struct {
	IP       string `json:"ip"`
	Hostname string `json:"hostname"`
	Endpoint string `json:"endpoint,omitempty"`
	Joined   int64  `json:"joined,omitempty"`
	Path     string `json:"path,omitempty"`
	Config   string `json:"config,omitempty"`
}

type ConfigView struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Created  int64  `json:"created"`
	LastSeen int64  `json:"last_seen,omitempty"`
	LastHost string `json:"last_host,omitempty"`
}

type NetworkView struct {
	Name    string       `json:"name"`
	Online  int          `json:"online"`
	Peers   []PeerView   `json:"peers"`
	Blocked []PeerView   `json:"blocked,omitempty"`
	Configs []ConfigView `json:"configs"`
	Panels  []PanelView  `json:"panels,omitempty"`
}

type PanelView struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Created  int64  `json:"created"`
	LastSeen int64  `json:"last_seen,omitempty"`
	LastHost string `json:"last_host,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
	Online   bool   `json:"online,omitempty"`
}

type Service interface {
	CreateNetwork(name, password string) error
	DeleteNetwork(name string) error
	SetPassword(name, password string) error
	NetworkPassword(network string) (string, error)
	DisconnectPeer(network, ip string) error
	DisconnectAll(network string) error
	AllowPeer(network, hostname string) error
	CreateConfig(network, label, password string) error
	RenameConfig(network, id, label string) error
	DeleteConfig(network, id string) error
	ConfigFile(network, id string) (label string, body []byte, err error)
	PublicEndpoint() string
	Snapshot() []NetworkView
}

func Handler(svc Service) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		b, err := webFS.ReadFile("web/index.html")
		if err != nil {
			http.Error(w, "страница недоступна", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(b)
	})
	mux.HandleFunc("GET /api/info", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"public": svc.PublicEndpoint()})
	})
	mux.HandleFunc("GET /api/networks", func(w http.ResponseWriter, r *http.Request) {
		views := svc.Snapshot()
		sort.Slice(views, func(i, j int) bool { return views[i].Name < views[j].Name })
		for i := range views {
			if views[i].Peers == nil {
				views[i].Peers = []PeerView{}
			}
			if views[i].Configs == nil {
				views[i].Configs = []ConfigView{}
			}
		}
		writeJSON(w, http.StatusOK, views)
	})
	mux.HandleFunc("POST /api/networks", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name     string `json:"name"`
			Password string `json:"password"`
		}
		if err := decodeJSON(r, &body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "некорректный запрос"})
			return
		}
		if err := svc.CreateNetwork(strings.TrimSpace(body.Name), body.Password); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, map[string]string{"ok": "создана"})
	})
	mux.HandleFunc("POST /api/networks/{name}/password", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Password string `json:"password"`
		}
		if err := decodeJSON(r, &body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "некорректный запрос"})
			return
		}
		if err := svc.SetPassword(r.PathValue("name"), body.Password); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"ok": "пароль изменён, участники отключены"})
	})
	mux.HandleFunc("DELETE /api/networks/{name}/peers/{ip}", func(w http.ResponseWriter, r *http.Request) {
		if err := svc.DisconnectPeer(r.PathValue("name"), r.PathValue("ip")); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"ok": "отключён"})
	})
	mux.HandleFunc("DELETE /api/networks/{name}/blocked/{hostname}", func(w http.ResponseWriter, r *http.Request) {
		if err := svc.AllowPeer(r.PathValue("name"), r.PathValue("hostname")); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"ok": "разрешён"})
	})
	mux.HandleFunc("DELETE /api/networks/{name}/peers", func(w http.ResponseWriter, r *http.Request) {
		if err := svc.DisconnectAll(r.PathValue("name")); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"ok": "все отключены"})
	})
	mux.HandleFunc("GET /api/networks/{name}/password", func(w http.ResponseWriter, r *http.Request) {
		pass, err := svc.NetworkPassword(r.PathValue("name"))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"password": pass})
	})
	mux.HandleFunc("POST /api/networks/{name}/configs", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Label    string `json:"label"`
			Password string `json:"password"`
		}
		if err := decodeJSON(r, &body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "некорректный запрос"})
			return
		}
		if err := svc.CreateConfig(r.PathValue("name"), body.Label, body.Password); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]string{"ok": "конфиг создан"})
	})
	mux.HandleFunc("GET /api/networks/{name}/configs/{id}/file", func(w http.ResponseWriter, r *http.Request) {
		label, body, err := svc.ConfigFile(r.PathValue("name"), r.PathValue("id"))
		if err != nil {
			writeErr(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Disposition", ContentDisposition(label))
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	})
	mux.HandleFunc("POST /api/networks/{name}/configs/{id}/rename", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Label string `json:"label"`
		}
		if err := decodeJSON(r, &body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "некорректный запрос"})
			return
		}
		if err := svc.RenameConfig(r.PathValue("name"), r.PathValue("id"), body.Label); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"ok": "переименован"})
	})
	mux.HandleFunc("DELETE /api/networks/{name}/configs/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := svc.DeleteConfig(r.PathValue("name"), r.PathValue("id")); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"ok": "конфиг удалён"})
	})
	mux.HandleFunc("DELETE /api/networks/{name}", func(w http.ResponseWriter, r *http.Request) {
		if err := svc.DeleteNetwork(r.PathValue("name")); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"ok": "удалена"})
	})
	return localOnly(mux)
}

func localOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !loopbackHost(r.Host) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "админка открывается только по адресу 127.0.0.1"})
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || u.Host != r.Host {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "запрос с чужой страницы"})
				return
			}
		}
		if r.Method == http.MethodPost {
			ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if ct != "application/json" {
				writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "нужен JSON"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func loopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func decodeJSON(r *http.Request, dst any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<16))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		return errors.New("лишние данные")
	}
	return nil
}

func writeErr(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if strings.Contains(err.Error(), "не найден") {
		status = http.StatusNotFound
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
