package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
)

// mediaToken encodes and decodes signed, opaque media URLs.
//
// Token format (URL-safe base64, no padding):
//
//	<b64url(payload)>.<b64url(hmac-sha256(payload, secret))>
//
// where payload = "<kind>:<server>/<mediaID>"
// and   kind    = "thumb" | "original"
//
// Any request whose HMAC does not validate is rejected with 403.

var errInvalidToken = errors.New("invalid media token")

func newMediaToken(secret, kind, server, mediaID string) string {
	payload := kind + ":" + server + "/" + mediaID
	mac := computeMAC(secret, payload)
	return b64e(payload) + "." + b64e(string(mac))
}

// decodeMediaToken returns kind, server, mediaID or an error.
func decodeMediaToken(secret, token string) (kind, server, mediaID string, err error) {
	dot := strings.LastIndexByte(token, '.')
	if dot < 1 || dot == len(token)-1 {
		return "", "", "", errInvalidToken
	}
	payloadB64, sigB64 := token[:dot], token[dot+1:]

	payloadBytes, err := b64d(payloadB64)
	if err != nil {
		return "", "", "", errInvalidToken
	}
	sigBytes, err := b64d(sigB64)
	if err != nil {
		return "", "", "", errInvalidToken
	}

	payload := string(payloadBytes)
	expected := computeMAC(secret, payload)
	if !hmac.Equal(sigBytes, expected) {
		return "", "", "", errInvalidToken
	}

	// payload = "<kind>:<server>/<mediaID>"
	colon := strings.IndexByte(payload, ':')
	if colon < 1 {
		return "", "", "", errInvalidToken
	}
	kind = payload[:colon]
	rest := payload[colon+1:]

	slash := strings.IndexByte(rest, '/')
	if slash < 1 || slash == len(rest)-1 {
		return "", "", "", errInvalidToken
	}
	server = rest[:slash]
	mediaID = rest[slash+1:]

	if kind != "thumb" && kind != "original" {
		return "", "", "", errInvalidToken
	}
	return kind, server, mediaID, nil
}

func computeMAC(secret, payload string) []byte {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(payload))
	return h.Sum(nil)
}

// b64e is URL-safe base64 without padding.
func b64e(s string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(s))
}

func b64d(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(s)
}
