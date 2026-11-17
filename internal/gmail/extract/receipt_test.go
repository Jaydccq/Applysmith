package extract

import (
	"reflect"
	"strings"
	"testing"
)

var lookup = ReceiptLookup{JobURL: "https://boards.greenhouse.io/exampleco/jobs/123456", ATS: "greenhouse", Tenant: "exampleco", Company: "Example Corp"}

func rcpt(from, subject, body string) ReceiptMessage {
	return ReceiptMessage{From: from, Subject: subject, Body: body}
}

func TestGmail_Receipt_VocabularyAndIdentity(t *testing.T) {
	ats := "Example Corp <" + addr("no-reply", "greenhouse-mail.io") + ">"
	plainATS := "Greenhouse <" + addr("no-reply", "greenhouse-mail.io") + ">"
	for name, c := range map[string]struct {
		m    ReceiptMessage
		want bool
	}{
		"tenant token in the subject":      {rcpt(ats, "Thank you for applying to exampleco", "We got it."), true},
		"company token in the sender":      {rcpt("Example <"+addr("jobs", "example.com")+">", "Application received", "Hello"), true},
		"employer named after applying":    {rcpt(ats, "Application received", "Thanks for applying to the Backend Engineer role at Example Corp."), true},
		"no receipt vocabulary":            {rcpt(ats, "Meeting about exampleco", "Let us talk about the exampleco roadmap."), false},
		"receipt of another company":       {rcpt(plainATS, "Thank you for applying to Sift", "We received your application to Sift."), false},
		"vocabulary but no identity":       {rcpt("Careers <"+addr("no-reply", "mailer.test")+">", "Application received", "Thank you."), false},
		"employer mentioned far from verb": {rcpt(plainATS, "Application received", "Thank you for applying. "+strings.Repeat("Filler words continue here. ", 5)+"Example Corp has many teams."), false},
	} {
		if got := ScoreReceipt(lookup, c.m) > 0; got != c.want {
			t.Errorf("%s: matched = %v, want %v", name, got, c.want)
		}
	}
}

func TestGmail_Receipt_AtsDomainNotIdentity(t *testing.T) {
	m := rcpt("Other <"+addr("no-reply", "greenhouse-mail.io")+">", "Thank you for applying to Sift", "We received your application.")
	if s := ScoreReceipt(lookup, m); s != 0 {
		t.Fatalf("the ATS sender domain alone scored %d", s)
	}
	// The ATS domain adds a point once there is an identity signal, and breaks ties.
	withID := rcpt("Example <"+addr("no-reply", "greenhouse-mail.io")+">", "Thank you for applying to Example", "We received your application.")
	off := rcpt("Example <"+addr("jobs", "example.com")+">", "Thank you for applying to Example", "We received your application.")
	if ScoreReceipt(lookup, withID) <= ScoreReceipt(lookup, off) {
		t.Fatalf("scores: ats=%d off=%d", ScoreReceipt(lookup, withID), ScoreReceipt(lookup, off))
	}
	// A posting on the employer's own domain: that sender plus a token adds one.
	own := ReceiptLookup{JobURL: "https://careers.example.com/jobs/1", ATS: "greenhouse", Tenant: "exampleco", Company: "Example Corp"}
	both := rcpt("Example <"+addr("jobs", "example.com")+">", "Thank you for applying to Example", "We received your application.")
	if s := ScoreReceipt(own, both); s != 3 {
		t.Fatalf("own domain and token = %d, want 3", s)
	}
	// A vendor's registrable domain is never the posting's own domain.
	vendor := ReceiptLookup{JobURL: "https://acme.icims.com/jobs/1", ATS: "icims", Tenant: "acme", Company: "Acme"}
	if s := ScoreReceipt(vendor, rcpt("x <"+addr("no-reply", "icims.com")+">", "Thank you for applying to Sift", "We received your application.")); s != 0 {
		t.Fatalf("a vendor sender identified an employer: %d", s)
	}
}

func TestGmail_Receipt_NonIdentifyingTokens(t *testing.T) {
	got := ReceiptTokens("eofe.fa.us2.oraclecloud.com", "Example Corp Inc")
	if !reflect.DeepEqual(got, []string{"eofe", "example"}) {
		t.Fatalf("tokens = %v", got)
	}
	for _, v := range []string{"com", "net", "jobs", "careers", "wd5", "us2", "greenhouse", "myworkdayjobs", "ab", "oraclecloud"} {
		if len(ReceiptTokens(v)) != 0 {
			t.Errorf("%q should identify nobody", v)
		}
	}
	// A bare host's TLD must not make any employer's receipt look like this one.
	bare := ReceiptLookup{JobURL: "https://eofe.fa.us2.oraclecloud.com/x", ATS: "", Tenant: "eofe.fa.us2.oraclecloud.com", Company: ""}
	if s := ScoreReceipt(bare, rcpt("Other <"+addr("jobs", "other.com")+">", "Application received", "Thank you for applying")); s != 0 {
		t.Fatalf("another employer's receipt scored %d", s)
	}
}

func TestGmail_Receipt_SharedVocabularyWithClassifier(t *testing.T) {
	// The shared vocabulary holds the easy-apply wording and scores its receipt;
	// an application-viewed status is not a receipt.
	samples := map[string]string{
		"applied.receipt.application_was_sent": "Your application was sent to Example Corp",
	}
	if p := findPhrase("applied.receipt.application_was_sent"); p == nil {
		t.Fatal("the easy-apply wording is missing from the shared vocabulary")
	}
	m := rcpt("LinkedIn <"+addr("jobs-noreply", "linkedin.com")+">", samples["applied.receipt.application_was_sent"], "Example Corp")
	if ScoreReceipt(ReceiptLookup{JobURL: "https://www.linkedin.com/jobs/view/1", ATS: "linkedin", Company: "Example Corp"}, m) == 0 {
		t.Fatal("the easy-apply receipt was missed")
	}
	viewed := rcpt(addr("a", "example.com"), "Your application was viewed by Example", "Example Corp viewed it")
	if ScoreReceipt(lookup, viewed) != 0 {
		t.Fatal("a viewed notice is not a receipt")
	}
}

func findPhrase(id string) *Phrase {
	for i := range ReceiptPhrases {
		if ReceiptPhrases[i].ID == id {
			return &ReceiptPhrases[i]
		}
	}
	return nil
}
