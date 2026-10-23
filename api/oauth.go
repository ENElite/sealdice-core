package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v4"

	"sealdice-core/dice"
)

const oauthPath = "/sd-api/oauth"
const oauthCookie = "sealdice_oauth_transaction"

type oauthConfig struct {
	AuthorizationURL string `json:"authorizationUrl"`
	TokenURL         string `json:"tokenUrl"`
	UserinfoURL      string `json:"userinfoUrl"`
	ClientID         string `json:"clientId"`
	ClientSecret     string `json:"clientSecret"`
	RedirectURL      string `json:"redirectUrl"`
	Scope            string `json:"scope"`
	Subject          string `json:"subject"`
}
type oauthTransaction struct {
	browser, verifier, admin string
	expires                  time.Time
	config                   oauthConfig
}
type oauthServer struct {
	mu      sync.Mutex
	config  oauthConfig
	pending map[string]oauthTransaction
	dm      *dice.DiceManager
	file    string
	client  *http.Client
}

func bindOAuthRoutes(e *echo.Echo, manager *dice.DiceManager) {
	s := &oauthServer{dm: manager, file: "./data/webui-oauth.json", pending: make(map[string]oauthTransaction), client: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}
	s.load()
	s.register(e)
}
func (s *oauthServer) load() {
	if data, err := os.ReadFile(s.file); err == nil {
		if err := json.Unmarshal(data, &s.config); err != nil {
			log.Printf("OAuth configuration is invalid: %v", err)
			s.config = oauthConfig{}
		}
		if s.config.ClientID != "" && !oauthConfigValid(s.config) {
			log.Printf("OAuth configuration has invalid URLs or client ID; disabled")
			s.config = oauthConfig{}
		}
	} else if !os.IsNotExist(err) {
		log.Printf("Cannot read OAuth configuration: %v", err)
	}
}
func (s *oauthServer) register(e *echo.Echo) {
	e.GET(oauthPath+"/status", s.status)
	e.GET(oauthPath+"/config", s.getConfig)
	e.PUT(oauthPath+"/config", s.setConfig)
	e.DELETE(oauthPath+"/binding", s.unbind)
	e.POST(oauthPath+"/bind", s.bind)
	e.POST(oauthPath+"/login", s.login)
	e.GET(oauthPath+"/callback", s.callback)
}
func oauthRandom() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}
func (s *oauthServer) authorized(c echo.Context) bool {
	token := c.Request().Header.Get("Token")
	if token == "" {
		token = strings.TrimPrefix(c.Request().Header.Get("Authorization"), "Bearer ")
	}
	allowed, ok := s.dm.AccessTokens.Load(token)
	return ok && allowed
}
func (s *oauthServer) status(c echo.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return c.JSON(http.StatusOK, map[string]bool{"enabled": s.config.Subject != "" && s.dm.UIPasswordHash != ""})
}
func (s *oauthServer) getConfig(c echo.Context) error {
	if !s.authorized(c) {
		return echo.ErrUnauthorized
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	config := s.config
	config.ClientSecret = ""
	return c.JSON(http.StatusOK, config)
}
func oauthValidateURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Host != "" && u.User == nil && u.Fragment == "" && (u.Scheme == "https" || (u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1")))
}
func oauthConfigValid(config oauthConfig) bool {
	callback, err := url.Parse(config.RedirectURL)
	return oauthValidateURL(config.AuthorizationURL) && oauthValidateURL(config.TokenURL) && oauthValidateURL(config.UserinfoURL) && oauthValidateURL(config.RedirectURL) && strings.TrimSpace(config.ClientID) != "" && err == nil && callback.Path == oauthPath+"/callback" && callback.RawQuery == ""
}
func (s *oauthServer) persist(config oauthConfig) error {
	if err := os.MkdirAll(filepath.Dir(s.file), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.file), ".oauth-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(f.Name(), s.file); err != nil {
		return err
	}
	s.config = config
	s.pending = make(map[string]oauthTransaction)
	return nil
}
func (s *oauthServer) setConfig(c echo.Context) error {
	if !s.authorized(c) {
		return echo.ErrUnauthorized
	}
	if s.dm.JustForTest {
		return echo.ErrForbidden
	}
	var config oauthConfig
	if err := json.NewDecoder(io.LimitReader(c.Request().Body, 16384)).Decode(&config); err != nil {
		return echo.ErrBadRequest
	}
	if s.dm.UIPasswordHash == "" {
		return echo.NewHTTPError(400, "请先设置 WebUI 密码")
	}
	if !oauthValidateURL(config.AuthorizationURL) || !oauthValidateURL(config.TokenURL) || !oauthValidateURL(config.UserinfoURL) || !oauthValidateURL(config.RedirectURL) || strings.TrimSpace(config.ClientID) == "" {
		return echo.NewHTTPError(400, "OAuth URL 或 client ID 无效；仅允许 HTTPS（本机测试可用 HTTP）")
	}
	callback, _ := url.Parse(config.RedirectURL)
	if callback.Path != oauthPath+"/callback" || callback.RawQuery != "" {
		return echo.NewHTTPError(400, "回调路径必须为 /sd-api/oauth/callback")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if config.ClientSecret == "" {
		config.ClientSecret = s.config.ClientSecret
	}
	config.Subject = "" // A changed provider must always be bound again.
	if err := s.persist(config); err != nil {
		return echo.NewHTTPError(500, "OAuth 配置保存失败")
	}
	return c.NoContent(http.StatusNoContent)
}
func (s *oauthServer) unbind(c echo.Context) error {
	if !s.authorized(c) {
		return echo.ErrUnauthorized
	}
	if s.dm.JustForTest {
		return echo.ErrForbidden
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	config := s.config
	config.Subject = ""
	if err := s.persist(config); err != nil {
		return echo.NewHTTPError(500, "解绑保存失败")
	}
	return c.NoContent(204)
}
func (s *oauthServer) bind(c echo.Context) error {
	if !s.authorized(c) {
		return echo.ErrUnauthorized
	}
	if s.dm.JustForTest {
		return echo.ErrForbidden
	}
	token := c.Request().Header.Get("Token")
	if token == "" {
		token = strings.TrimPrefix(c.Request().Header.Get("Authorization"), "Bearer ")
	}
	return s.start(c, token)
}
func (s *oauthServer) login(c echo.Context) error { return s.start(c, "") }
func (s *oauthServer) start(c echo.Context, admin string) error {
	// JSON POST plus same-origin requests prevents third-party forms starting a login.
	if !strings.HasPrefix(c.Request().Header.Get("Content-Type"), "application/json") {
		return echo.ErrBadRequest
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dm.UIPasswordHash == "" || s.config.ClientID == "" || (admin == "" && s.config.Subject == "") {
		return echo.ErrForbidden
	}
	callbackURL, err := url.Parse(s.config.RedirectURL)
	if err != nil || callbackURL.Host == "" {
		return echo.ErrForbidden
	}
	if origin := c.Request().Header.Get("Origin"); origin != "" && origin != callbackURL.Scheme+"://"+callbackURL.Host {
		return echo.ErrForbidden
	}
	now := time.Now()
	for state, tx := range s.pending {
		if now.After(tx.expires) {
			delete(s.pending, state)
		}
	}
	if len(s.pending) >= 1024 {
		return echo.NewHTTPError(429, "too many OAuth requests")
	}
	state, browser, verifier := oauthRandom(), oauthRandom(), oauthRandom()
	s.pending[state] = oauthTransaction{browser: browser, verifier: verifier, admin: admin, expires: now.Add(5 * time.Minute), config: s.config}
	callback, _ := url.Parse(s.config.RedirectURL)
	c.SetCookie(&http.Cookie{Name: oauthCookie, Value: browser, Path: oauthPath, HttpOnly: true, Secure: callback.Scheme == "https", SameSite: http.SameSiteLaxMode, MaxAge: 300})
	u, _ := url.Parse(s.config.AuthorizationURL)
	q := u.Query()
	challenge := sha256.Sum256([]byte(verifier))
	q.Set("response_type", "code")
	q.Set("client_id", s.config.ClientID)
	q.Set("redirect_uri", s.config.RedirectURL)
	q.Set("scope", s.config.Scope)
	q.Set("state", state)
	q.Set("code_challenge_method", "S256")
	q.Set("code_challenge", base64.RawURLEncoding.EncodeToString(challenge[:]))
	u.RawQuery = q.Encode()
	return c.JSON(200, map[string]string{"url": u.String()})
}
func (s *oauthServer) exchange(c echo.Context, tx oauthTransaction) (string, error) {
	form := url.Values{"grant_type": {"authorization_code"}, "code": {c.QueryParam("code")}, "redirect_uri": {tx.config.RedirectURL}, "client_id": {tx.config.ClientID}, "client_secret": {tx.config.ClientSecret}, "code_verifier": {tx.verifier}}
	req, err := http.NewRequestWithContext(c.Request().Context(), http.MethodPost, tx.config.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var token struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
	}
	if res.StatusCode != 200 || json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&token) != nil || token.AccessToken == "" || !strings.EqualFold(token.TokenType, "Bearer") {
		return "", errors.New("token exchange failed")
	}
	req, err = http.NewRequestWithContext(c.Request().Context(), http.MethodGet, tx.config.UserinfoURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	user, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer user.Body.Close()
	var identity struct {
		Subject string `json:"sub"`
	}
	if user.StatusCode != 200 || json.NewDecoder(io.LimitReader(user.Body, 1<<20)).Decode(&identity) != nil || identity.Subject == "" || len(identity.Subject) > 512 {
		return "", errors.New("userinfo failed")
	}
	return identity.Subject, nil
}
func oauthFailure(c echo.Context, status int, message string) error {
	c.Response().Header().Set("Content-Security-Policy", "default-src 'none'; base-uri 'none'; frame-ancestors 'none'")
	return c.HTML(status, "<!doctype html><meta charset=utf-8><title>OAuth 登录失败</title><p>"+html.EscapeString(message)+"</p><a href=\"/v2ui/\">返回 WebUI 重新登录</a>")
}
func (s *oauthServer) callback(c echo.Context) error {
	c.Response().Header().Set("Cache-Control", "no-store")
	c.Response().Header().Set("Referrer-Policy", "no-referrer")
	s.mu.Lock()
	state := c.QueryParam("state")
	tx, ok := s.pending[state]
	delete(s.pending, state)
	s.mu.Unlock()
	cookie, err := c.Cookie(oauthCookie)
	if !ok || err != nil || cookie.Value != tx.browser || time.Now().After(tx.expires) || c.QueryParam("error") != "" || c.QueryParam("code") == "" {
		return oauthFailure(c, 403, "OAuth 登录失败或已过期，请重新发起")
	}
	c.SetCookie(&http.Cookie{Name: oauthCookie, Value: "", Path: oauthPath, HttpOnly: true, MaxAge: -1, SameSite: http.SameSiteLaxMode})
	subject, err := s.exchange(c, tx)
	if err != nil {
		return oauthFailure(c, 403, "OAuth 第三方登录失败")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if tx.config != s.config || s.dm.UIPasswordHash == "" {
		return oauthFailure(c, 403, "OAuth 配置或管理员会话已失效")
	}
	if tx.admin != "" {
		allowed, exists := s.dm.AccessTokens.Load(tx.admin)
		if !exists || !allowed {
			return oauthFailure(c, 403, "OAuth 配置或管理员会话已失效")
		}
		config := s.config
		config.Subject = subject
		if err := s.persist(config); err != nil {
			return oauthFailure(c, 500, "OAuth 绑定保存失败")
		}
		return c.Redirect(303, "/v2ui/#/misc/base-setting?oauth=bound")
	}
	if subject != s.config.Subject {
		return oauthFailure(c, 403, "该第三方账号未绑定")
	}
	// A short same-origin HTML response installs the existing WebUI token without putting it in a URL.
	token := oauthRandom() + oauthRandom()
	s.dm.AccessTokens.Store(token, true)
	s.dm.Save()
	nonce := oauthRandom()
	c.Response().Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'nonce-"+nonce+"'; base-uri 'none'; frame-ancestors 'none'")
	encoded, _ := json.Marshal(token)
	return c.HTML(200, "<!doctype html><meta charset=utf-8><title>OAuth 登录成功</title><script nonce=\""+nonce+"\">localStorage.setItem('token',"+string(encoded)+");location.replace('/v2ui/');</script>")
}
