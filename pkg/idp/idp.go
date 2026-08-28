package idp

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"html/template"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
	"github.com/ory/fosite"
	"github.com/ory/fosite/compose"
	"github.com/ory/fosite/token/jwt"
	"github.com/sigbit/mcp-auth-proxy/v2/pkg/auth"
	"github.com/sigbit/mcp-auth-proxy/v2/pkg/repository"
	"github.com/sigbit/mcp-auth-proxy/v2/pkg/utils"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

type IDPRouter struct {
	repo        repository.Repository
	privKey     *rsa.PrivateKey
	logger      *zap.Logger
	externalURL string
	hasher      fosite.Hasher
	provider    fosite.OAuth2Provider
	signer      *jwt.DefaultSigner
	authRouter  *auth.AuthRouter
}

func NewIDPRouter(
	repo repository.Repository,
	privKey *rsa.PrivateKey,
	logger *zap.Logger,
	externalURL string,
	secret []byte,
	authRouter *auth.AuthRouter,
) (*IDPRouter, error) {
	hasher := &fosite.BCrypt{
		Config: &fosite.Config{
			HashCost: bcrypt.DefaultCost,
		},
	}
	config := &fosite.Config{
		GlobalSecret:                   secret,
		AccessTokenLifespan:            24 * time.Hour,
		RefreshTokenLifespan:           30 * 24 * time.Hour,
		RefreshTokenScopes:             []string{},
		AccessTokenIssuer:              externalURL,
		EnforcePKCE:                    false,
		EnforcePKCEForPublicClients:    true,
		EnablePKCEPlainChallengeMethod: false,
		ScopeStrategy:                  fosite.HierarchicScopeStrategy,
		MinParameterEntropy:            fosite.MinParameterEntropy,
		ClientSecretsHasher:            hasher,
	}
	provider, signer := customCompose(config, repo, privKey)

	return &IDPRouter{
		repo:        repo,
		privKey:     privKey,
		logger:      logger,
		externalURL: externalURL,
		hasher:      hasher,
		provider:    provider,
		signer:      signer,
		authRouter:  authRouter,
	}, nil
}

func customCompose(config *fosite.Config, storage any, key any) (fosite.OAuth2Provider, *jwt.DefaultSigner) {
	keyGetter := func(context.Context) (any, error) { return key, nil }
	signer := &jwt.DefaultSigner{GetPrivateKey: keyGetter}

	provider := compose.Compose(
		config,
		storage,
		&compose.CommonStrategy{
			CoreStrategy:               compose.NewOAuth2JWTStrategy(keyGetter, compose.NewOAuth2HMACStrategy(config), config),
			OpenIDConnectTokenStrategy: compose.NewOpenIDConnectStrategy(keyGetter, config),
			Signer:                     signer,
		},
		compose.OAuth2AuthorizeExplicitFactory,
		compose.OAuth2RefreshTokenGrantFactory,
		compose.OAuth2TokenIntrospectionFactory,
		compose.OAuth2PKCEFactory,
	)
	return provider, signer
}

const (
	AuthorizationEndpoint            = "/.idp/auth"
	AuthorizationReturnEndpoint      = "/.idp/auth/:ar_id"
	TokenEndpoint                    = "/.idp/token"
	IntrospectionEndpoint            = "/.idp/introspect"
	RegistrationEndpoint             = "/.idp/register"
	OauthAuthorizationServerEndpoint = "/.well-known/oauth-authorization-server"
	JWKSEndpoint                     = "/.well-known/jwks.json"
	sessionKeyAuthorizeRequestIDs    = "idp_authorize_request_ids"
	sessionKeyAuthorizeReplay        = "idp_authorize_replay"
)

// replayWindow bounds how long an already-answered authorization return stays
// replayable from the same session. Repeat GET/POSTs within the window (browser
// form resubmission, Claude.ai re-navigation) re-serve the same redirect with
// the same code instead of failing with an invalid-session error.
var replayWindow = 10 * time.Second

// replayEntry is a cached authorization response, keyed by authorize-request id
// in the browser session. See ADR-0002 for the accepted concurrency semantics.
type replayEntry struct {
	RedirectURL string    `json:"redirect_url"`
	ExpiresAt   time.Time `json:"expires_at"`
}

func (a *IDPRouter) SetupRoutes(router gin.IRouter) {
	router.GET(AuthorizationEndpoint, a.handleAuth)
	router.GET(AuthorizationReturnEndpoint, a.authRouter.RequireAuth(), a.handleAuthorizationReturnForm)
	router.POST(AuthorizationReturnEndpoint, a.authRouter.RequireAuth(), a.handleAuthorizationReturn)
	router.POST(TokenEndpoint, a.handleToken)
	router.POST(IntrospectionEndpoint, a.handleIntrospect)
	router.POST(RegistrationEndpoint, a.handleRegister)
	router.GET(OauthAuthorizationServerEndpoint, a.handleOauthAuthorizationServer)
	router.GET(JWKSEndpoint, a.handleJWKS)
}

func (a *IDPRouter) handleAuth(c *gin.Context) {
	ctx := c.Request.Context()

	// RFC 6749 makes state RECOMMENDED, not REQUIRED, but fosite enforces
	// minimum entropy (8 chars). Generate a server-side state for clients
	// that omit it (e.g., MCP Inspector, Cursor CLI) so they can complete
	// the OAuth flow. The generated state is echoed back in the redirect;
	// clients that didn't send state will simply ignore it.
	if c.Request.URL.Query().Get("state") == "" {
		state, err := utils.GenerateState()
		if err != nil {
			a.provider.WriteAuthorizeError(ctx, c.Writer, nil, fosite.ErrServerError.WithWrap(err))
			return
		}
		q := c.Request.URL.Query()
		q.Set("state", state)
		c.Request.URL.RawQuery = q.Encode()
	}

	ar, err := a.provider.NewAuthorizeRequest(ctx, c.Request)
	if err != nil {
		a.provider.WriteAuthorizeError(ctx, c.Writer, ar, err)
		return
	}

	if err := a.repo.CreateAuthorizeRequest(ctx, ar); err != nil {
		a.logger.Error("Failed to create authorize requester", zap.Error(err))
		a.provider.WriteAuthorizeError(ctx, c.Writer, ar, fosite.ErrServerError.WithWrap(err))
		return
	}
	session := sessions.Default(c)
	addAuthorizeRequestID(session, ar.GetID())
	if err := session.Save(); err != nil {
		a.logger.Error("Failed to save authorize request in session", zap.Error(err))
		_ = a.repo.DeleteAuthorizeRequest(ctx, ar.GetID())
		a.provider.WriteAuthorizeError(ctx, c.Writer, ar, fosite.ErrServerError.WithWrap(err))
		return
	}
	c.Redirect(302, strings.ReplaceAll(AuthorizationReturnEndpoint, ":ar_id", ar.GetID()))
}

func (a *IDPRouter) handleAuthorizationReturnForm(c *gin.Context) {
	ctx := c.Request.Context()
	arID := c.Param("ar_id")
	session := sessions.Default(c)
	if a.replayOrInvalid(c, session, arID) {
		return
	}

	ar, err := a.repo.GetAuthorizeRequest(ctx, arID)
	if err != nil {
		a.logger.Error("Failed to get authorize requester", zap.Error(err))
		a.writeInvalidAuthorizationSession(c)
		return
	}

	// Show the redirect URI host (mandated by the MCP spec's security
	// considerations) and the client name when one was registered, so the
	// owner can see who is requesting access.
	host := ""
	if redirectURI := ar.GetRedirectURI(); redirectURI != nil {
		host = redirectURI.Hostname()
	}
	name := ""
	if clientID := ar.GetClient().GetID(); clientID != "" {
		if clientName, err := a.repo.GetClientName(ctx, clientID); err == nil {
			name = clientName
		}
	}
	if name == "" {
		name = host
	}

	var buf bytes.Buffer
	if err := consentFormTmpl.Execute(&buf, consentFormData{Name: name, Host: host}); err != nil {
		a.logger.Error("Failed to render consent form", zap.Error(err))
		c.AbortWithStatusJSON(500, gin.H{"error": "Internal Server Error"})
		return
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", buf.Bytes())
}

type consentFormData struct {
	Name string
	Host string
}

var consentFormTmpl = template.Must(template.New("consent").Parse(consentFormTemplate))

const consentFormTemplate = `<!doctype html>
<html>
<head><meta charset="utf-8"><title>Authorize access</title></head>
<body>
<h1>Authorize access to your MCP server</h1>
<p>{{.Name}} is requesting access.</p>
<p>After authorizing, you'll be redirected to {{.Host}} with an authorization code.</p>
<form method="post">
  <button type="submit" name="decision" value="authorize">Authorize</button>
  <button type="submit" name="decision" value="deny">Deny</button>
</form>
</body>
</html>`

func (a *IDPRouter) handleAuthorizationReturn(c *gin.Context) {
	ctx := c.Request.Context()
	arID := c.Param("ar_id")
	session := sessions.Default(c)
	if a.replayOrInvalid(c, session, arID) {
		return
	}

	ar, err := a.repo.GetAuthorizeRequest(ctx, arID)
	if err != nil {
		// The session still lists this authorize request but its store record
		// is gone — consumed by a concurrent POST (ADR-0002) or expired. Re-serve
		// the cached redirect when the replay window has one, otherwise an HTML
		// error page instead of raw JSON.
		a.logger.Error("Failed to get authorize requester", zap.Error(err))
		if redirectURL, ok := getReplayEntry(session, arID); ok {
			c.Redirect(http.StatusSeeOther, redirectURL)
			return
		}
		a.writeInvalidAuthorizationSession(c)
		return
	}
	defer func() {
		if err := a.repo.DeleteAuthorizeRequest(ctx, arID); err != nil {
			a.logger.Error("Failed to delete authorize requester", zap.Error(err))
		}
	}()

	// Deny (RFC 6749 §4.1.2.1): redirect the user-agent back to the client's
	// redirect URI with error=access_denied instead of issuing a code.
	if c.PostForm("decision") == "deny" {
		removeAuthorizeRequestID(session, arID)
		if err := session.Save(); err != nil {
			a.logger.Error("Failed to remove authorize request from session", zap.Error(err))
		}
		a.provider.WriteAuthorizeError(ctx, c.Writer, ar, fosite.ErrAccessDenied)
		return
	}

	for _, scope := range ar.GetRequestedScopes() {
		ar.GrantScope(scope)
	}
	ar.GrantAudience(a.externalURL)

	subject := "user"
	if userID, ok := session.Get(auth.SessionKeyUserID).(string); ok && userID != "" {
		subject = userID
	}
	var userInfo map[string]any
	if userInfoJSON, ok := session.Get(auth.SessionKeyUserInfo).(string); ok && userInfoJSON != "" {
		json.Unmarshal([]byte(userInfoJSON), &userInfo)
	}

	jwtSession, err := NewJWTSessionWithKey(a.externalURL, subject, a.privKey, userInfo)
	if err != nil {
		a.logger.With(utils.Err(err)...).Error("Failed to create JWT session", zap.Error(err))
		a.provider.WriteAuthorizeError(ctx, c.Writer, ar, err)
		return
	}

	response, err := a.provider.NewAuthorizeResponse(ctx, ar, jwtSession)
	if err != nil {
		a.logger.With(utils.Err(err)...).Error("Failed to generate authorization response", zap.Error(err))
		a.provider.WriteAuthorizeError(ctx, c.Writer, ar, err)
		return
	}

	removeAuthorizeRequestID(session, arID)
	if err := session.Save(); err != nil {
		a.logger.Error("Failed to remove authorize request from session", zap.Error(err))
		a.provider.WriteAuthorizeError(ctx, c.Writer, ar, fosite.ErrServerError.WithWrap(err))
		return
	}

	a.provider.WriteAuthorizeResponse(ctx, c.Writer, ar, response)

	// Cache the issued redirect for the replay window so a repeat submission
	// from the same session re-serves the same code instead of 403ing.
	if redirectURL := c.Writer.Header().Get("Location"); redirectURL != "" {
		setReplayEntry(session, arID, replayEntry{
			RedirectURL: redirectURL,
			ExpiresAt:   time.Now().Add(replayWindow),
		})
		if err := session.Save(); err != nil {
			a.logger.Error("Failed to save authorize replay entry in session", zap.Error(err))
		}
	}
}

// replayOrInvalid reports whether the request must not proceed: either it is a
// repeat submission from the same session within the replay window (served the
// cached redirect with the same code — browser form-resubmission heuristic or
// Claude.ai re-navigation), or the authorize request is not bound to this
// session at all (an HTML error page). It writes the response and returns true
// when the caller should stop.
func (a *IDPRouter) replayOrInvalid(c *gin.Context, session sessions.Session, arID string) bool {
	if hasAuthorizeRequestID(session, arID) {
		return false
	}
	if redirectURL, ok := getReplayEntry(session, arID); ok {
		c.Redirect(http.StatusSeeOther, redirectURL)
		return true
	}
	a.writeInvalidAuthorizationSession(c)
	return true
}

func (a *IDPRouter) writeInvalidAuthorizationSession(c *gin.Context) {
	c.Data(http.StatusForbidden, "text/html; charset=utf-8", []byte(`<!doctype html><html><body><h1>Invalid authorization session</h1><p>This authorization request is invalid or has expired. Please start the OAuth flow again.</p></body></html>`))
}

func authorizeRequestIDs(session sessions.Session) []string {
	value, ok := session.Get(sessionKeyAuthorizeRequestIDs).(string)
	if !ok || value == "" {
		return nil
	}
	var ids []string
	if err := json.Unmarshal([]byte(value), &ids); err != nil {
		return nil
	}
	return ids
}

func addAuthorizeRequestID(session sessions.Session, arID string) {
	ids := authorizeRequestIDs(session)
	if hasAuthorizeRequestID(session, arID) {
		return
	}
	ids = append(ids, arID)
	data, _ := json.Marshal(ids)
	session.Set(sessionKeyAuthorizeRequestIDs, string(data))
}

func hasAuthorizeRequestID(session sessions.Session, arID string) bool {
	for _, id := range authorizeRequestIDs(session) {
		if id == arID {
			return true
		}
	}
	return false
}

func removeAuthorizeRequestID(session sessions.Session, arID string) {
	ids := authorizeRequestIDs(session)
	remaining := ids[:0]
	for _, id := range ids {
		if id != arID {
			remaining = append(remaining, id)
		}
	}
	if len(remaining) == 0 {
		session.Delete(sessionKeyAuthorizeRequestIDs)
		return
	}
	data, _ := json.Marshal(remaining)
	session.Set(sessionKeyAuthorizeRequestIDs, string(data))
}

func replayEntries(session sessions.Session) map[string]replayEntry {
	value, ok := session.Get(sessionKeyAuthorizeReplay).(string)
	if !ok || value == "" {
		return nil
	}
	var entries map[string]replayEntry
	if err := json.Unmarshal([]byte(value), &entries); err != nil {
		return nil
	}
	return entries
}

func setReplayEntry(session sessions.Session, arID string, entry replayEntry) {
	entries := replayEntries(session)
	if entries == nil {
		entries = make(map[string]replayEntry)
	}
	// Drop expired entries so the session cookie doesn't accumulate stale
	// redirects across many OAuth flows.
	now := time.Now()
	for id, e := range entries {
		if id != arID && now.After(e.ExpiresAt) {
			delete(entries, id)
		}
	}
	entries[arID] = entry
	data, _ := json.Marshal(entries)
	session.Set(sessionKeyAuthorizeReplay, string(data))
}

// getReplayEntry returns the cached redirect URL for an already-answered
// authorize request if one exists for this session and is still within the
// replay window.
func getReplayEntry(session sessions.Session, arID string) (string, bool) {
	entry, ok := replayEntries(session)[arID]
	if !ok {
		return "", false
	}
	if time.Now().After(entry.ExpiresAt) {
		return "", false
	}
	return entry.RedirectURL, true
}

func (a *IDPRouter) handleToken(c *gin.Context) {
	ctx := c.Request.Context()

	session, err := NewJWTSessionWithKey("", "", a.privKey, nil)
	if err != nil {
		a.logger.With(utils.Err(err)...).Error("Failed to create JWT session for token", zap.Error(err))
		a.provider.WriteAccessError(ctx, c.Writer, nil, fosite.ErrServerError.WithWrap(err))
		return
	}

	accessRequest, err := a.provider.NewAccessRequest(ctx, c.Request, session)
	if err != nil {
		a.logger.With(utils.Err(err)...).Error("Failed to create access request", zap.String("grant_type", c.PostForm("grant_type")))
		a.provider.WriteAccessError(ctx, c.Writer, accessRequest, err)
		return
	}

	response, err := a.provider.NewAccessResponse(ctx, accessRequest)
	if err != nil {
		a.logger.With(utils.Err(err)...).Error("Failed to create access response", zap.String("grant_type", c.PostForm("grant_type")), zap.Error(err))
		a.provider.WriteAccessError(ctx, c.Writer, accessRequest, err)
		return
	}

	a.provider.WriteAccessResponse(ctx, c.Writer, accessRequest, response)
}

func (a *IDPRouter) handleIntrospect(c *gin.Context) {
	ctx := c.Request.Context()
	session, err := NewJWTSessionWithKey("", "", a.privKey, nil)
	if err != nil {
		a.provider.WriteIntrospectionError(ctx, c.Writer, fosite.ErrServerError.WithWrap(err))
		return
	}

	ir, err := a.provider.NewIntrospectionRequest(ctx, c.Request, session)
	if err != nil {
		a.provider.WriteIntrospectionError(ctx, c.Writer, err)
		return
	}

	a.provider.WriteIntrospectionResponse(ctx, c.Writer, ir)
}

type registrationRequest struct {
	ClientName              string   `json:"client_name"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	Scope                   string   `json:"scope"`
	RedirectURIs            []string `json:"redirect_uris"`
}

type registrationResponse struct {
	ClientID                string   `json:"client_id"`
	ClientSecret            string   `json:"client_secret,omitempty"`
	RedirectURIs            []string `json:"redirect_uris"`
	ClientName              string   `json:"client_name"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	RegistrationClientURI   string   `json:"registration_client_uri"`
	ClientIDIssuedAt        int64    `json:"client_id_issued_at"`
}

func (a *IDPRouter) handleRegister(c *gin.Context) {
	ctx := c.Request.Context()

	var req registrationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "invalid_request", "error_description": err.Error()})
		return
	}

	clientID, err := utils.GenerateClientID()
	if err != nil {
		a.logger.Error("Failed to generate client ID", zap.Error(err))
		c.JSON(500, gin.H{"error": "server_error", "error_description": err.Error()})
		return
	}

	var clientSecret string
	var hashedSecret []byte
	isPublic := req.TokenEndpointAuthMethod == "none"

	if !isPublic {
		// Generate client secret for confidential clients
		clientSecret, err = utils.GenerateClientSecret()
		if err != nil {
			a.logger.Error("Failed to generate client secret", zap.Error(err))
			c.JSON(500, gin.H{"error": "server_error", "error_description": err.Error()})
			return
		}

		hashedSecret, err = a.hasher.Hash(ctx, []byte(clientSecret))
		if err != nil {
			a.logger.Error("Failed to hash client secret", zap.Error(err))
			c.JSON(500, gin.H{"error": "server_error", "error_description": err.Error()})
			return
		}
	}

	client := &fosite.DefaultClient{
		ID:            clientID,
		Secret:        hashedSecret,
		RedirectURIs:  req.RedirectURIs,
		GrantTypes:    req.GrantTypes,
		ResponseTypes: req.ResponseTypes,
		Scopes:        strings.Fields(req.Scope),
		Audience:      []string{a.externalURL},
		Public:        isPublic,
	}
	if err := a.repo.RegisterClient(ctx, client, req.ClientName); err != nil {
		a.logger.Error("Failed to register client", zap.String("client_id", clientID), zap.Error(err))
		c.JSON(500, gin.H{"error": "server_error", "error_description": err.Error()})
		return
	}

	registrationClientURI, err := url.JoinPath(RegistrationEndpoint, clientID)
	if err != nil {
		a.logger.Error("Failed to create registration client URI", zap.String("client_id", clientID), zap.Error(err))
		c.JSON(500, gin.H{"error": "server_error", "error_description": err.Error()})
		return
	}

	response := registrationResponse{
		ClientID:                clientID,
		RedirectURIs:            req.RedirectURIs,
		ClientName:              req.ClientName,
		GrantTypes:              req.GrantTypes,
		ResponseTypes:           req.ResponseTypes,
		TokenEndpointAuthMethod: req.TokenEndpointAuthMethod,
		RegistrationClientURI:   registrationClientURI,
		ClientIDIssuedAt:        time.Now().Unix(),
	}

	if !isPublic {
		response.ClientSecret = clientSecret
	}

	c.JSON(201, response)
}

type authorizationServerResponse struct {
	Issuer                            string   `json:"issuer"`
	AuthorizationEndpoint             string   `json:"authorization_endpoint"`
	TokenEndpoint                     string   `json:"token_endpoint"`
	RegistrationEndpoint              string   `json:"registration_endpoint"`
	ScopesSupported                   []string `json:"scopes_supported"`
	ResponseTypesSupported            []string `json:"response_types_supported"`
	ResponseModesSupported            []string `json:"response_modes_supported"`
	GrantTypesSupported               []string `json:"grant_types_supported"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported"`
	CodeChallengeMethodsSupported     []string `json:"code_challenge_methods_supported"`
}

func (a *IDPRouter) handleOauthAuthorizationServer(c *gin.Context) {
	authorizationEndpoint, err := url.JoinPath(a.externalURL, AuthorizationEndpoint)
	if err != nil {
		a.logger.Error("Failed to create authorization endpoint URL", zap.Error(err))
		c.JSON(500, gin.H{"error": "server_error", "error_description": err.Error()})
		return
	}
	tokenEndpoint, err := url.JoinPath(a.externalURL, TokenEndpoint)
	if err != nil {
		a.logger.Error("Failed to create token endpoint URL", zap.Error(err))
		c.JSON(500, gin.H{"error": "server_error", "error_description": err.Error()})
		return
	}
	registrationEndpoint, err := url.JoinPath(a.externalURL, RegistrationEndpoint)
	if err != nil {
		a.logger.Error("Failed to create registration endpoint URL", zap.Error(err))
		c.JSON(500, gin.H{"error": "server_error", "error_description": err.Error()})
		return
	}

	res := &authorizationServerResponse{
		Issuer:                            a.externalURL,
		AuthorizationEndpoint:             authorizationEndpoint,
		TokenEndpoint:                     tokenEndpoint,
		RegistrationEndpoint:              registrationEndpoint,
		ScopesSupported:                   []string{},
		ResponseTypesSupported:            []string{"code"},
		ResponseModesSupported:            []string{"query"},
		GrantTypesSupported:               []string{"authorization_code", "refresh_token"},
		TokenEndpointAuthMethodsSupported: []string{"client_secret_basic", "client_secret_post", "none"},
		CodeChallengeMethodsSupported:     []string{"S256"},
	}
	c.JSON(200, res)
}

type jwk struct {
	Kty string `json:"kty"`
	Use string `json:"use"`
	Kid string `json:"kid"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
}

type jwks struct {
	Keys []jwk `json:"keys"`
}

func (a *IDPRouter) handleJWKS(c *gin.Context) {
	publicKey := &a.privKey.PublicKey

	// Convert RSA public key components to base64url
	nBytes := publicKey.N.Bytes()
	eBytes := big.NewInt(int64(publicKey.E)).Bytes()

	n := base64.RawURLEncoding.EncodeToString(nBytes)
	e := base64.RawURLEncoding.EncodeToString(eBytes)

	keyID, err := utils.GenerateKeyID(&a.privKey.PublicKey)
	if err != nil {
		a.logger.Error("Failed to generate key ID for JWKS", zap.Error(err))
		c.JSON(500, gin.H{"error": "failed to generate key ID"})
		return
	}

	k := jwk{
		Kty: "RSA",
		Use: "sig",
		Kid: keyID,
		Alg: "RS256",
		N:   n,
		E:   e,
	}

	ks := jwks{Keys: []jwk{k}}
	c.JSON(200, ks)
}

func NewJWTSessionWithKey(iss string, subject string, privateKey *rsa.PrivateKey, userInfo map[string]any) (*Session, error) {
	keyID, err := utils.GenerateKeyID(&privateKey.PublicKey)
	if err != nil {
		return nil, err
	}
	var extra map[string]any
	if userInfo != nil {
		extra = map[string]any{"userinfo": userInfo}
	}
	return &Session{
		DefaultSession: &fosite.DefaultSession{
			Username: subject,
			Subject:  subject,
		},
		JWTClaims: &jwt.JWTClaims{
			Issuer:    iss,
			Subject:   subject,
			Audience:  []string{},
			ExpiresAt: time.Now().Add(time.Hour),
			IssuedAt:  time.Now(),
			NotBefore: time.Now(),
			Extra:     extra,
		},
		JWTHeader: &jwt.Headers{
			Extra: map[string]any{
				"kid": keyID,
			},
		},
	}, nil
}

type Session struct {
	*fosite.DefaultSession
	JWTClaims *jwt.JWTClaims
	JWTHeader *jwt.Headers
}

func (s *Session) GetJWTClaims() jwt.JWTClaimsContainer {
	return s.JWTClaims
}

func (s *Session) GetJWTHeader() *jwt.Headers {
	return s.JWTHeader
}

func (s *Session) Clone() fosite.Session {
	if s == nil {
		return nil
	}

	clone := &Session{
		DefaultSession: &fosite.DefaultSession{
			Username:  s.DefaultSession.Username,
			Subject:   s.DefaultSession.Subject,
			ExpiresAt: s.DefaultSession.ExpiresAt,
		},
		JWTClaims: &jwt.JWTClaims{
			Issuer:    s.JWTClaims.Issuer,
			Subject:   s.JWTClaims.Subject,
			Audience:  s.JWTClaims.Audience,
			ExpiresAt: s.JWTClaims.ExpiresAt,
			IssuedAt:  s.JWTClaims.IssuedAt,
			NotBefore: s.JWTClaims.NotBefore,
			Extra:     s.JWTClaims.Extra,
		},
		JWTHeader: &jwt.Headers{
			Extra: make(map[string]any),
		},
	}

	for k, v := range s.JWTHeader.Extra {
		clone.JWTHeader.Extra[k] = v
	}

	return clone
}
