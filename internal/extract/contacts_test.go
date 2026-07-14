package extract

import "testing"

func TestNormalizePhone(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"bare 10 digit", "9876543210", "+919876543210"},
		{"leading zero", "09876543210", "+919876543210"},
		{"country code", "919876543210", "+919876543210"},
		{"plus country code with spaces", "+91 98765 43210", "+919876543210"},
		{"hyphenated", "98765-43210", "+919876543210"},
		{"zero prefixed country code", "0919876543210", "+919876543210"},

		{"landline starting 2 is not a mobile", "2212345678", ""},
		{"too short", "98765", ""},
		{"too long", "98765432109876", ""},
		{"empty", "", ""},
		// A hex colour or an asset hash must never look like a phone number.
		{"not a number", "ffffff", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizePhone(tc.in); got != tc.want {
				t.Errorf("normalizePhone(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestContactsFindsStructuredMarkup(t *testing.T) {
	html := `
		<html><body>
			<a href="mailto:hello@chaicafe.in">Email us</a>
			<a href="tel:+919876543210">Call</a>
			<a href="https://wa.me/919812345678">WhatsApp</a>
			<p>Or write to sales@chaicafe.in</p>
		</body></html>`

	got := Contacts("biz-1", html, "test")

	var emails, phones, whatsapps int
	for _, c := range got {
		switch {
		case c.Email != "":
			emails++
		case c.WhatsApp != "":
			whatsapps++
		case c.Phone != "":
			phones++
		}
	}

	if emails != 2 {
		t.Errorf("expected 2 emails, got %d (%+v)", emails, got)
	}
	if phones != 1 {
		t.Errorf("expected 1 phone, got %d", phones)
	}
	if whatsapps != 1 {
		t.Errorf("expected 1 whatsapp, got %d", whatsapps)
	}

	// A mailto: link is a deliberate act by the site owner; a regex hit in body
	// text is a guess. The confidence scores must reflect that.
	for _, c := range got {
		if c.Email == "hello@chaicafe.in" && c.Confidence < 0.9 {
			t.Errorf("mailto address should be high confidence, got %v", c.Confidence)
		}
	}
}

func TestContactsRejectsAssetsAndPlaceholders(t *testing.T) {
	html := `
		<img src="logo@2x.png">
		<p>you@example.com</p>
		<p>noreply@tracking.io</p>
		<style>.x{color:#ff8899}</style>`

	for _, c := range Contacts("biz-1", html, "test") {
		if c.Email != "" {
			t.Errorf("junk address should have been filtered: %q", c.Email)
		}
		if c.Phone != "" {
			t.Errorf("style block should not yield a phone: %q", c.Phone)
		}
	}
}

func TestStripTags(t *testing.T) {
	html := `<div><script>var phone="9999999999";</script><p>Call 98765 43210</p></div>`

	got := StripTags(html)
	if want := "Call 98765 43210"; got != want {
		t.Errorf("StripTags() = %q, want %q", got, want)
	}
}
