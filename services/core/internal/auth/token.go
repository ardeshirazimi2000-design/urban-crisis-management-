package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims is the minimal identity extracted from a verified access token.
// Authorization data (roles, scopes) is NOT taken from the token; it is resolved from the database.
type Claims struct {
	Subject string
	Name    string
}

type Verifier interface {
	Verify(ctx context.Context, raw string) (Claims, error)
}

var ErrInvalidToken = errors.New("invalid token")

type tokenClaims struct {
	jwt.RegisteredClaims
	Name              string `json:"name"`
	PreferredUsername string `json:"preferred_username"`
}

func (c tokenClaims) toClaims() (Claims, error) {
	if c.Subject == "" {
		return Claims{}, ErrInvalidToken
	}
	name := c.Name
	if name == "" {
		name = c.PreferredUsername
	}
	return Claims{Subject: c.Subject, Name: name}, nil
}

// ---------------------------------------------------------------------------
// Local development verifier (HS256). Refused outside APP_ENV=local|test by config validation.
// ---------------------------------------------------------------------------

const DevIssuer = "urban-crisis-dev"

type DevVerifier struct {
	Secret   []byte
	Audience string
}

func (v DevVerifier) Verify(_ context.Context, raw string) (Claims, error) {
	var tc tokenClaims
	_, err := jwt.ParseWithClaims(raw, &tc, func(t *jwt.Token) (any, error) { return v.Secret, nil },
		jwt.WithValidMethods([]string{"HS256"}), jwt.WithIssuer(DevIssuer), jwt.WithAudience(v.Audience),
		jwt.WithExpirationRequired(), jwt.WithLeeway(30*time.Second))
	if err != nil {
		return Claims{}, ErrInvalidToken
	}
	return tc.toClaims()
}

// IssueDevToken mints a short-lived local token. Only reachable via the local-only dev endpoint and tests.
func IssueDevToken(secret []byte, audience, subject, name string, ttl time.Duration) (string, error) {
	now := time.Now()
	tc := tokenClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: DevIssuer, Subject: subject, Audience: jwt.ClaimStrings{audience},
			IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
		Name: name,
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, tc).SignedString(secret)
}

// ---------------------------------------------------------------------------
// OIDC verifier: validates iss, aud, exp and signature against the issuer's JWKS (RS256/ES256),
// refreshing keys on unknown kid (key rotation) with a minimum refresh interval.
// ---------------------------------------------------------------------------

type OIDCVerifier struct {
	Issuer   string
	Audience string
	JWKSURL  string
	Client   *http.Client

	mu          sync.RWMutex
	keys        map[string]any
	lastRefresh time.Time
}

func NewOIDCVerifier(ctx context.Context, issuer, audience, jwksURL string) (*OIDCVerifier, error) {
	v := &OIDCVerifier{Issuer: strings.TrimSuffix(issuer, "/"), Audience: audience, JWKSURL: jwksURL,
		Client: &http.Client{Timeout: 5 * time.Second}}
	if v.JWKSURL == "" {
		u, err := v.discover(ctx)
		if err != nil {
			return nil, err
		}
		v.JWKSURL = u
	}
	if err := v.refresh(ctx); err != nil {
		return nil, err
	}
	return v, nil
}

func (v *OIDCVerifier) discover(ctx context.Context) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, v.Issuer+"/.well-known/openid-configuration", nil)
	resp, err := v.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("oidc discovery: %w", err)
	}
	defer resp.Body.Close()
	var doc struct {
		JWKSURI string `json:"jwks_uri"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil || doc.JWKSURI == "" {
		return "", fmt.Errorf("oidc discovery: missing jwks_uri")
	}
	return doc.JWKSURI, nil
}

type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	N   string `json:"n"`
	E   string `json:"e"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

func (v *OIDCVerifier) refresh(ctx context.Context) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, v.JWKSURL, nil)
	resp, err := v.Client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch jwks: %w", err)
	}
	defer resp.Body.Close()
	var set struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&set); err != nil {
		return fmt.Errorf("decode jwks: %w", err)
	}
	keys := map[string]any{}
	for _, k := range set.Keys {
		if k.Use != "" && k.Use != "sig" {
			continue
		}
		switch k.Kty {
		case "RSA":
			n, err1 := base64.RawURLEncoding.DecodeString(k.N)
			e, err2 := base64.RawURLEncoding.DecodeString(k.E)
			if err1 != nil || err2 != nil {
				continue
			}
			keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
		case "EC":
			if k.Crv != "P-256" {
				continue
			}
			x, err1 := base64.RawURLEncoding.DecodeString(k.X)
			y, err2 := base64.RawURLEncoding.DecodeString(k.Y)
			if err1 != nil || err2 != nil {
				continue
			}
			keys[k.Kid] = &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
		}
	}
	v.mu.Lock()
	v.keys = keys
	v.lastRefresh = time.Now()
	v.mu.Unlock()
	return nil
}

func (v *OIDCVerifier) key(ctx context.Context, kid string) (any, error) {
	v.mu.RLock()
	k, ok := v.keys[kid]
	stale := time.Since(v.lastRefresh) > time.Minute
	v.mu.RUnlock()
	if ok {
		return k, nil
	}
	if stale {
		if err := v.refresh(ctx); err == nil {
			v.mu.RLock()
			k, ok = v.keys[kid]
			v.mu.RUnlock()
			if ok {
				return k, nil
			}
		}
	}
	return nil, ErrInvalidToken
}

func (v *OIDCVerifier) Verify(ctx context.Context, raw string) (Claims, error) {
	var tc tokenClaims
	_, err := jwt.ParseWithClaims(raw, &tc, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		return v.key(ctx, kid)
	}, jwt.WithValidMethods([]string{"RS256", "ES256"}), jwt.WithIssuer(v.Issuer), jwt.WithAudience(v.Audience),
		jwt.WithExpirationRequired(), jwt.WithLeeway(30*time.Second))
	if err != nil {
		return Claims{}, ErrInvalidToken
	}
	return tc.toClaims()
}
