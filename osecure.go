package osecure

import (
	"encoding/base64"
	"encoding/gob"
	"encoding/json"
	"errors"
	"github.com/gorilla/sessions"
	"golang.org/x/oauth2"
	"net/http"
	"sort"
	"time"
)

var (
	SessionExpireTime    = 86400
	PermissionExpireTime = 600
)

func init() {
	gob.Register(&time.Time{})
	gob.Register(&AuthSessionData{})
}

type AuthSessionData struct {
	Token        oauth2.Token
	ExpireAt     time.Time
	Permissions  []string
	PermExpireAt time.Time
}

type CookieConfig struct {
	SigningKey    string `yaml:"signing_key" env:"skey"`
	EncryptionKey string `yaml:"encryption_key" env:"ekey"`
}

type OAuthConfig struct {
	ClientID       string `yaml:"client_id" env:"client_id"`
	Secret         string `yaml:"secret" env:"secret"`
	AuthURL        string `yaml:"auth_url" env:"auth_url"`
	TokenURL       string `yaml:"token_url" env:"token_url"`
	PermissionsURL string `yaml:"permissions_url" env:"permissions_url"`
}

func NewAuthSessionData(token oauth2.Token) *AuthSessionData {
	return &AuthSessionData{
		Token:        token,
		ExpireAt:     time.Now().Add(time.Duration(SessionExpireTime) * time.Second),
		Permissions:  []string{},
		PermExpireAt: time.Time{}, // Zero time
	}
}

func (data *AuthSessionData) IsExpired() bool {
	return data.ExpireAt.Before(time.Now())
}

func (data *AuthSessionData) IsPermExpired() bool {
	return data.PermExpireAt.Before(time.Now())
}

type OAuthSession struct {
	name           string
	cookieStore    *sessions.CookieStore
	client         *oauth2.Config
	permissionsURL string
}

func NewOAuthSession(name string, oauthConf *OAuthConfig, cookieConf *CookieConfig, callbackURL string) *OAuthSession {

	client := &oauth2.Config{
		ClientID:     oauthConf.ClientID,
		ClientSecret: oauthConf.Secret,
		Endpoint: oauth2.Endpoint{
			AuthURL:  oauthConf.AuthURL,
			TokenURL: oauthConf.TokenURL,
		},
		RedirectURL: callbackURL,
	}
	return &OAuthSession{
		name:           name,
		cookieStore:    newCookieStore(cookieConf),
		client:         client,
		permissionsURL: oauthConf.PermissionsURL,
	}
}

func (s *OAuthSession) Secured(h http.Handler) http.Handler {
	fn := func(w http.ResponseWriter, r *http.Request) {
		if !s.isAuthorized(r) {
			s.startOAuth(w, r)
			return
		}
		h.ServeHTTP(w, r)
	}
	return http.HandlerFunc(fn)
}

func (s *OAuthSession) isAuthorized(r *http.Request) bool {
	data := s.getAuthSessionDataFromRequest(r)
	if data == nil || data.IsExpired() {
		return false
	}

	return true
}

func (s *OAuthSession) ensurePermUpdated(w http.ResponseWriter, r *http.Request, data *AuthSessionData) {
	if !data.IsPermExpired() {
		return
	}

	client := oauth2.NewClient(oauth2.NoContext, oauth2.StaticTokenSource(&data.Token))

	resp, err := client.Get(s.permissionsURL)
	if err != nil {
		panic(err)
	}

	var result struct {
		Permissions []string `json:"permissions"`
	}
	err = json.NewDecoder(resp.Body).Decode(&result)
	if err != nil {
		panic(err)
	}

	data.Permissions = result.Permissions
	data.PermExpireAt = time.Now().Add(time.Duration(PermissionExpireTime) * time.Second)

	// Sort the string, as sort.SearchStrings needs sorted []string.
	sort.Strings(data.Permissions)

	s.issueAuthCookie(w, r, data)
	return
}

func (s *OAuthSession) GetPermissions(w http.ResponseWriter, r *http.Request) ([]string, error) {
	data := s.getAuthSessionDataFromRequest(r)
	if data == nil || data.IsExpired() {
		return nil, errors.New("invalid session")
	}

	s.ensurePermUpdated(w, r, data)

	return data.Permissions, nil
}

func (s *OAuthSession) HasPermission(w http.ResponseWriter, r *http.Request, permission string) bool {
	data := s.getAuthSessionDataFromRequest(r)
	if data == nil || data.IsExpired() {
		return false
	}

	s.ensurePermUpdated(w, r, data)

	perms := data.Permissions

	id := sort.SearchStrings(perms, permission)
	result := id < len(perms) && perms[id] == permission

	return result
}

func (s *OAuthSession) getAuthSessionDataFromRequest(r *http.Request) *AuthSessionData {
	session, err := s.cookieStore.Get(r, s.name)
	if err != nil {
		panic(err)
	}

	v, found := session.Values["data"]
	if !found {
		return nil
	}

	data, ok := v.(*AuthSessionData)
	if !ok {
		return nil
	}

	return data

}

func (s *OAuthSession) startOAuth(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, s.client.AuthCodeURL(r.RequestURI), 303)
}

func (s *OAuthSession) CallbackView(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	code := q.Get("code")
	cont := q.Get("state")

	jr, err := s.client.Exchange(oauth2.NoContext, code)

	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	s.issueAuthCookie(w, r, NewAuthSessionData(*jr))
	http.Redirect(w, r, cont, 303)
}

func (s *OAuthSession) issueAuthCookie(w http.ResponseWriter, r *http.Request, data *AuthSessionData) {
	session, err := s.cookieStore.Get(r, s.name)
	if err != nil {
		panic(err)
	}
	session.Values["data"] = data
	session.Save(r, w)
}

func newCookieStore(conf *CookieConfig) *sessions.CookieStore {

	var signingKey, encryptionKey []byte
	var err error

	signingKey, err = base64.StdEncoding.DecodeString(conf.SigningKey)
	if err != nil {
		panic(err)
	}

	encryptionKey, err = base64.StdEncoding.DecodeString(conf.EncryptionKey)
	if err != nil {
		panic(err)
	}

	return sessions.NewCookieStore(signingKey, encryptionKey)
}
