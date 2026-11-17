// Clean-room: docs/provenance.md#CR-SUBSCRIPTIONS
// Written from OpenAI's public Sign in with ChatGPT documentation and the
// research spec (section 1a) alone.

package llm

import (
	"cmp"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// chatgptEndpoint is a path under the issuer.
func (r *Router) chatgptEndpoint(path string) string {
	issuer := r.chatgpt.issuer
	if issuer == "" {
		issuer = chatgptIssuer
	}
	return strings.TrimRight(issuer, "/") + path
}

func (r *Router) chatgptIssuerURL() string { return r.chatgptEndpoint("") }

// oauthError is the refusal of the token endpoint: a 4xx reply that names an
// error code. Anything else (a network failure, a 5xx, a 429) is a transient
// *Error and never ends a sign-in.
type oauthError struct {
	Status      int
	Code        string
	Description string
}

func (e *oauthError) Error() string {
	return boundedText(fmt.Sprintf("%s (HTTP %d) %s", e.Code, e.Status, e.Description))
}

// terminalRefresh lists the refresh errors after which the refresh token can
// never work again: the user has to sign in again.
var terminalRefresh = map[string]bool{
	"invalid_grant": true, "invalid_refresh_token": true, "token_expired": true,
	"refresh_token_expired": true, "refresh_token_invalidated": true, "refresh_token_reused": true,
}

type chatgptTokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	Scope        string `json:"scope"`
	ExpiresIn    int    `json:"expires_in"`
}

// tokenRequest posts a form to the token endpoint. The attempt runs to its end
// whatever the caller does: a refresh cut off in the middle can leave the
// rotated token unsaved, so callers pass a context that is not cancelled with
// theirs.
func (r *Router) tokenRequest(ctx context.Context, form url.Values) (chatgptTokens, error) {
	var out chatgptTokens
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.chatgptEndpoint("/api/accounts/oauth/token"), strings.NewReader(form.Encode()))
	if err != nil {
		return out, &Error{Kind: KindOther, Message: err.Error()}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := r.http.Do(req)
	if err != nil {
		return out, classifyError(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(body, &out); err != nil || out.AccessToken == "" {
			return chatgptTokens{}, &Error{Kind: KindOther, Message: "the token endpoint sent an unreadable reply"}
		}
		return out, nil
	}
	var eb struct {
		Error       json.RawMessage `json:"error"`
		Description string          `json:"error_description"`
	}
	_ = json.Unmarshal(body, &eb)
	code := ""
	var s string
	var o struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	switch {
	case json.Unmarshal(eb.Error, &s) == nil:
		code = s
	case json.Unmarshal(eb.Error, &o) == nil:
		code, eb.Description = o.Code, cmp.Or(eb.Description, o.Message)
	}
	if code == "" || resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
		e := classifyHTTP(resp.StatusCode, boundedText(string(body)))
		e.retryAfter = retryHint(resp.Header.Get("Retry-After"), string(body), time.Now())
		return chatgptTokens{}, e
	}
	return chatgptTokens{}, &oauthError{Status: resp.StatusCode, Code: code, Description: eb.Description}
}

// discovery is the part of the OpenID configuration the sign-in reads.
type discovery struct {
	JWKSURI    string `json:"jwks_uri"`
	Revocation string `json:"revocation_endpoint"`
}

func (r *Router) getJSON(ctx context.Context, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := r.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered HTTP %d", req.URL.Host, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

func (r *Router) discover(ctx context.Context) (discovery, error) {
	var d discovery
	err := r.getJSON(ctx, r.chatgptEndpoint("/.well-known/openid-configuration"), &d)
	return d, err
}

type jwk struct {
	Kty, Kid, Crv, N, E, X, Y string
}

func b64(s string) []byte {
	b, _ := base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
	return b
}

// publicKey builds the verification key of an RSA or P-256 JWK.
func (k jwk) publicKey() (crypto.PublicKey, error) {
	switch k.Kty {
	case "RSA":
		n, e := new(big.Int).SetBytes(b64(k.N)), new(big.Int).SetBytes(b64(k.E))
		if n.Sign() == 0 || !e.IsInt64() || e.Int64() < 3 {
			return nil, errors.New("bad RSA key")
		}
		return &rsa.PublicKey{N: n, E: int(e.Int64())}, nil
	case "EC":
		if k.Crv != "P-256" {
			return nil, errors.New("unsupported curve " + k.Crv)
		}
		pub := &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(b64(k.X)), Y: new(big.Int).SetBytes(b64(k.Y))}
		if !pub.Curve.IsOnCurve(pub.X, pub.Y) {
			return nil, errors.New("bad EC key")
		}
		return pub, nil
	}
	return nil, errors.New("unsupported key type " + k.Kty)
}

// verifySignature checks a compact JWS. Only RS256 and ES256 are accepted:
// never "none", never a symmetric algorithm.
func verifySignature(alg string, key crypto.PublicKey, signed string, sig []byte) bool {
	sum := sha256.Sum256([]byte(signed))
	switch pub := key.(type) {
	case *rsa.PublicKey:
		return alg == "RS256" && rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig) == nil
	case *ecdsa.PublicKey:
		if alg != "ES256" || len(sig) != 64 {
			return false
		}
		return ecdsa.Verify(pub, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:]))
	}
	return false
}

type idClaims struct {
	Iss   string          `json:"iss"`
	Sub   string          `json:"sub"`
	Aud   json.RawMessage `json:"aud"`
	Exp   int64           `json:"exp"`
	Nbf   int64           `json:"nbf"`
	Nonce string          `json:"nonce"`
	Email string          `json:"email"`
}

func (c idClaims) audience(id string) bool {
	var one string
	var many []string
	if json.Unmarshal(c.Aud, &one) == nil {
		return one == id
	}
	if json.Unmarshal(c.Aud, &many) == nil {
		for _, a := range many {
			if a == id {
				return true
			}
		}
	}
	return false
}

const idTokenLeeway = time.Minute

// verifyIDToken validates the ID token of a sign-in against the issuer's
// published keys: signature, issuer, audience (the issued client id),
// expiry, not-before and the nonce of this attempt.
func (r *Router) verifyIDToken(ctx context.Context, raw, clientID, nonce string) (idClaims, error) {
	var c idClaims
	fail := func(why string) (idClaims, error) { return idClaims{}, errors.New("the ID token was refused: " + why) }
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return fail("it is not a signed token")
	}
	var h struct{ Alg, Kid string }
	if json.Unmarshal(b64(parts[0]), &h) != nil || json.Unmarshal(b64(parts[1]), &c) != nil {
		return fail("it is unreadable")
	}
	d, err := r.discover(ctx)
	if err != nil || d.JWKSURI == "" {
		return fail("the issuer's keys could not be fetched")
	}
	var set struct{ Keys []jwk }
	if err := r.getJSON(ctx, d.JWKSURI, &set); err != nil {
		return fail("the issuer's keys could not be fetched")
	}
	verified := false
	for _, k := range set.Keys {
		if h.Kid != "" && k.Kid != h.Kid {
			continue
		}
		if pub, err := k.publicKey(); err == nil && verifySignature(h.Alg, pub, parts[0]+"."+parts[1], b64(parts[2])) {
			verified = true
			break
		}
	}
	now := r.now()
	switch {
	case !verified:
		return fail("its signature does not match the issuer's keys")
	case c.Iss != r.chatgptIssuerURL():
		return fail("wrong issuer")
	case !c.audience(clientID):
		return fail("it was not issued to this app's client")
	case c.Exp == 0 || !now.Before(time.Unix(c.Exp, 0).Add(idTokenLeeway)):
		return fail("it has expired")
	case c.Nbf != 0 && now.Add(idTokenLeeway).Before(time.Unix(c.Nbf, 0)):
		return fail("it is not valid yet")
	case subtle.ConstantTimeCompare([]byte(c.Nonce), []byte(nonce)) != 1:
		return fail("the nonce does not match this sign-in")
	case c.Sub == "":
		return fail("it names no account")
	}
	return c, nil
}

// jwtExpiry reads the exp claim of an access token. The token is not verified
// here: the server that receives it is the judge of that.
func jwtExpiry(token string) (time.Time, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}, false
	}
	var c struct{ Exp int64 }
	if json.Unmarshal(b64(parts[1]), &c) != nil || c.Exp == 0 {
		return time.Time{}, false
	}
	return time.Unix(c.Exp, 0), true
}
