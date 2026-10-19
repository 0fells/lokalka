package admin

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeSvc struct {
	public  string
	views   []NetworkView
	label   string
	body    []byte
	fileErr error
}

func (f *fakeSvc) CreateNetwork(name, password string) error      { return nil }
func (f *fakeSvc) DeleteNetwork(name string) error                { return nil }
func (f *fakeSvc) SetPassword(name, password string) error        { return nil }
func (f *fakeSvc) NetworkPassword(network string) (string, error) { return "", nil }
func (f *fakeSvc) DisconnectPeer(network, ip string) error        { return nil }
func (f *fakeSvc) DisconnectAll(network string) error             { return nil }
func (f *fakeSvc) AllowPeer(network, hostname string) error       { return nil }
func (f *fakeSvc) CreateConfig(network, label, password string) error {
	return nil
}
func (f *fakeSvc) RenameConfig(network, id, label string) error { return nil }
func (f *fakeSvc) DeleteConfig(network, id string) error        { return nil }
func (f *fakeSvc) ConfigFile(network, id string) (string, []byte, error) {
	return f.label, f.body, f.fileErr
}
func (f *fakeSvc) PublicEndpoint() string  { return f.public }
func (f *fakeSvc) Snapshot() []NetworkView { return f.views }

func TestNetworkListHidesPasswords(t *testing.T) {
	svc := &fakeSvc{views: []NetworkView{{
		Name:    "coop",
		Online:  0,
		Configs: []ConfigView{{ID: "abc", Label: "ноутбук", Created: 10}},
	}}}
	res := get(t, Handler(svc), "/api/networks")
	body, _ := io.ReadAll(res.Body)
	if strings.Contains(string(body), "password") || strings.Contains(string(body), "secret") {
		t.Fatalf("список содержит секрет: %s", body)
	}
	var nets []NetworkView
	if err := json.Unmarshal(body, &nets); err != nil {
		t.Fatal(err)
	}
	if len(nets) != 1 || nets[0].Configs[0].Label != "ноутбук" {
		t.Fatalf("ответ: %+v", nets)
	}
}

func TestDownloadConfig(t *testing.T) {
	raw, err := ClientConfig("203.0.113.10:51820", "coop", "secret", "abc", "ноутбук", "")
	if err != nil {
		t.Fatal(err)
	}
	svc := &fakeSvc{public: "203.0.113.10:51820", label: "ноутбук", body: raw}
	res := get(t, Handler(svc), "/api/networks/coop/configs/abc/file")
	if res.StatusCode != http.StatusOK {
		t.Fatal(res.Status)
	}
	disp := res.Header.Get("Content-Disposition")
	if !strings.Contains(disp, "lokalka.lokalka") || !strings.Contains(disp, "filename*=") {
		t.Fatalf("имя файла: %s", disp)
	}
	var file struct {
		Version  int    `json:"lokalka"`
		Server   string `json:"server"`
		Network  string `json:"network"`
		Password string `json:"password"`
		ConfigID string `json:"config_id"`
		Label    string `json:"label"`
	}
	if err := json.NewDecoder(res.Body).Decode(&file); err != nil {
		t.Fatal(err)
	}
	if file.Version != 1 || file.Server != "203.0.113.10:51820" || file.Network != "coop" || file.Password != "secret" || file.ConfigID != "abc" || file.Label != "ноутбук" {
		t.Fatalf("файл: %+v", file)
	}
}

func TestRejectWildcardEndpoint(t *testing.T) {
	if _, err := NormalizeEndpoint("0.0.0.0:51820"); err == nil {
		t.Fatal("0.0.0.0 не должен попадать в конфиг")
	}
}

func get(t *testing.T, h http.Handler, path string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Host = "127.0.0.1:8756"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Result()
}

func TestOnlyLocalRequests(t *testing.T) {
	h := Handler(&fakeSvc{})
	cases := []struct {
		name, method, host, origin, ctype string
		want                              int
	}{
		{"свой адрес", http.MethodGet, "127.0.0.1:8756", "", "", http.StatusOK},
		{"localhost", http.MethodGet, "localhost:8756", "", "", http.StatusOK},
		{"подмена DNS", http.MethodGet, "evil.example:8756", "", "", http.StatusForbidden},
		{"чужая страница", http.MethodPost, "127.0.0.1:8756", "http://evil.example", "application/json", http.StatusForbidden},
		{"форма text/plain", http.MethodPost, "127.0.0.1:8756", "", "text/plain", http.StatusUnsupportedMediaType},
		{"своя страница", http.MethodPost, "127.0.0.1:8756", "http://127.0.0.1:8756", "application/json", http.StatusCreated},
	}
	for _, tc := range cases {
		path := "/api/networks"
		var body io.Reader
		if tc.method == http.MethodPost {
			body = strings.NewReader(`{"name":"x","password":"secret"}`)
		}
		req := httptest.NewRequest(tc.method, path, body)
		req.Host = tc.host
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}
		if tc.ctype != "" {
			req.Header.Set("Content-Type", tc.ctype)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("%s: код %d, ждали %d", tc.name, rec.Code, tc.want)
		}
	}
}
