package wire

import "testing"

func TestParseCookiesJSON(t *testing.T) {
	got, err := ParseCookies(`{"SID":"a","HSID":"b"}`)
	if err != nil {
		t.Fatal(err)
	}
	if got["SID"] != "a" || got["HSID"] != "b" {
		t.Errorf("got %v", got)
	}
}

func TestParseCookiesFromCurl(t *testing.T) {
	// The shape Chrome's "Copy as cURL" actually produces, complete with the
	// other headers it drags along.
	curl := `curl 'https://messages.google.com/web/rpc' \
  -H 'accept: */*' \
  -H 'cookie: SID=aaa; HSID=bbb; OSID=ccc; SSID=ddd; APISID=eee; SAPISID=fff; __Secure-1PSIDTS=ggg' \
  -H 'user-agent: Mozilla/5.0' \
  --data-raw 'x'`
	got, err := ParseCookies(curl)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range RequiredGaiaCookies {
		if got[name] == "" {
			t.Errorf("missing %s in %v", name, got)
		}
	}
	if got["__Secure-1PSIDTS"] != "ggg" {
		t.Errorf("optional cookie not parsed: %v", got)
	}
	// A stray header must not be mistaken for a cookie.
	if _, ok := got["accept"]; ok {
		t.Error("parsed a non-cookie header as a cookie")
	}
	if len(MissingGaiaCookies(got)) != 0 {
		t.Errorf("expected nothing missing, got %v", MissingGaiaCookies(got))
	}
}

func TestParseCookiesDoubleQuotedAndCookieFlag(t *testing.T) {
	if got, err := ParseCookies(`curl "https://x" -H "Cookie: SID=1; HSID=2"`); err != nil || got["SID"] != "1" {
		t.Errorf("double-quoted form failed: %v %v", got, err)
	}
	if got, err := ParseCookies(`curl https://x -b 'SID=1; HSID=2'`); err != nil || got["HSID"] != "2" {
		t.Errorf("-b form failed: %v %v", got, err)
	}
}

func TestParseCookiesBareHeader(t *testing.T) {
	got, err := ParseCookies("SID=1; HSID=2; SAPISID=3")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Errorf("got %v", got)
	}
}

func TestParseCookiesRejectsJunk(t *testing.T) {
	if _, err := ParseCookies("hello world"); err == nil {
		t.Error("expected an error for input with no cookies")
	}
	if _, err := ParseCookies("   "); err == nil {
		t.Error("expected an error for empty input")
	}
}

func TestMissingGaiaCookies(t *testing.T) {
	missing := MissingGaiaCookies(map[string]string{"SID": "a", "HSID": " "})
	if len(missing) != 5 {
		t.Errorf("expected 5 missing (blank HSID counts), got %v", missing)
	}
}
