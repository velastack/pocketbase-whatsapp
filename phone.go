package whatsapp

import (
	"errors"
	"strings"
)

var (
	errInvalidPhone      = errors.New("invalid phone number, must be in international format (eg. +16165550123)")
	errCountryNotAllowed = errors.New("phone numbers from this country are not allowed")
)

// NormalizePhone normalizes the provided raw phone number to the
// E.164 format (eg. "+16165550123").
//
// It accepts common separators (spaces, dashes, dots, parenthesis) and
// the "00" international prefix but it intentionally doesn't try to guess
// a country code for national numbers (the client is expected to send the
// number in international format, eg. with the help of a country picker).
func NormalizePhone(raw string) (string, error) {
	raw = strings.TrimSpace(raw)

	if strings.HasPrefix(raw, "00") {
		raw = "+" + raw[2:]
	}

	if !strings.HasPrefix(raw, "+") {
		return "", errInvalidPhone
	}

	var digits strings.Builder
	digits.Grow(len(raw))

	for _, r := range raw[1:] {
		switch {
		case r >= '0' && r <= '9':
			digits.WriteRune(r)
		case r == ' ' || r == '-' || r == '.' || r == '(' || r == ')':
			// skip common separators
		default:
			return "", errInvalidPhone
		}
	}

	d := digits.String()

	// E.164 allows max 15 digits (incl. the country code);
	// the shortest assigned numbers are ~8 digits
	if len(d) < 8 || len(d) > 15 || d[0] == '0' {
		return "", errInvalidPhone
	}

	return "+" + d, nil
}

// isCountryAllowed reports whether the E.164 phone starts with one
// of the allowed country calling codes (eg. "1", "52", "+244").
//
// An empty allowed list means that all countries are allowed.
func isCountryAllowed(phone string, allowedCallingCodes []string) bool {
	if len(allowedCallingCodes) == 0 {
		return true
	}

	digits := strings.TrimPrefix(phone, "+")

	for _, code := range allowedCallingCodes {
		code = strings.TrimPrefix(strings.TrimSpace(code), "+")
		if code != "" && strings.HasPrefix(digits, code) {
			return true
		}
	}

	return false
}
