// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package bus

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// TokenHeader is the request header that carries the shared capability token.
const TokenHeader = "Authorization"

const bearerScheme = "Bearer"

// WriteToken sets the capability token on h.
// An empty token leaves h unchanged. The value is a bearer credential.
// Callers that send the token use this so the header has one shape.
func WriteToken(h http.Header, token string) error {
	if token == "" {
		return nil
	}
	if err := validToken(token); err != nil {
		return err
	}
	if h == nil {
		return errors.New("missing header")
	}
	h.Set(TokenHeader, bearerScheme+" "+token)
	return nil
}

// ReadTokenFile reads the shared capability token from path.
// A trailing newline is ignored. The result is printable ASCII with no spaces.
// An empty file is an error. The error text does not include the token.
func ReadTokenFile(path string) (string, error) {
	if path == "" {
		return "", errors.New("token file is required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("token file: %w", err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return "", errors.New("token file is empty")
	}
	token := strings.TrimRight(string(raw), "\r\n")
	if err := validToken(token); err != nil {
		return "", errors.New("token file cannot be sent as a header value")
	}
	return token, nil
}

func validToken(token string) error {
	if token == "" {
		return errors.New("token is empty")
	}
	for i := 0; i < len(token); i++ {
		c := token[i]
		if c < 0x21 || c > 0x7e {
			return errors.New("token cannot be sent as a header value")
		}
	}
	return nil
}

type tokenDecision int

const (
	tokenOK tokenDecision = iota
	tokenMissing
	tokenRejected
)

// checkToken reports whether h carries want.
// An empty want fails closed. The comparison does not stop on the first
// differing byte, and the presented value is not returned.
func checkToken(h http.Header, want string) tokenDecision {
	if want == "" {
		return tokenRejected
	}
	v := h.Get(TokenHeader)
	if v == "" {
		return tokenMissing
	}
	scheme, rest, ok := strings.Cut(v, " ")
	if !ok || !strings.EqualFold(scheme, bearerScheme) || !matchToken(rest, want) {
		return tokenRejected
	}
	return tokenOK
}

func matchToken(got, want string) bool {
	g := sha256.Sum256([]byte(got))
	w := sha256.Sum256([]byte(want))
	return subtle.ConstantTimeCompare(g[:], w[:]) == 1
}
