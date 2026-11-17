package signals

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

const secHost = "www.sec.gov"

func indexRow(form, name, cik, date, acc string) string {
	return fmt.Sprintf("%-12s%-40s%-10s%-12s%s", form, name, cik, date, "edgar/data/"+cik+"/"+acc+".txt")
}

func formIndex(rows ...string) string {
	return "Form Type   Company Name                             CIK       Date Filed  File Name\n" +
		strings.Repeat("-", 100) + "\n" + strings.Join(rows, "\n") + "\n"
}

const primaryXML = `<?xml version="1.0"?><edgarSubmission><primaryIssuer><entityName>Example Robotics Inc</entityName>
<issuerAddress><stateOrCountry>CA</stateOrCountry></issuerAddress></primaryIssuer>
<offeringData><industryGroup><industryGroupType>Other Technology</industryGroupType></industryGroup>
<typeOfFiling><dateOfFirstSale><value>2026-10-05</value></dateOfFirstSale></typeOfFiling>
<offeringSalesAmounts><totalOfferingAmount>5000000</totalOfferingAmount></offeringSalesAmounts></offeringData>
<signatureBlock><signature><signatureDate>2026-10-08</signatureDate></signature></signatureBlock></edgarSubmission>`

func (e *env) edgarFixture() {
	e.web.status(secHost, "/Archives/edgar/daily-index/2026/QTR4/form.20261012.idx", http.StatusForbidden)
	e.web.body(secHost, "/Archives/edgar/daily-index/2026/QTR4/form.20261009.idx", "text/plain",
		formIndex(indexRow("D", "Example Robotics Inc", "1234567", "2026-10-09", "0001234567-26-000001"),
			indexRow("10-K", "Other Company", "7654321", "2026-10-09", "0007654321-26-000009")))
	e.web.body(secHost, "/Archives/edgar/data/1234567/000123456726000001/primary_doc.xml", "application/xml", primaryXML)
}

func TestEdgar_ParseIndexFromRightSurvivesWrappedHeader(t *testing.T) {
	wrapped := "Form Type   Company Name\nCIK   Date Filed   File Name\n------\n" +
		indexRow("D", "Example Robotics Inc", "1234567", "2026-10-09", "0001234567-26-000001") + "\n" +
		indexRow("D/A", "Alpha Beta Gamma Holdings", "222", "2026-10-09", "0000000222-26-000002") + "\n" +
		indexRow("8-K", "Skipped", "3", "2026-10-09", "0000000003-26-000003") + "\n"
	rows := ParseFormIndex(wrapped)
	if len(rows) != 2 {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].Name != "Example Robotics Inc" || rows[0].CIK != "1234567" || rows[0].Folder != "000123456726000001" {
		t.Fatalf("row 0 = %+v", rows[0])
	}
	if rows[1].Form != "D/A" || rows[1].Name != "Alpha Beta Gamma Holdings" {
		t.Fatalf("row 1 = %+v", rows[1])
	}
}

func TestEdgar_IndexNotYetPublishedUsesPreviousDay(t *testing.T) {
	e := newEnv(t)
	e.enable(SourceEdgar)
	e.edgarFixture()
	sum := e.tick(TickRequest{Sources: []string{SourceEdgar}})
	if got := sum.Sources[0]; got.Status != SrcOK || got.New != 1 {
		t.Fatalf("source = %+v", got)
	}
	var days []string
	for _, r := range e.web.requests(secHost) {
		days = append(days, r.Path[strings.LastIndex(r.Path, "/")+1:])
	}
	if days[0] != "form.20261012.idx" || days[1] != "form.20261009.idx" {
		t.Fatalf("walk = %v", days)
	}
	for _, r := range e.web.requests(secHost) {
		if r.UA != "applysmith alex@example.org" {
			t.Fatalf("user agent = %q", r.UA)
		}
	}
}

func TestEdgar_OffByDefaultAndNeedsContactEmail(t *testing.T) {
	e := newEnv(t)
	e.edgarFixture()
	sum := e.tick()
	for _, s := range sum.Sources {
		if s.ID == SourceEdgar && s.Status != SrcDisabled {
			t.Fatalf("edgar = %+v", s)
		}
	}
	if n := len(e.web.requests(secHost)); n != 0 {
		t.Fatalf("%d requests reached the SEC host while the source was off", n)
	}
	e.enable(SourceEdgar)
	e.sets.Profile.Candidate.Email = ""
	sum = e.tick(TickRequest{Sources: []string{SourceEdgar}})
	if got := sum.Sources[0]; got.Status != SrcSkipped || got.Reason != "needs contact email" {
		t.Fatalf("source = %+v", got)
	}
	if n := len(e.web.requests(secHost)); n != 0 {
		t.Fatalf("%d requests sent without a contact email", n)
	}
}

func TestEdgar_ExtractsFieldsToleratingMissing(t *testing.T) {
	row := IndexRow{Name: "Index Name LLC", Date: time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)}
	f := ParseFiling([]byte(primaryXML), row)
	if f.Issuer != "Example Robotics Inc" || f.Amount != 5e6 || f.Industry != "Other Technology" || f.State != "CA" || f.FirstSale.Format("2006-01-02") != "2026-10-05" {
		t.Fatalf("filing = %+v", f)
	}
	noSale := strings.Replace(primaryXML, "<dateOfFirstSale><value>2026-10-05</value></dateOfFirstSale>", "", 1)
	if g := ParseFiling([]byte(noSale), row); g.FirstSale.Format("2006-01-02") != "2026-10-08" {
		t.Fatalf("signature date fallback = %v", g.FirstSale)
	}
	if g := ParseFiling([]byte("<not-xml"), row); g.Issuer != "Index Name LLC" || g.Amount != 0 || g.FirstSale.Format("2006-01-02") != "2026-10-09" {
		t.Fatalf("index fallback = %+v", g)
	}
	indefinite := strings.Replace(primaryXML, "5000000", "Indefinite", 1)
	if g := ParseFiling([]byte(indefinite), row); g.Amount != 0 || g.Issuer == "" {
		t.Fatalf("indefinite amount = %+v", g)
	}
}

func TestEdgar_BackfillSkipsWeekendsAndUnpublishedDays(t *testing.T) {
	e := newEnv(t)
	e.enable(SourceEdgar)
	e.edgarFixture()
	sum, err := e.svc.Tick(e.ctx, TickRequest{Sources: []string{SourceEdgar}, Days: 3})
	if err != nil {
		t.Fatal(err)
	}
	if got := sum.Sources[0]; got.Status != SrcOK || got.New != 1 {
		t.Fatalf("source = %+v", got)
	}
	for _, r := range e.web.requests(secHost) {
		if strings.Contains(r.Path, "20261010") || strings.Contains(r.Path, "20261011") {
			t.Fatalf("a weekend day was requested: %s", r.Path)
		}
	}
}

// secPaths lists the paths asked of the SEC host from request number from on.
func (e *env) secPaths(from int) []string {
	var out []string
	for _, r := range e.web.requests(secHost)[from:] {
		out = append(out, r.Path)
	}
	return out
}

func (e *env) edgarDay(day, issuer, cik string) {
	acc := "000" + cik + "-26-000001"
	e.web.body(secHost, "/Archives/edgar/daily-index/2026/QTR4/form."+day+".idx", "text/plain",
		formIndex(indexRow("D", issuer, cik, day[:4]+"-"+day[4:6]+"-"+day[6:], acc)))
	e.web.body(secHost, "/Archives/edgar/data/"+cik+"/"+strings.ReplaceAll(acc, "-", "")+"/primary_doc.xml", "application/xml",
		strings.Replace(primaryXML, "Example Robotics Inc", issuer, 1))
}

// A business day on which no tick ran is read by the next one: the collector
// walks back to the day it read last, not only to the newest index.
func TestEdgar_ADayNoTickReadIsReadByTheNextTick(t *testing.T) {
	e := newEnv(t)
	e.enable(SourceEdgar)
	e.edgarFixture() // Monday the 12th: Friday's index is the newest
	if got := e.tick(TickRequest{Sources: []string{SourceEdgar}}).Sources[0]; got.New != 1 {
		t.Fatalf("first tick = %+v", got)
	}
	// No tick on Tuesday. On Wednesday the indexes of Monday and Tuesday are out.
	e.clk.advance(48 * time.Hour)
	e.web.status(secHost, "/Archives/edgar/daily-index/2026/QTR4/form.20261014.idx", http.StatusForbidden)
	e.edgarDay("20261013", "Tuesday Systems Inc", "2222222")
	e.edgarDay("20261012", "Monday Systems Inc", "3333333")
	before := len(e.web.requests(secHost))
	got := e.tick(TickRequest{Sources: []string{SourceEdgar}}).Sources[0]
	if got.Status != SrcOK || got.New != 2 {
		t.Fatalf("the tick after a day without one = %+v", got)
	}
	if n := e.count(`SELECT count(*) FROM signal_documents WHERE title LIKE '%Monday Systems%'`); n != 1 {
		t.Fatal("Monday's filing, which no tick saw as the newest index, was not collected")
	}
	for _, p := range e.secPaths(before) {
		if strings.Contains(p, "20261009") || strings.Contains(p, "1234567") {
			t.Fatalf("a day read by an earlier tick was asked for again: %s", p)
		}
	}
}

// While no newer index is out the newest one is read again, as a sign the
// lane lives, but a filing already stored is not downloaded a second time.
func TestEdgar_AStoredFilingIsNotDownloadedAgain(t *testing.T) {
	e := newEnv(t)
	e.enable(SourceEdgar)
	e.edgarFixture()
	if got := e.tick(TickRequest{Sources: []string{SourceEdgar}}).Sources[0]; got.New != 1 {
		t.Fatalf("first tick = %+v", got)
	}
	before := len(e.web.requests(secHost))
	got := e.tick(TickRequest{Sources: []string{SourceEdgar}}).Sources[0]
	if got.Status != SrcOK || got.Docs != 1 || got.New != 0 {
		t.Fatalf("second tick = %+v", got)
	}
	for _, p := range e.secPaths(before) {
		if strings.HasSuffix(p, "primary_doc.xml") {
			t.Fatalf("a stored filing was downloaded again: %s", p)
		}
	}
}
