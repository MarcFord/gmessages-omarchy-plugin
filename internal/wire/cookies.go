package wire

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// ParseCookies accepts the two things a person can realistically paste:
// a JSON object of cookie name/value pairs, or a "Copy as cURL" command from
// browser devtools. The cURL form is the practical one — the cookies this
// needs are HttpOnly, so they never appear in document.cookie and can only be
// lifted off a real request.
func ParseCookies(input string) (map[string]string, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return nil, fmt.Errorf("empty input")
	}

	if strings.HasPrefix(trimmed, "{") {
		var out map[string]string
		if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
			return nil, fmt.Errorf("parse JSON cookies: %w", err)
		}
		return out, nil
	}

	if cookies := cookiesFromCurl(trimmed); len(cookies) > 0 {
		return cookies, nil
	}

	// Last resort: a bare "a=1; b=2" cookie header.
	if cookies := parseCookieHeader(trimmed); len(cookies) > 0 {
		return cookies, nil
	}

	return nil, fmt.Errorf("could not find any cookies — paste a JSON object, a Cookie: header, or a 'Copy as cURL' command")
}

// curlCookieRe matches the cookie header however devtools happens to quote it:
// -H 'cookie: ...', -H "Cookie: ...", or the -b/--cookie flag.
var curlCookieRe = regexp.MustCompile(`(?is)(?:-H\s*|--header\s*)['"]\s*cookie:\s*(.*?)['"]|(?:-b|--cookie)\s+['"](.*?)['"]`)

func cookiesFromCurl(input string) map[string]string {
	m := curlCookieRe.FindStringSubmatch(input)
	if m == nil {
		return nil
	}
	header := m[1]
	if header == "" {
		header = m[2]
	}
	return parseCookieHeader(header)
}

func parseCookieHeader(header string) map[string]string {
	out := make(map[string]string)
	for _, part := range strings.Split(header, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, value, found := strings.Cut(part, "=")
		if !found {
			continue
		}
		name = strings.TrimSpace(name)
		value = strings.TrimSpace(value)
		if name == "" || value == "" {
			continue
		}
		out[name] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// MissingGaiaCookies lists which required cookies a set is short of.
func MissingGaiaCookies(cookies map[string]string) []string {
	var missing []string
	for _, name := range RequiredGaiaCookies {
		if strings.TrimSpace(cookies[name]) == "" {
			missing = append(missing, name)
		}
	}
	return missing
}
