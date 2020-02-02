package edgecontext

import (
	"crypto/rsa"
	"errors"
	"fmt"

	jwt "gopkg.in/dgrijalva/jwt-go.v3"

	"github.com/reddit/baseplate.go/secrets"
)

type keysType []*rsa.PublicKey

const (
	authenticationPubKeySecretPath = "secret/authentication/public-key"
	jwtAlg                         = "RS256"
)

// When trying versioned secret with jwt, there are some errors that won't be
// fixed by the next version of the secret, so we can return early instead of
// trying all the remaining versions.
//
// TODO: We can also get rid of this block when upstream added native support
// for key rotation.
var shortCircuitErrors = []uint32{
	jwt.ValidationErrorMalformed,
	jwt.ValidationErrorAudience,
	jwt.ValidationErrorExpired,
	jwt.ValidationErrorIssuedAt,
	jwt.ValidationErrorIssuer,
	jwt.ValidationErrorNotValidYet,
	jwt.ValidationErrorId,
	jwt.ValidationErrorClaimsInvalid,
}

func shouldShortCircutError(err error) bool {
	var ve jwt.ValidationError
	if errors.As(err, &ve) {
		for _, bitmask := range shortCircuitErrors {
			if ve.Errors&bitmask != 0 {
				return true
			}
		}
	}
	return false
}

// ValidateToken parses and validates a jwt token, and return the decoded
// AuthenticationToken.
func ValidateToken(token string) (*AuthenticationToken, error) {
	keys, ok := keysValue.Load().(keysType)
	if !ok {
		// This would only happen when all previous middleware parsing failed.
		return nil, errors.New("no public keys loaded")
	}

	// TODO: Patch upstream to support key rotation natively:
	// https://github.com/dgrijalva/jwt-go/pull/372
	var lastErr error
	for _, key := range keys {
		token, err := jwt.ParseWithClaims(
			token,
			&AuthenticationToken{},
			func(_ *jwt.Token) (interface{}, error) {
				return key, nil
			},
		)
		if err != nil {
			if shouldShortCircutError(err) {
				return nil, err
			}
			// Try next pubkey.
			lastErr = err
			continue
		}

		if claims, ok := token.Claims.(*AuthenticationToken); ok && token.Valid && token.Method.Alg() == jwtAlg {
			return claims, nil
		}

		lastErr = jwt.NewValidationError("", 0)
	}
	return nil, lastErr
}

func validatorMiddleware(next secrets.SecretHandlerFunc) secrets.SecretHandlerFunc {
	return func(sec *secrets.Secrets) {
		defer next(sec)

		versioned, err := sec.GetVersionedSecret(authenticationPubKeySecretPath)
		if err != nil {
			logger(fmt.Sprintf(
				"Failed to get secrets %q: %v",
				authenticationPubKeySecretPath,
				err,
			))
			return
		}

		all := versioned.GetAll()
		keys := make(keysType, 0, len(all))
		for i, v := range all {
			key, err := jwt.ParseRSAPublicKeyFromPEM([]byte(v))
			if err != nil {
				logger(fmt.Sprintf(
					"Failed to parse key #%d: %v",
					i,
					err,
				))
			} else {
				keys = append(keys, key)
			}
		}

		if len(keys) == 0 {
			logger("No valid keys in secrets store.")
			return
		}

		keysValue.Store(keys)
	}
}
