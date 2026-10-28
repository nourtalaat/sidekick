package auth

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/oauth2"

	"github.com/nourtalaat/sidekick/internal/audit"
	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/ids"
	"github.com/nourtalaat/sidekick/internal/problem"
	"github.com/nourtalaat/sidekick/internal/roles"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// The audit actions of logins.
const (
	ActionUserCreated     = "user.created"
	ActionUserRoleChanged = "user.role_changed"
)

// errorCodePattern is an OAuth error code, such as access_denied, which
// the callback repeats to the user. Anything else is not shown.
var errorCodePattern = regexp.MustCompile(`^[a-z_]{1,64}$`)

var (
	// errLoginRace means another login of the same new user committed
	// first. Signing in again works.
	errLoginRace = errors.New("another login of the same user ran at the same time")
	// errBadGroups means the groups claim is neither a list of strings nor
	// one string.
	errBadGroups = errors.New("the groups claim is not a list of strings")
)

// identity is what an ID token says about a user.
type identity struct {
	Issuer  string
	Subject string
	// Email is empty unless the provider sent one it did not mark
	// unverified.
	Email  string
	Name   string
	Groups []string
}

// Login starts the authorization code flow with PKCE: it keeps a new
// state, nonce and code verifier in a sealed cookie, and sends the browser
// to the provider. The return query parameter, a path on this site, is
// where the browser goes after the login.
func (s *Service) Login(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	p, err := s.oidcProvider(ctx)
	if err != nil {
		s.log.ErrorContext(ctx, "could not reach the OIDC provider", "error", err)
		problem.Write(w, r, http.StatusBadGateway, "The identity provider cannot be reached. Try again later.")

		return
	}

	secret, key, err := s.loginSecrets(ctx)
	if err != nil {
		s.log.ErrorContext(ctx, "could not start a login", "error", err)
		problem.Write(w, r, http.StatusInternalServerError, problem.InternalDetail)

		return
	}

	st := loginState{
		State:    randomToken(tokenBytes),
		Nonce:    randomToken(tokenBytes),
		Verifier: oauth2.GenerateVerifier(),
		Return:   returnPath(r.URL.Query().Get("return")),
		Expires:  s.now().Add(loginLifetime),
	}

	value, err := seal(key, st)
	if err != nil {
		s.log.ErrorContext(ctx, "could not start a login", "error", err)
		problem.Write(w, r, http.StatusInternalServerError, problem.InternalDetail)

		return
	}

	http.SetCookie(w, &http.Cookie{
		Name: loginCookie, Value: value, Path: "/", MaxAge: int(loginLifetime.Seconds()),
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})

	to := s.oauth(p, secret).AuthCodeURL(st.State, oidc.Nonce(st.Nonce), oauth2.S256ChallengeOption(st.Verifier))
	http.Redirect(w, r, to, http.StatusFound)
}

// Callback ends the login the provider sends back: it checks the state,
// trades the code for tokens with the code verifier, checks the ID token
// and its nonce, maps the user's groups to a role, stores the user and
// starts a session.
func (s *Service) Callback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	clearCookie(w, loginCookie)

	q := r.URL.Query()
	if code := q.Get("error"); code != "" {
		detail := "The identity provider did not sign you in."
		if errorCodePattern.MatchString(code) {
			detail = fmt.Sprintf("The identity provider did not sign you in: %s.", code)
		}

		problem.Write(w, r, http.StatusUnauthorized, detail)

		return
	}

	p, err := s.oidcProvider(ctx)
	if err != nil {
		s.log.ErrorContext(ctx, "could not reach the OIDC provider", "error", err)
		problem.Write(w, r, http.StatusBadGateway, "The identity provider cannot be reached. Try again later.")

		return
	}

	secret, key, err := s.loginSecrets(ctx)
	if err != nil {
		s.log.ErrorContext(ctx, "could not end a login", "error", err)
		problem.Write(w, r, http.StatusInternalServerError, problem.InternalDetail)

		return
	}

	st, ok := s.loginStateOf(r, key)
	if !ok || subtle.ConstantTimeCompare([]byte(st.State), []byte(q.Get("state"))) != 1 {
		problem.Write(w, r, http.StatusBadRequest, "This login expired or started in another browser. Sign in again.")

		return
	}

	id, status, detail := s.verify(ctx, p, secret, st, q.Get("code"))
	if status != 0 {
		problem.Write(w, r, status, detail)

		return
	}

	role := s.cfg.RoleOf(id.Groups)
	if role == 0 {
		s.log.InfoContext(ctx, "refused a login: no group gives a role", "subject", id.Subject)
		problem.Write(w, r, http.StatusForbidden, "None of your groups gives you a Sidekick role. Ask an admin to add one to oidc.roles.")

		return
	}

	sessionID, err := s.saveLogin(ctx, id, role)
	if err != nil {
		s.log.ErrorContext(ctx, "could not store a login", "subject", id.Subject, "error", err)

		status, detail := http.StatusInternalServerError, problem.InternalDetail
		if errors.Is(err, errLoginRace) {
			status, detail = http.StatusConflict, "Another sign-in of yours ran at the same time. Sign in again."
		}

		problem.Write(w, r, status, detail)

		return
	}

	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: sessionID, Path: "/", MaxAge: int(SessionLifetime.Seconds()),
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, st.Return, http.StatusSeeOther)
}

// loginSecrets returns the client secret and the key that seals login
// state.
func (s *Service) loginSecrets(ctx context.Context) (secret string, key []byte, err error) {
	secret, err = s.clientSecret(ctx)
	if err != nil {
		return "", nil, fmt.Errorf("read the client secret: %w", err)
	}

	key, err = loginKey(secret)
	if err != nil {
		return "", nil, fmt.Errorf("make the login state key: %w", err)
	}

	return secret, key, nil
}

// loginStateOf opens the login cookie of r.
func (s *Service) loginStateOf(r *http.Request, key []byte) (loginState, bool) {
	c, err := r.Cookie(loginCookie)
	if err != nil {
		return loginState{}, false
	}

	st, err := open(key, c.Value, s.now())

	return st, err == nil
}

// verify trades code for tokens and checks the ID token: its signature,
// issuer, audience and expiry, and that its nonce is the login's. On a
// problem it returns the status and detail to answer with.
func (s *Service) verify(ctx context.Context, p *oidc.Provider, secret string, st loginState, code string) (identity, int, string) {
	token, err := s.oauth(p, secret).Exchange(oidc.ClientContext(ctx, s.http), code, oauth2.VerifierOption(st.Verifier))
	if err != nil {
		s.log.WarnContext(ctx, "the OIDC provider refused a login code", "error", exchangeError(err))

		return identity{}, http.StatusUnauthorized, "The identity provider did not accept the login. Sign in again."
	}

	raw, _ := token.Extra("id_token").(string)
	if raw == "" {
		s.log.WarnContext(ctx, "the OIDC provider sent no ID token")

		return identity{}, http.StatusBadGateway, "The identity provider sent no ID token."
	}

	idToken, err := p.Verifier(&oidc.Config{ClientID: s.cfg.ClientID, Now: s.now}).Verify(ctx, raw)
	if err != nil {
		s.log.WarnContext(ctx, "refused an ID token", "error", err)

		return identity{}, http.StatusUnauthorized, "The ID token is not valid. Sign in again."
	}

	if subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(st.Nonce)) != 1 {
		s.log.WarnContext(ctx, "refused an ID token: its nonce is not the login's", "subject", idToken.Subject)

		return identity{}, http.StatusUnauthorized, "The ID token is not for this login. Sign in again."
	}

	id, err := identityOf(idToken, s.cfg.GroupsClaim)
	if err != nil {
		s.log.WarnContext(ctx, "refused an ID token", "subject", idToken.Subject, "error", err)

		return identity{}, http.StatusUnauthorized, "The ID token's claims are not valid."
	}

	return id, 0, ""
}

// saveLogin stores the user and starts their session, in one transaction,
// and returns the session ID for the cookie. A first login and a changed
// role are written to the audit log.
func (s *Service) saveLogin(ctx context.Context, id identity, role roles.Role) (string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin: %w", err)
	}

	defer func() { _ = tx.Rollback(ctx) }()

	user, err := s.saveUser(ctx, tx, id, role)
	if err != nil {
		return "", fmt.Errorf("save the user: %w", err)
	}

	q := db.New(tx)
	now := s.now()

	if _, err := q.DeleteExpiredSessions(ctx, now); err != nil {
		return "", fmt.Errorf("delete expired sessions: %w", err)
	}

	sessionID, raw := newSessionID()

	err = q.CreateSession(ctx, db.CreateSessionParams{
		IDHash: hashSessionID(raw), UserID: user.ID, CsrfToken: randomToken(tokenBytes),
		CreatedAt: now, ExpiresAt: now.Add(SessionLifetime),
	})
	if err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit: %w", err)
	}

	s.log.InfoContext(ctx, "a user signed in", "user", user.ID, "role", user.Role)

	return sessionID, nil
}

// saveUser adds the user at their first login or updates them, through tx.
// When the email is another user's, it moves to this one, since the
// provider says it is theirs now.
func (s *Service) saveUser(ctx context.Context, tx pgx.Tx, id identity, role roles.Role) (db.User, error) {
	q := db.New(tx)
	now := s.now()
	name := string(config.RoleNameOf(role))

	old, err := q.LockUserBySubject(ctx, db.LockUserBySubjectParams{OidcIssuer: id.Issuer, OidcSubject: id.Subject})

	found := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return db.User{}, fmt.Errorf("read the user: %w", err)
	}

	userID := old.ID
	if !found {
		userID = ids.New()
	}

	if err := s.releaseEmail(ctx, q, id, userID); err != nil {
		return db.User{}, fmt.Errorf("move the email: %w", err)
	}

	if found {
		user, err := q.UpdateUserLogin(ctx, db.UpdateUserLoginParams{
			ID: userID, Email: optional(id.Email), Name: id.Name, Groups: id.Groups, Role: name, Now: now,
		})
		if err != nil {
			return db.User{}, fmt.Errorf("update the user: %w", err)
		}

		if old.Role != name {
			err := s.audit.Write(ctx, tx, audit.Entry{
				ActorKind: audit.ActorUser, ActorID: userID.String(), Action: ActionUserRoleChanged,
				TargetKind: "user", TargetID: userID.String(), Old: old.Role, New: name,
			})
			if err != nil {
				return db.User{}, fmt.Errorf("audit the role change: %w", err)
			}
		}

		return user, nil
	}

	user, err := q.InsertUser(ctx, db.InsertUserParams{
		ID: userID, OidcIssuer: id.Issuer, OidcSubject: id.Subject, Email: optional(id.Email),
		Name: id.Name, Groups: id.Groups, Role: name, Now: now,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.User{}, errLoginRace
	}

	if err != nil {
		return db.User{}, fmt.Errorf("add the user: %w", err)
	}

	err = s.audit.Write(ctx, tx, audit.Entry{
		ActorKind: audit.ActorUser, ActorID: userID.String(), Action: ActionUserCreated,
		TargetKind: "user", TargetID: userID.String(), New: name,
	})
	if err != nil {
		return db.User{}, fmt.Errorf("audit the new user: %w", err)
	}

	return user, nil
}

// releaseEmail clears the user's email from every other user, and logs
// each one it moves.
func (s *Service) releaseEmail(ctx context.Context, q *db.Queries, id identity, userID uuid.UUID) error {
	if id.Email == "" {
		return nil
	}

	moved, err := q.ReleaseUserEmail(ctx, db.ReleaseUserEmailParams{Email: id.Email, OidcIssuer: id.Issuer, OidcSubject: id.Subject})
	if err != nil {
		return fmt.Errorf("release the email from other users: %w", err)
	}

	for _, from := range moved {
		s.log.WarnContext(ctx, "an email moved to a newer login; the older user no longer matches Slack users by email", "from_user", from, "to_user", userID)
	}

	return nil
}

// identityOf reads the user from the ID token's claims. The groups are in
// the claim groupsClaim names, as a list of strings or one string. An
// email marked email_verified false is dropped; a token without
// email_verified counts as verified, since some providers never send it.
func identityOf(t *oidc.IDToken, groupsClaim string) (identity, error) {
	var claims map[string]json.RawMessage
	if err := t.Claims(&claims); err != nil {
		return identity{}, fmt.Errorf("decode the claims: %w", err)
	}

	id := identity{Issuer: t.Issuer, Subject: t.Subject}

	email := stringClaim(claims, "email")
	if verified, ok := claims["email_verified"]; !ok || !isFalse(verified) {
		id.Email = email
	}

	id.Name = firstNonEmpty(stringClaim(claims, "name"), stringClaim(claims, "preferred_username"), email, t.Subject)

	groups, err := groupsOf(claims[groupsClaim])
	if err != nil {
		return identity{}, fmt.Errorf("read the %s claim: %w", groupsClaim, err)
	}

	id.Groups = groups

	return id, nil
}

// groupsOf decodes a groups claim: missing or null, a list of strings, or
// one string. It never returns nil.
func groupsOf(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return []string{}, nil
	}

	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		return list, nil
	}

	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		return []string{one}, nil
	}

	return nil, errBadGroups
}

// stringClaim returns the claim name when it is a string, else "".
func stringClaim(claims map[string]json.RawMessage, name string) string {
	var s string
	_ = json.Unmarshal(claims[name], &s)

	return strings.TrimSpace(s)
}

// isFalse reports whether a claim is false, or the string "false" that
// some providers send. Null counts as missing, not false.
func isFalse(raw json.RawMessage) bool {
	if string(raw) == "null" {
		return false
	}

	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return !b
	}

	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.EqualFold(s, "false")
	}

	return false
}

// returnPath returns p when it is a path on this site, and "/" otherwise,
// so a login link cannot send the browser to another site.
func returnPath(p string) string {
	u, err := url.Parse(p)
	if err != nil || u.Scheme != "" || u.Host != "" || !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.Contains(p, `\`) {
		return "/"
	}

	return p
}

// exchangeError describes a failed code exchange without the provider's
// response body.
func exchangeError(err error) string {
	if re, ok := errors.AsType[*oauth2.RetrieveError](err); ok {
		code := re.ErrorCode
		if !errorCodePattern.MatchString(code) {
			code = "unknown"
		}

		status := 0
		if re.Response != nil {
			status = re.Response.StatusCode
		}

		return fmt.Sprintf("the token endpoint answered %d with error %s", status, code)
	}

	return err.Error()
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}

	return ""
}

// optional turns an empty string into SQL NULL.
func optional(s string) *string {
	if s == "" {
		return nil
	}

	return &s
}
