package extract

import (
	"strings"
	"testing"
)

func addr(local, domain string) string { return local + "@" + domain }

func wd(local string) string { return "Workday <" + addr(local, WorkdaySenderDomain) + ">" }

func sf() string { return "SuccessFactors <" + addr("system", SuccessFactorsSenderDomain) + ">" }

const host = "acme.wd5.myworkdayjobs.com"

func anchor(href string) string { return `<a href="` + href + `">go</a>` }

func activation(token string) string { return "https://" + host + "/Careers/activate/" + token }

func TestGmail_ActivationLink_AllFourLocksHold(t *testing.T) {
	m := ActivationLink(LinkInput{HTML: anchor(activation("a1b2c3d4e5")), From: wd("acme"), Host: host})
	if m == nil || m.URL != activation("a1b2c3d4e5") || m.Host != host || !strings.Contains(m.Reason, "otp.workday.com") {
		t.Fatalf("match = %+v", m)
	}
	// A subdomain of the sender on a dot boundary qualifies.
	if m := ActivationLink(LinkInput{HTML: anchor(activation("a1b2c3d4e5")), From: "x <" + addr("a", "mail."+WorkdaySenderDomain) + ">", Host: host}); m == nil {
		t.Fatal("sender subdomain refused")
	}
}

func TestGmail_ActivationLink_HostEqualityNotSuffix(t *testing.T) {
	for _, other := range []string{
		"other-tenant.wd5.myworkdayjobs.com", "acme.wd5.myworkdayjobs.com.evil.test", "evilacme.wd5.myworkdayjobs.com",
		"wd5.myworkdayjobs.com", "ACME.example.test",
	} {
		link := "https://" + other + "/Careers/activate/a1b2c3d4e5"
		if m := ActivationLink(LinkInput{HTML: anchor(link), From: wd("acme"), Host: host}); m != nil {
			t.Errorf("host %s qualified", other)
		}
	}
	// The caller names its host; none means no match. Case and spacing are normalised.
	if ActivationLink(LinkInput{HTML: anchor(activation("a1b2c3d4e5")), From: wd("acme")}) != nil {
		t.Error("no host qualified")
	}
	if ActivationLink(LinkInput{HTML: anchor(activation("a1b2c3d4e5")), From: wd("acme"), Host: "  " + strings.ToUpper(host) + " "}) == nil {
		t.Error("host normalisation")
	}
}

func TestGmail_ActivationLink_HttpsOnly(t *testing.T) {
	plain := strings.Replace(activation("a1b2c3d4e5"), "https://", "http://", 1)
	if ActivationLink(LinkInput{HTML: anchor(plain), From: wd("acme"), Host: host}) != nil {
		t.Fatal("plain http qualified")
	}
	// An escaped href is decoded.
	html := `<a href="https://` + host + `/Careers/activate/a1b2c3d4e5?x=1&amp;y=2">go</a>`
	m := ActivationLink(LinkInput{HTML: html, From: wd("acme"), Host: host})
	if m == nil || !strings.HasSuffix(m.URL, "?x=1&y=2") {
		t.Fatalf("escaped href: %+v", m)
	}
}

func TestGmail_ActivationLink_OpaqueToken(t *testing.T) {
	for _, tok := range []string{"", "abcdefgh", "12345678", "a1b2c3", "abc-defg"} {
		link := "https://" + host + "/Careers/activate/" + tok
		if m := ActivationLink(LinkInput{HTML: anchor(link), From: wd("acme"), Host: host}); m != nil {
			t.Errorf("token %q qualified", tok)
		}
	}
	if ActivationLink(LinkInput{HTML: anchor("https://" + host + "/Careers/activate/"), From: wd("acme"), Host: host}) != nil {
		t.Error("token-free path qualified")
	}
}

func TestGmail_ActivationLink_ShapePerAts(t *testing.T) {
	login := "https://" + host + "/Careers/login/a1b2c3d4e5"
	if ActivationLink(LinkInput{HTML: anchor(login), From: wd("acme"), Host: host}) != nil {
		t.Fatal("a sign-in link on the right host qualified")
	}
}

func TestGmail_ActivationLink_SuccessFactorsShape(t *testing.T) {
	const sfHost = "career41.sapsf.com"
	hash := strings.Repeat("Ab3dEf5hIj", 4) + "%3D%3D"
	good := "https://" + sfHost + "/career?company=exampleco&hash=" + hash + "&navBarLevel=EMAIL_VERIFICATION&career_job_req_id=1"
	if m := ActivationLink(LinkInput{HTML: anchor(good), From: sf(), Host: sfHost}); m == nil {
		t.Fatal("SuccessFactors activation refused")
	}
	applying := "https://" + sfHost + "/career?company=exampleco&navBarLevel=JOB_MGMT&career_job_req_id=1"
	if ActivationLink(LinkInput{HTML: anchor(applying), From: sf(), Host: sfHost}) != nil {
		t.Error("a thank-you link without a hash qualified")
	}
	short := "https://" + sfHost + "/career?hash=short&navBarLevel=EMAIL_VERIFICATION"
	if ActivationLink(LinkInput{HTML: anchor(short), From: sf(), Host: sfHost}) != nil {
		t.Error("a short hash qualified")
	}
	other := "https://" + sfHost + "/other?hash=" + hash + "&navBarLevel=EMAIL_VERIFICATION"
	if ActivationLink(LinkInput{HTML: anchor(other), From: sf(), Host: sfHost}) != nil {
		t.Error("a path other than /career qualified")
	}
	for _, h := range []string{"career41.sapsf.com", "career5.successfactors.eu", "career.sapsf.eu"} {
		if !SuccessFactorsHost(h) {
			t.Errorf("%s is a SuccessFactors career host", h)
		}
	}
	for _, h := range []string{"acme.wd5.myworkdayjobs.com", "career41.sapsf.com.evil.test", "xcareer41.sapsf.com", "sapsf.com"} {
		if SuccessFactorsHost(h) {
			t.Errorf("%s is not a SuccessFactors career host", h)
		}
	}
}

func TestGmail_ActivationLink_SenderOwnsItsShape(t *testing.T) {
	const sfHost = "career41.sapsf.com"
	hash := strings.Repeat("Ab3dEf5hIj", 4)
	sfLink := "https://" + sfHost + "/career?hash=" + hash + "&navBarLevel=EMAIL_VERIFICATION"
	if ActivationLink(LinkInput{HTML: anchor(sfLink), From: wd("acme"), Host: sfHost}) != nil {
		t.Error("a SuccessFactors-shaped link from the Workday sender qualified")
	}
	if ActivationLink(LinkInput{HTML: anchor(activation("a1b2c3d4e5")), From: sf(), Host: host}) != nil {
		t.Error("a Workday-shaped link from the SuccessFactors sender qualified")
	}
	for _, from := range []string{"x <" + addr("a", "example.com") + ">", addr("a", WorkdaySenderDomain+".evil.test"), addr("a", "evil"+WorkdaySenderDomain), ""} {
		if ActivationLink(LinkInput{HTML: anchor(activation("a1b2c3d4e5")), From: from, Host: host}) != nil {
			t.Errorf("sender %q qualified", from)
		}
	}
}

func TestGmail_ActivationLink_IgnoresAnchorText(t *testing.T) {
	html := `<p>Please activate your account now!</p><a href="https://` + host + `/Careers/account/settings/a1b2c3d4e5">click to activate</a>`
	if ActivationLink(LinkInput{HTML: html, From: wd("acme"), Host: host}) != nil {
		t.Fatal("persuasive prose qualified a non-activation path")
	}
	// The prose cannot supply the URL either.
	if ActivationLink(LinkInput{HTML: anchor("activate at " + host + "/Careers/activate/a1b2c3d4e5"), From: wd("acme"), Host: host}) != nil {
		t.Fatal("a scheme-less address qualified")
	}
}

func TestGmail_ResetLink_RequiresWorkdaySenderAndShape(t *testing.T) {
	for _, seg := range []string{"passwordreset", "resetpassword", "reset-password", "password-reset", "PasswordReset"} {
		link := "https://" + host + "/Careers/" + seg + "/a1b2c3d4e5"
		if m := ResetLink(LinkInput{HTML: anchor(link), From: wd("acme"), Host: host}); m == nil {
			t.Errorf("segment %s refused", seg)
		}
	}
	link := "https://" + host + "/Careers/passwordreset/a1b2c3d4e5"
	if ResetLink(LinkInput{HTML: anchor(link), From: sf(), Host: host}) != nil {
		t.Error("SuccessFactors sender qualified a reset")
	}
	if ResetLink(LinkInput{HTML: anchor(activation("a1b2c3d4e5")), From: wd("acme"), Host: host}) != nil {
		t.Error("an activation link qualified as a reset")
	}
	if ResetLink(LinkInput{HTML: anchor("https://" + host + "/Careers/passwordreset/abcdefgh"), From: wd("acme"), Host: host}) != nil {
		t.Error("a digit-free token qualified")
	}
}

func TestGmail_ResetLink_OtherShapeFailsClosed(t *testing.T) {
	for _, path := range []string{"/Careers/forgot/a1b2c3d4e5", "/Careers/reset?token=a1b2c3d4e5", "/Careers/password/a1b2c3d4e5"} {
		if ResetLink(LinkInput{HTML: anchor("https://" + host + path), From: wd("acme"), Host: host}) != nil {
			t.Errorf("%s qualified", path)
		}
	}
}

// An activation mail's link is an anchor's href or an address the mail prints
// as visible text (Workday's own tenant prints it; owner decision). An address
// hidden in a comment, a script or any other attribute is never read, and a
// reset link is an anchor's href only.
func TestGmail_Links_HrefsAndPrintedAddressesOnly(t *testing.T) {
	good := activation("a1b2c3d4e5")
	for name, in := range map[string]LinkInput{
		"address in the text part": {Text: "Please open " + good + ". Thanks"},
		"address in html prose":    {HTML: `<p>Open ` + good + ` now</p>`},
		"address as anchor text":   {HTML: `<a href="https://evil.test/x">` + good + `</a>`},
	} {
		in.From, in.Host = wd("acme"), host
		if m := ActivationLink(in); m == nil || m.URL != good {
			t.Errorf("%s: activation match %+v", name, m)
		}
		in.HTML, in.Text = strings.ReplaceAll(in.HTML, "activate", "passwordreset"), strings.ReplaceAll(in.Text, "activate", "passwordreset")
		if m := ResetLink(in); m != nil {
			t.Errorf("%s (reset): qualified %s", name, m.URL)
		}
	}
	for name, in := range map[string]LinkInput{
		"address in an html comment":           {HTML: `<!-- <a href="` + good + `">x</a> ` + good + ` -->`},
		"address in a script block":            {HTML: `<script>var a = '<a href="` + good + `">'</script>`},
		"address in another attribute":         {HTML: `<a title="` + good + `" href="https://evil.test/x">x</a>`},
		"href inside a quoted attribute value": {HTML: `<a title='href="` + good + `"' href="https://evil.test/x">x</a>`},
		"address in an image source":           {HTML: `<img src="` + good + `">`},
		"address in a link tag":                {HTML: `<link href="` + good + `">`},
		"address in a button action":           {HTML: `<form action="` + good + `"><button>go</button></form>`},
		"printed on another host":              {Text: "Open https://evil.test/Careers/activate/a1b2c3d4e5"},
		"printed without a token":              {Text: "Open https://" + host + "/Careers/activate/"},
	} {
		in.From, in.Host = wd("acme"), host
		if m := ActivationLink(in); m != nil {
			t.Errorf("%s: qualified %s", name, m.URL)
		}
	}
	// A printed address from another sender is no activation mail.
	if m := ActivationLink(LinkInput{Text: "Open " + good, From: "x <a@evil.test>", Host: host}); m != nil {
		t.Errorf("another sender's printed address qualified %s", m.URL)
	}
	// The same address as a real href still qualifies, in any attribute order and quote style.
	for _, a := range []string{
		`<a href="` + good + `">x</a>`, `<a href='` + good + `'>x</a>`, `<A HREF="` + good + `">x</A>`,
		`<a class="b" target=_blank href="` + good + `" style="x">x</a>`, `<a href=` + good + `>x</a>`,
	} {
		if ActivationLink(LinkInput{HTML: a, From: wd("acme"), Host: host}) == nil {
			t.Errorf("%s refused", a)
		}
	}
}

// The From header is parsed as one address; its display name and any text
// around it are never read, and the domain must equal the sender domain or be
// a subdomain of it on a dot boundary.
func TestGmail_Links_SenderLockIsEqualityOrDotBoundarySubdomain(t *testing.T) {
	link := anchor(activation("a1b2c3d4e5"))
	for _, from := range []string{
		`"` + WorkdaySenderDomain + `" <` + addr("attacker", "evil.test") + `>`,
		addr("a", "evil.test") + " ." + WorkdaySenderDomain,
		addr("a", "evil.test") + ", " + addr("a", WorkdaySenderDomain),
		"<" + addr("a", WorkdaySenderDomain+".evil.test") + ">",
		"<" + addr("a", "x"+WorkdaySenderDomain) + ">",
		"<" + addr("a", WorkdaySenderDomain+".") + ">",
		"<" + addr("a", "evil.test") + "> <" + addr("b", WorkdaySenderDomain) + ">",
		"Workday <" + WorkdaySenderDomain + ">",
		"Workday <" + addr("a", WorkdaySenderDomain),
		"",
	} {
		if m := ActivationLink(LinkInput{HTML: link, From: from, Host: host}); m != nil {
			t.Errorf("From %q qualified", from)
		}
	}
	for _, from := range []string{wd("acme"), addr("a", strings.ToUpper(WorkdaySenderDomain)), "<" + addr("a", WorkdaySenderDomain) + ">", "<" + addr("a", "x."+WorkdaySenderDomain) + ">"} {
		if ActivationLink(LinkInput{HTML: link, From: from, Host: host}) == nil {
			t.Errorf("From %q refused", from)
		}
	}
}

func TestGmail_SenderDomain_ParsesOneAddressAndFailsClosed(t *testing.T) {
	for from, want := range map[string]string{
		"Workday <" + addr("a", "OTP.Workday.com") + ">":                 "otp.workday.com",
		addr("a", "example.com"):                                         "example.com",
		`"` + WorkdaySenderDomain + `" <` + addr("x", "evil.test") + `>`: "evil.test",
		addr("a", "evil.test") + " ." + WorkdaySenderDomain:              "",
		addr("a", "b.example") + ", " + addr("c", "d.example"):           "",
		"Workday <" + WorkdaySenderDomain + ">":                          "",
		"":                                                               "",
	} {
		if got := SenderDomain(from); got != want {
			t.Errorf("SenderDomain(%q) = %q, want %q", from, got, want)
		}
	}
}

// SuccessFactors keeps the same rule as Workday: its sender domain or a
// subdomain of it, never a name that merely ends in it.
func TestGmail_ActivationLink_SuccessFactorsSenderBoundary(t *testing.T) {
	const sfHost = "career41.sapsf.com"
	link := `<a href="https://` + sfHost + `/career?hash=` + strings.Repeat("Ab3dEf5hIj", 4) + `&navBarLevel=EMAIL_VERIFICATION">go</a>`
	for from, want := range map[string]bool{
		addr("system", SuccessFactorsSenderDomain):                              true,
		addr("system", "mail."+SuccessFactorsSenderDomain):                      true,
		addr("system", "a.b."+SuccessFactorsSenderDomain):                       true,
		addr("system", "evil"+SuccessFactorsSenderDomain):                       false,
		addr("system", SuccessFactorsSenderDomain+".evil.test"):                 false,
		addr("system", "x-"+SuccessFactorsSenderDomain):                         false,
		`"` + SuccessFactorsSenderDomain + `" <` + addr("s", "evil.test") + `>`: false,
	} {
		if got := ActivationLink(LinkInput{HTML: link, From: from, Host: sfHost}) != nil; got != want {
			t.Errorf("From %q: qualified = %v, want %v", from, got, want)
		}
	}
}
