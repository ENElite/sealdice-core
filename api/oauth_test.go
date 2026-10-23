package api //nolint:testpackage

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"sealdice-core/api/v2/middleware"
	"sealdice-core/dice"
)

func TestOAuthBindingAndLogin(t *testing.T) {
	var subject atomic.Value
	subject.Store("admin-sub")
	var codes sync.Map
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/authorize":
			q := r.URL.Query()
			if q.Get("response_type") != "code" || q.Get("client_id") != "client" || q.Get("redirect_uri") != "http://127.0.0.1/sd-api/oauth/callback" || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
				t.Error("PKCE challenge missing")
			}
			target, _ := url.Parse(q.Get("redirect_uri"))
			code := oauthRandom()
			codes.Store(code, q.Get("code_challenge"))
			target.RawQuery = url.Values{"code": {code}, "state": {q.Get("state")}}.Encode()
			http.Redirect(w, r, target.String(), http.StatusFound)
		case "/token":
			_ = r.ParseForm()
			challenge, valid := codes.LoadAndDelete(r.Form.Get("code"))
			digest := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if !valid || challenge != base64.RawURLEncoding.EncodeToString(digest[:]) || r.Form.Get("client_secret") != "secret" || r.Form.Get("client_id") != "client" || r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("redirect_uri") != "http://127.0.0.1/sd-api/oauth/callback" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_, _ = fmt.Fprint(w, `{"access_token":"provider-token","token_type":"Bearer"}`)
		case "/userinfo":
			if r.Header.Get("Authorization") != "Bearer provider-token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"sub": subject.Load().(string)})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer provider.Close()
	manager := &dice.DiceManager{UIPasswordHash: "password"}
	manager.AccessTokens.Store("admin", true)
	s := &oauthServer{dm: manager, file: filepath.Join(t.TempDir(), "oauth.json"), pending: make(map[string]oauthTransaction), client: provider.Client()}
	s.config = oauthConfig{AuthorizationURL: provider.URL + "/authorize", TokenURL: provider.URL + "/token", UserinfoURL: provider.URL + "/userinfo", ClientID: "client", ClientSecret: "secret", RedirectURL: "http://127.0.0.1/sd-api/oauth/callback", Scope: "openid"}
	e := echo.New()
	s.register(e)
	e.GET("/protected", func(c echo.Context) error { return c.NoContent(http.StatusNoContent) }, middleware.EchoAuthMiddleware(&dice.Dice{Parent: manager}))
	request := func(method, path, token string, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader("{}"))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Token", token)
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		e.ServeHTTP(w, r)
		return w
	}
	start := func(mode string) (string, *http.Cookie) {
		t.Helper()
		w := request(http.MethodPost, oauthPath+"/"+mode, "admin", nil)
		if w.Code != http.StatusOK {
			t.Fatalf("start %s: %d %s", mode, w.Code, w.Body.String())
		}
		var result struct {
			URL string `json:"url"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &result)
		client := provider.Client()
		client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
		authorized, err := client.Get(result.URL)
		if err != nil {
			t.Fatal(err)
		}
		_ = authorized.Body.Close()
		if authorized.StatusCode != http.StatusFound {
			t.Fatalf("authorization failed: %d", authorized.StatusCode)
		}
		callback, err := url.Parse(authorized.Header.Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		return callback.RequestURI(), w.Result().Cookies()[0]
	}
	if w := request(http.MethodPost, oauthPath+"/login", "", nil); w.Code != http.StatusForbidden {
		t.Fatal("unbound source must reject login")
	}
	callback, cookie := start("bind")
	if w := request(http.MethodGet, callback, "", cookie); w.Code != http.StatusSeeOther || s.config.Subject != "admin-sub" {
		t.Fatalf("bind failed: %d %s", w.Code, w.Body.String())
	}
	if w := request(http.MethodGet, callback, "", cookie); w.Code != http.StatusForbidden {
		t.Fatal("replay accepted")
	}
	callback, cookie = start("login")
	if w := request(http.MethodGet, callback, "", cookie); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "localStorage.setItem('token'") {
		t.Fatalf("login failed: %d %s", w.Code, w.Body.String())
	}
	var issued string
	manager.AccessTokens.Range(func(key string, _ bool) bool {
		if key != "admin" {
			issued = key
		}
		return true
	})
	if w := request(http.MethodGet, "/protected", issued, nil); w.Code != http.StatusNoContent || issued == "" {
		t.Fatal("OAuth token cannot access protected API")
	}
	subject.Store("stranger")
	callback, cookie = start("login")
	if w := request(http.MethodGet, callback, "", cookie); w.Code != http.StatusForbidden {
		t.Fatal("unbound account accepted")
	}
	subject.Store("admin-sub")
	callback, cookie = start("bind")
	if w := request(http.MethodGet, callback, "", &http.Cookie{Name: cookie.Name, Value: "other-browser"}); w.Code != http.StatusForbidden {
		t.Fatal("other browser accepted")
	}
	if w := request(http.MethodGet, callback, "", cookie); w.Code != http.StatusForbidden {
		t.Fatal("consumed state accepted")
	}
	callback, cookie = start("bind")
	manager.AccessTokens.Delete("admin")
	if w := request(http.MethodGet, callback, "", cookie); w.Code != http.StatusForbidden {
		t.Fatal("revoked admin accepted")
	}
	manager.AccessTokens.Store("admin", true)
	callback, cookie = start("login")
	if w := request(http.MethodGet, callback+"&error=access_denied", "", cookie); w.Code != http.StatusForbidden {
		t.Fatal("provider rejection accepted")
	}
	callback, cookie = start("login")
	badCallback, _ := url.Parse(callback)
	badQuery := badCallback.Query()
	badQuery.Set("code", "bad")
	badCallback.RawQuery = badQuery.Encode()
	if w := request(http.MethodGet, badCallback.String(), "", cookie); w.Code != http.StatusForbidden {
		t.Fatal("failed token exchange accepted")
	}
	callback, cookie = start("login")
	s.mu.Lock()
	for key, tx := range s.pending {
		tx.expires = time.Now().Add(-time.Minute)
		s.pending[key] = tx
	}
	s.mu.Unlock()
	if w := request(http.MethodGet, callback, "", cookie); w.Code != http.StatusForbidden {
		t.Fatal("expired state accepted")
	}
	callback, cookie = start("bind")
	if err := s.persist(s.config); err != nil {
		t.Fatal(err)
	}
	if w := request(http.MethodGet, callback, "", cookie); w.Code != http.StatusForbidden {
		t.Fatal("invalidated transaction accepted")
	}
	if w := request(http.MethodGet, oauthPath+"/config", "admin", nil); strings.Contains(w.Body.String(), "secret\"") && strings.Contains(w.Body.String(), ":\"secret\"") {
		t.Fatal("client secret exposed")
	}
}

func TestOAuthConfigurationValidation(t *testing.T) {
	for _, raw := range []string{"http://example.com/auth", "https://user:pass@example.com", "file:///tmp/a", "https://example.com/#fragment"} {
		if oauthValidateURL(raw) {
			t.Errorf("accepted %s", raw)
		}
	}
	for _, raw := range []string{"https://example.com/auth", "http://127.0.0.1/token", "http://localhost/userinfo"} {
		if !oauthValidateURL(raw) {
			t.Errorf("rejected %s", raw)
		}
	}
}

func TestOAuthPersistenceAndFailures(t *testing.T) {
	root := t.TempDir()
	s := &oauthServer{file: filepath.Join(root, "oauth.json"), pending: make(map[string]oauthTransaction)}
	config := oauthConfig{ClientID: "client", ClientSecret: "secret", Subject: "bound-sub", AuthorizationURL: "https://provider.example/auth", TokenURL: "https://provider.example/token", UserinfoURL: "https://provider.example/userinfo", RedirectURL: "https://dice.example/sd-api/oauth/callback"}
	if err := s.persist(config); err != nil {
		t.Fatal(err)
	}
	restarted := &oauthServer{file: s.file}
	restarted.load()
	if restarted.config != config {
		t.Fatal("restart lost OAuth configuration or binding")
	}
	blocker := filepath.Join(root, "blocker")
	if err := os.WriteFile(blocker, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	s.file = filepath.Join(blocker, "oauth.json")
	if err := s.persist(oauthConfig{ClientID: "changed"}); err == nil {
		t.Fatal("write failure ignored")
	}
	if s.config != config {
		t.Fatal("failed write changed the active configuration")
	}
	if err := os.WriteFile(restarted.file, []byte("invalid-json"), 0600); err != nil {
		t.Fatal(err)
	}
	restarted.load()
	if restarted.config != (oauthConfig{}) {
		t.Fatal("invalid persisted source remains enabled")
	}
}

func TestOAuthHTTPConfigurationGuards(t *testing.T) {
	manager := &dice.DiceManager{UIPasswordHash: "password"}
	manager.AccessTokens.Store("admin", true)
	s := &oauthServer{dm: manager, file: filepath.Join(t.TempDir(), "oauth.json"), pending: make(map[string]oauthTransaction)}
	e := echo.New()
	s.register(e)
	request := func(method, path, token, origin, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, oauthPath+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Token", token)
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		e.ServeHTTP(w, r)
		return w
	}
	config := oauthConfig{AuthorizationURL: "https://provider.example/authorize", TokenURL: "https://provider.example/token", UserinfoURL: "https://provider.example/userinfo", ClientID: "client", ClientSecret: "secret", RedirectURL: "https://dice.example/sd-api/oauth/callback", Subject: "injected-sub"}
	data, _ := json.Marshal(config)
	if w := request(http.MethodPut, "/config", "", "", string(data)); w.Code != http.StatusUnauthorized {
		t.Fatal("unauthenticated configuration accepted")
	}
	if w := request(http.MethodPost, "/bind", "", "", "{}"); w.Code != http.StatusUnauthorized {
		t.Fatal("unauthenticated binding accepted")
	}
	manager.UIPasswordHash = ""
	if w := request(http.MethodPut, "/config", "admin", "", string(data)); w.Code != http.StatusBadRequest {
		t.Fatal("source enabled without password")
	}
	manager.UIPasswordHash = "password"
	if w := request(http.MethodPut, "/config", "admin", "", string(data)); w.Code != http.StatusNoContent || s.config.Subject != "" {
		t.Fatalf("save configuration failed: %d %s", w.Code, w.Body.String())
	}
	if w := request(http.MethodPost, "/bind", "admin", "https://attacker.example", "{}"); w.Code != http.StatusForbidden {
		t.Fatal("cross-origin binding accepted")
	}
	if w := request(http.MethodGet, "/config", "admin", "", ""); strings.Contains(w.Body.String(), `"clientSecret":"secret"`) {
		t.Fatal("secret leaked")
	}
	config.RedirectURL = "https://dice.example/incorrect-callback"
	data, _ = json.Marshal(config)
	if w := request(http.MethodPut, "/config", "admin", "", string(data)); w.Code != http.StatusBadRequest {
		t.Fatal("incorrect callback accepted")
	}
	manager.JustForTest = true
	if w := request(http.MethodPost, "/bind", "admin", "", "{}"); w.Code != http.StatusForbidden {
		t.Fatal("demo mode accepted binding")
	}
	if w := request(http.MethodDelete, "/binding", "admin", "", "{}"); w.Code != http.StatusForbidden {
		t.Fatal("demo mode accepted unbinding")
	}
}

func TestOAuthRoutesWithManagementMiddleware(t *testing.T) {
	manager := &dice.DiceManager{UIPasswordHash: "password"}
	manager.AccessTokens.Store("admin", true)
	previousDice := myDice
	myDice = &dice.Dice{Parent: manager}
	t.Cleanup(func() { myDice = previousDice })
	server := &oauthServer{
		dm:      manager,
		file:    filepath.Join(t.TempDir(), "oauth.json"),
		pending: make(map[string]oauthTransaction),
		config:  oauthConfig{AuthorizationURL: "https://provider.example/authorize", TokenURL: "https://provider.example/token", UserinfoURL: "https://provider.example/userinfo", ClientID: "client", RedirectURL: "https://dice.example/sd-api/oauth/callback", Subject: "bound-sub"},
	}
	e := echo.New()
	e.Use(managementAuthMiddleware)
	server.register(e)
	request := func(method, path, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, oauthPath+path, strings.NewReader("{}"))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Token", token)
		}
		w := httptest.NewRecorder()
		e.ServeHTTP(w, r)
		return w
	}
	if w := request(http.MethodGet, "/status", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"enabled":true`) {
		t.Fatalf("anonymous status blocked: %d %s", w.Code, w.Body.String())
	}
	if w := request(http.MethodPost, "/login", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "provider.example/authorize") {
		t.Fatalf("anonymous OAuth login blocked: %d %s", w.Code, w.Body.String())
	}
	if w := request(http.MethodGet, "/callback", ""); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "返回 WebUI") {
		t.Fatalf("public callback did not reach OAuth validation: %d %s", w.Code, w.Body.String())
	}
	for _, route := range []struct{ method, path string }{{http.MethodGet, "/config"}, {http.MethodPut, "/config"}, {http.MethodPost, "/bind"}, {http.MethodDelete, "/binding"}, {http.MethodGet, "/login"}, {http.MethodPost, "/status"}, {http.MethodGet, "/callback/other"}} {
		if w := request(route.method, route.path, ""); w.Code != http.StatusForbidden {
			t.Errorf("anonymous %s %s escaped management auth: %d", route.method, route.path, w.Code)
		}
	}
	if w := request(http.MethodGet, "/config", "admin"); w.Code != http.StatusOK {
		t.Fatalf("authenticated config blocked: %d %s", w.Code, w.Body.String())
	}
	if w := request(http.MethodPost, "/bind", "admin"); w.Code != http.StatusOK {
		t.Fatalf("authenticated binding blocked: %d %s", w.Code, w.Body.String())
	}
	if server.config.Subject != "bound-sub" {
		t.Fatal("starting binding changed the existing binding before callback")
	}
}
