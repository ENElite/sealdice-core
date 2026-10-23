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
			http.Redirect(w, r, target.String(), 302)
		case "/token":
			_ = r.ParseForm()
			challenge, valid := codes.LoadAndDelete(r.Form.Get("code"))
			digest := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if !valid || challenge != base64.RawURLEncoding.EncodeToString(digest[:]) || r.Form.Get("client_secret") != "secret" || r.Form.Get("client_id") != "client" || r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("redirect_uri") != "http://127.0.0.1/sd-api/oauth/callback" {
				w.WriteHeader(400)
				return
			}
			_, _ = fmt.Fprint(w, `{"access_token":"provider-token","token_type":"Bearer"}`)
		case "/userinfo":
			if r.Header.Get("Authorization") != "Bearer provider-token" {
				w.WriteHeader(401)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"sub": subject.Load().(string)})
		default:
			w.WriteHeader(404)
		}
	}))
	defer provider.Close()
	dm := &dice.DiceManager{UIPasswordHash: "password"}
	dm.AccessTokens.Store("admin", true)
	s := &oauthServer{dm: dm, file: filepath.Join(t.TempDir(), "oauth.json"), pending: make(map[string]oauthTransaction), client: provider.Client()}
	s.config = oauthConfig{AuthorizationURL: provider.URL + "/authorize", TokenURL: provider.URL + "/token", UserinfoURL: provider.URL + "/userinfo", ClientID: "client", ClientSecret: "secret", RedirectURL: "http://127.0.0.1/sd-api/oauth/callback", Scope: "openid"}
	e := echo.New()
	s.register(e)
	e.GET("/protected", func(c echo.Context) error { return c.NoContent(204) }, middleware.EchoAuthMiddleware(&dice.Dice{Parent: dm}))
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
		w := request("POST", oauthPath+"/"+mode, "admin", nil)
		if w.Code != 200 {
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
		if authorized.StatusCode != 302 {
			t.Fatalf("authorization failed: %d", authorized.StatusCode)
		}
		callback, err := url.Parse(authorized.Header.Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		return callback.RequestURI(), w.Result().Cookies()[0]
	}
	if w := request("POST", oauthPath+"/login", "", nil); w.Code != 403 {
		t.Fatal("unbound source must reject login")
	}
	callback, cookie := start("bind")
	if w := request("GET", callback, "", cookie); w.Code != 303 || s.config.Subject != "admin-sub" {
		t.Fatalf("bind failed: %d %s", w.Code, w.Body.String())
	}
	if w := request("GET", callback, "", cookie); w.Code != 403 {
		t.Fatal("replay accepted")
	}
	callback, cookie = start("login")
	if w := request("GET", callback, "", cookie); w.Code != 200 || !strings.Contains(w.Body.String(), "localStorage.setItem('token'") {
		t.Fatalf("login failed: %d %s", w.Code, w.Body.String())
	}
	var issued string
	dm.AccessTokens.Range(func(key string, _ bool) bool {
		if key != "admin" {
			issued = key
		}
		return true
	})
	if w := request("GET", "/protected", issued, nil); w.Code != 204 || issued == "" {
		t.Fatal("OAuth token cannot access protected API")
	}
	subject.Store("stranger")
	callback, cookie = start("login")
	if w := request("GET", callback, "", cookie); w.Code != 403 {
		t.Fatal("unbound account accepted")
	}
	subject.Store("admin-sub")
	callback, cookie = start("bind")
	if w := request("GET", callback, "", &http.Cookie{Name: cookie.Name, Value: "other-browser"}); w.Code != 403 {
		t.Fatal("other browser accepted")
	}
	if w := request("GET", callback, "", cookie); w.Code != 403 {
		t.Fatal("consumed state accepted")
	}
	callback, cookie = start("bind")
	dm.AccessTokens.Delete("admin")
	if w := request("GET", callback, "", cookie); w.Code != 403 {
		t.Fatal("revoked admin accepted")
	}
	dm.AccessTokens.Store("admin", true)
	callback, cookie = start("login")
	if w := request("GET", callback+"&error=access_denied", "", cookie); w.Code != 403 {
		t.Fatal("provider rejection accepted")
	}
	callback, cookie = start("login")
	badCallback, _ := url.Parse(callback)
	badQuery := badCallback.Query()
	badQuery.Set("code", "bad")
	badCallback.RawQuery = badQuery.Encode()
	if w := request("GET", badCallback.String(), "", cookie); w.Code != 403 {
		t.Fatal("failed token exchange accepted")
	}
	callback, cookie = start("login")
	s.mu.Lock()
	for key, tx := range s.pending {
		tx.expires = time.Now().Add(-time.Minute)
		s.pending[key] = tx
	}
	s.mu.Unlock()
	if w := request("GET", callback, "", cookie); w.Code != 403 {
		t.Fatal("expired state accepted")
	}
	callback, cookie = start("bind")
	if err := s.persist(s.config); err != nil {
		t.Fatal(err)
	}
	if w := request("GET", callback, "", cookie); w.Code != 403 {
		t.Fatal("invalidated transaction accepted")
	}
	if w := request("GET", oauthPath+"/config", "admin", nil); strings.Contains(w.Body.String(), "secret\"") && strings.Contains(w.Body.String(), ":\"secret\"") {
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
	dm := &dice.DiceManager{UIPasswordHash: "password"}
	dm.AccessTokens.Store("admin", true)
	s := &oauthServer{dm: dm, file: filepath.Join(t.TempDir(), "oauth.json"), pending: make(map[string]oauthTransaction)}
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
	if w := request("PUT", "/config", "", "", string(data)); w.Code != 401 {
		t.Fatal("unauthenticated configuration accepted")
	}
	if w := request("POST", "/bind", "", "", "{}"); w.Code != 401 {
		t.Fatal("unauthenticated binding accepted")
	}
	dm.UIPasswordHash = ""
	if w := request("PUT", "/config", "admin", "", string(data)); w.Code != 400 {
		t.Fatal("source enabled without password")
	}
	dm.UIPasswordHash = "password"
	if w := request("PUT", "/config", "admin", "", string(data)); w.Code != 204 || s.config.Subject != "" {
		t.Fatalf("save configuration failed: %d %s", w.Code, w.Body.String())
	}
	if w := request("POST", "/bind", "admin", "https://attacker.example", "{}"); w.Code != 403 {
		t.Fatal("cross-origin binding accepted")
	}
	if w := request("GET", "/config", "admin", "", ""); strings.Contains(w.Body.String(), `"clientSecret":"secret"`) {
		t.Fatal("secret leaked")
	}
	config.RedirectURL = "https://dice.example/incorrect-callback"
	data, _ = json.Marshal(config)
	if w := request("PUT", "/config", "admin", "", string(data)); w.Code != 400 {
		t.Fatal("incorrect callback accepted")
	}
	dm.JustForTest = true
	if w := request("POST", "/bind", "admin", "", "{}"); w.Code != 403 {
		t.Fatal("demo mode accepted binding")
	}
	if w := request("DELETE", "/binding", "admin", "", "{}"); w.Code != 403 {
		t.Fatal("demo mode accepted unbinding")
	}
}
