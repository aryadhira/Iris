// Package auth implements Iris' single-owner passphrase gate: one passphrase,
// one HttpOnly cookie, no user table and no public sign-up.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"

	"github.com/aryadhira/iris/internal/store"
)

// CookieName is the only browser credential Iris issues.
const CookieName = "iris_session"

// SessionTTL is the lifetime of a login.
const SessionTTL = 30 * 24 * time.Hour

// wrongPassphraseDelay blunts passphrase guessing.
const wrongPassphraseDelay = 250 * time.Millisecond

// SessionStore is the subset of the sqlc-generated queries auth needs.
type SessionStore interface {
	CreateAuthSession(ctx context.Context, arg store.CreateAuthSessionParams) error
	GetAuthSession(ctx context.Context, tokenHash string) (store.AuthSession, error)
	DeleteAuthSession(ctx context.Context, tokenHash string) error
}

// Service owns login, logout and the authentication middleware.
type Service struct {
	store        SessionStore
	passHash     [sha256.Size]byte
	cookieSecure bool
	logger       *slog.Logger
}

// New hashes the owner passphrase once at boot. The plaintext is never stored.
func New(st SessionStore, passphrase string, cookieSecure bool, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		store:        st,
		passHash:     sha256.Sum256([]byte(passphrase)),
		cookieSecure: cookieSecure,
		logger:       logger,
	}
}

type loginRequest struct {
	Passphrase string `json:"passphrase"`
}

// Login checks the passphrase and, on success, issues a 32-byte session token
// in an HttpOnly cookie. Only the token's SHA-256 is stored in Postgres.
func (s *Service) Login(c echo.Context) error {
	var req loginRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, errorBody("Send your passphrase to sign in."))
	}

	submitted := sha256.Sum256([]byte(req.Passphrase))
	if subtle.ConstantTimeCompare(submitted[:], s.passHash[:]) != 1 {
		time.Sleep(wrongPassphraseDelay)
		return c.JSON(http.StatusUnauthorized, errorBody("That passphrase is not correct."))
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		s.logger.Error("cannot mint session token", "error", err)
		return c.JSON(http.StatusInternalServerError, errorBody("Iris could not sign you in. Try again."))
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	expires := time.Now().Add(SessionTTL)

	err := s.store.CreateAuthSession(c.Request().Context(), store.CreateAuthSessionParams{
		TokenHash: hashToken(token),
		ExpiresAt: expires,
		UserAgent: truncate(c.Request().UserAgent(), 512),
	})
	if err != nil {
		s.logger.Error("cannot store auth session", "error", err)
		return c.JSON(http.StatusInternalServerError, errorBody("Iris could not sign you in. Try again."))
	}

	c.SetCookie(&http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.cookieSecure,
		SameSite: http.SameSiteLaxMode,
		Expires:  expires,
		MaxAge:   int(SessionTTL / time.Second),
	})
	return c.JSON(http.StatusOK, map[string]bool{"authenticated": true})
}

// Me answers whether the current cookie is valid (the middleware already
// enforced it).
func (s *Service) Me(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]bool{"authenticated": true})
}

// Logout deletes the session row and clears the cookie.
func (s *Service) Logout(c echo.Context) error {
	if cookie, err := c.Cookie(CookieName); err == nil && cookie.Value != "" {
		if err := s.store.DeleteAuthSession(c.Request().Context(), hashToken(cookie.Value)); err != nil {
			s.logger.Error("cannot delete auth session", "error", err)
			return c.JSON(http.StatusInternalServerError, errorBody("Iris could not sign you out. Try again."))
		}
	}
	c.SetCookie(&http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   s.cookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
	return c.JSON(http.StatusOK, map[string]bool{"authenticated": false})
}

// Middleware requires a valid cookie on every /api route except login and
// health. The SPA itself stays public: login is a browser route and its bundle
// cannot carry a cookie before the owner has signed in.
func (s *Service) Middleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		requestPath := c.Request().URL.Path
		if !strings.HasPrefix(requestPath, "/api/") && requestPath != "/api" {
			return next(c)
		}
		switch requestPath {
		case "/api/auth/login", "/api/health":
			return next(c)
		}

		cookie, err := c.Cookie(CookieName)
		if err != nil || cookie.Value == "" {
			return c.JSON(http.StatusUnauthorized, errorBody("Sign in to Iris first."))
		}

		_, err = s.store.GetAuthSession(c.Request().Context(), hashToken(cookie.Value))
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				s.logger.Error("cannot verify auth session", "error", err)
				return c.JSON(http.StatusInternalServerError, errorBody("Iris cannot check your session right now."))
			}
			return c.JSON(http.StatusUnauthorized, errorBody("Your Iris session has ended. Sign in again."))
		}

		return next(c)
	}
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func errorBody(message string) map[string]string {
	return map[string]string{"error": message}
}

func truncate(v string, max int) string {
	if len(v) <= max {
		return v
	}
	return strings.ToValidUTF8(v[:max], "")
}
