package learn

import (
	"strings"
	"testing"
)

func TestAnswerBank_Events_PhoneRedactionWithoutLookaround(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{"spaced number", "call 415 555 0100 now", "call [phone] now"},
		{"international", "Contact alex@example.com or +1 415 555 0100", "Contact [email] or [phone]"},
		{"punctuated", "(415) 555-0100.", "[phone]."},
		{"ats id survives", "question_30011834002", "question_30011834002"},
		{"year range survives", "2021-2024", "2021-2024"},
		{"twenty digits survive", "12345678901234567890", "12345678901234567890"},
		{"glued to a word", "ref415 555 0100", "ref415 555 0100"},
		{"followed by a letter", "415 555 0100x", "415 555 0100x"},
		{"nine digits survive", "415-555-010", "415-555-010"},
		{"fifteen digits are redacted", "+1 22 333 44 555 0100", "[phone]"},
		{"sixteen digits survive", "1234 5678 9012 3456", "1234 5678 9012 3456"},
		{"date survives", "applied on 2031-03-04", "applied on 2031-03-04"},
		{"two numbers", "415 555 0100 or 650 555 0100", "[phone] or [phone]"},
		{"zip survives", "Springfield 90210", "Springfield 90210"},
	} {
		if got := Redact(c.in); got != c.want {
			t.Errorf("%s: Redact(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
	if got := Redact("write to a.b+c@sub.example.org please"); got != "write to [email] please" {
		t.Errorf("email: %q", got)
	}
}

func TestStripURLRemovesCredentialsQueryAndFragment(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"https://user:pass@jobs.example.com/apply/42?token=abc#step2", "https://jobs.example.com/apply/42"},
		{"https://jobs.example.com/apply?", "https://jobs.example.com/apply"},
		{"https://jobs.example.com:8443/a/b?x=1", "https://jobs.example.com:8443/a/b"},
		{"not a url?secret=1", "not a url"},
		{"ftp://u:p@host.example.com/x#frag", "ftp://host.example.com/x"},
		{"https://jobs.example.com/%zz?token=abc", "https://jobs.example.com/%zz"},
	} {
		if got := StripURL(c.in); got != c.want {
			t.Errorf("StripURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := StripURLsIn("Apply at https://jobs.example.com/a?token=abc#x or see https://example.org/?q=1."); strings.Contains(got, "token") || strings.Contains(got, "q=1") || strings.Contains(got, "#x") {
		t.Errorf("StripURLsIn left a query: %q", got)
	}
}

func TestSnippetValidation(t *testing.T) {
	good := []string{
		`<input id="q1" name="role" type="text" placeholder="Role" aria-label="Role" data-testid="x" autocomplete="off">`,
		`<select name="s"><option value="1">One</option></select>`,
		`<input type="radio" name="r" value="yes"> Yes`,
		`<input type=checkbox value=on>`,
		`<label for="q1">Your role</label><!-- a comment -->`,
		``,
	}
	for _, s := range good {
		if err := ValidateSnippet(s); err != nil {
			t.Errorf("%q: %v", s, err)
		}
	}
	bad := []string{
		`<input onfocus="steal()">`,
		`<a href="https://example.com">x</a>`,
		`<div style="color:red">x</div>`,
		`<div class="x">x</div>`,
		`<input type="text" value="typed">`,
		`<div value="x">x</div>`,
		`<script>alert(1)</script>`,
		`<SCRIPT src=x></SCRIPT>`,
		`<style>a{}</style>`,
		`<scr<script>ipt>`,
		`a < b`,
		`<input id="x"`,
		`<!-- open`,
		`<input data-x="1">`,
		strings.Repeat("a", maxSnippetIn+1),
	}
	for _, s := range bad {
		if err := ValidateSnippet(s); err == nil {
			t.Errorf("%.40q must be rejected", s)
		}
	}
}
