package extract

import "testing"

func TestGmail_ResetLink_HostEqualsTenant(t *testing.T) {
	link := func(h string) string { return "https://" + h + "/Careers/passwordreset/a1b2c3d4e5" }
	if ResetLink(LinkInput{HTML: anchor(link(host)), From: wd("acme"), Host: host}) == nil {
		t.Fatal("the tenant's own reset link was refused")
	}
	for _, other := range []string{"other-tenant.wd5.myworkdayjobs.com", "x" + host, host + ".evil.test", "wd5.myworkdayjobs.com"} {
		if ResetLink(LinkInput{HTML: anchor(link(other)), From: wd("acme"), Host: host}) != nil {
			t.Errorf("a reset link on %s qualified for %s", other, host)
		}
	}
}
