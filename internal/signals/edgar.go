package signals

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"applysmith/internal/lazyregexp"
)

const (
	edgarMaxFilings = 1000
	edgarWalkBack   = 4 // extra business days after today
	// edgarDayScope keeps the newest index day read, as yyyymmdd.
	edgarDayScope = "day"
)

// IndexRow is one Form D line of the daily form index.
type IndexRow struct {
	Form   string
	Name   string
	CIK    string
	Date   time.Time
	Folder string // accession number without dashes
}

var indexDateRE = lazyregexp.New(`^\d{4}-?\d{2}-?\d{2}$`)

// ParseFormIndex reads Form D and D/A rows. The line is read from the right:
// the last three fields are the CIK, the filing date and the file name, so a
// header that wraps over two lines cannot break the parse.
func ParseFormIndex(data string) []IndexRow {
	var out []IndexRow
	for _, line := range strings.Split(data, "\n") {
		f := strings.Fields(line)
		if len(f) < 5 {
			continue
		}
		n := len(f)
		file, date, cik := f[n-1], f[n-2], f[n-3]
		if !strings.HasSuffix(file, ".txt") || !indexDateRE.MatchString(date) {
			continue
		}
		form := f[0]
		if form != "D" && form != "D/A" {
			continue
		}
		t, err := time.Parse("2006-01-02", insertDashes(date))
		if err != nil {
			continue
		}
		acc := strings.TrimSuffix(path.Base(file), ".txt")
		out = append(out, IndexRow{Form: form, Name: strings.Join(f[1:n-3], " "), CIK: cik, Date: t, Folder: strings.ReplaceAll(acc, "-", "")})
	}
	return out
}

func insertDashes(d string) string {
	if len(d) == 8 {
		return d[:4] + "-" + d[4:6] + "-" + d[6:]
	}
	return d
}

type primaryDoc struct {
	Issuer struct {
		Name    string `xml:"entityName"`
		Address struct {
			State string `xml:"stateOrCountry"`
		} `xml:"issuerAddress"`
	} `xml:"primaryIssuer"`
	Offering struct {
		Industry struct {
			Type string `xml:"industryGroupType"`
		} `xml:"industryGroup"`
		Filing struct {
			FirstSale struct {
				Value string `xml:"value"`
			} `xml:"dateOfFirstSale"`
		} `xml:"typeOfFiling"`
		Sales struct {
			Total string `xml:"totalOfferingAmount"`
		} `xml:"offeringSalesAmounts"`
	} `xml:"offeringData"`
	Signature struct {
		Date string `xml:"signature>signatureDate"`
	} `xml:"signatureBlock"`
}

// ParseFiling reads the primary document of a Form D. Every field may be
// missing: the date of first sale falls back to the signature date and then to
// the index date, and the issuer to the name on the index row.
func ParseFiling(data []byte, row IndexRow) Filing {
	var d primaryDoc
	_ = xml.Unmarshal(data, &d)
	f := Filing{Issuer: strings.TrimSpace(d.Issuer.Name), Industry: strings.TrimSpace(d.Offering.Industry.Type), State: strings.TrimSpace(d.Issuer.Address.State)}
	if f.Issuer == "" {
		f.Issuer = row.Name
	}
	if v, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(d.Offering.Sales.Total), ",", ""), 64); err == nil && v > 0 {
		f.Amount = v
	}
	for _, s := range []string{d.Offering.Filing.FirstSale.Value, d.Signature.Date} {
		if t, err := time.Parse("2006-01-02", strings.TrimSpace(s)); err == nil {
			f.FirstSale = t
			break
		}
	}
	if f.FirstSale.IsZero() {
		f.FirstSale = row.Date
	}
	return f
}

func filingBody(f Filing, row IndexRow) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s filed Form %s with the SEC", f.Issuer, row.Form)
	if f.Amount > 0 {
		fmt.Fprintf(&b, " for an offering of $%.0f", f.Amount)
	}
	fmt.Fprintf(&b, ". Date of first sale: %s.", f.FirstSale.Format("2006-01-02"))
	if f.Industry != "" {
		fmt.Fprintf(&b, " Industry group: %s.", f.Industry)
	}
	if f.State != "" {
		fmt.Fprintf(&b, " State or country: %s.", f.State)
	}
	return b.String()
}

// businessDays lists up to n weekdays ending at day, newest first.
func businessDays(day time.Time, n int) []time.Time {
	day = time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
	var out []time.Time
	for len(out) < n {
		if wd := day.Weekday(); wd != time.Saturday && wd != time.Sunday {
			out = append(out, day)
		}
		day = day.AddDate(0, 0, -1)
	}
	return out
}

func (e Endpoints) indexURL(day time.Time) string {
	return fmt.Sprintf("%s/Archives/edgar/daily-index/%d/QTR%d/form.%s.idx", e.edgar(), day.Year(), (int(day.Month())-1)/3+1, day.Format("20060102"))
}

func (e Endpoints) filingURL(row IndexRow) string {
	return fmt.Sprintf("%s/Archives/edgar/data/%s/%s/primary_doc.xml", e.edgar(), row.CIK, row.Folder)
}

// collectEdgar reads the daily form index and the Form D filings it lists.
// Today's index is normally not out yet, so it walks back over the business
// days before it: the newest published index always, and after it every
// published one newer than the day the last tick read, so a day on which no
// tick ran is still collected while it is inside the walk. A backfill reads
// every published day of its window.
func collectEdgar(ctx context.Context, env *collectEnv) (Collected, error) {
	email := strings.TrimSpace(env.Profile.Candidate.Email)
	if email == "" {
		return Collected{}, &SkipError{Reason: "needs contact email"}
	}
	hdr := http.Header{"User-Agent": {"applysmith " + email}}
	get := func(u string) (Response, error) {
		return env.Fetch.Do(ctx, Request{URL: u, Header: hdr, Gap: gapEdgar})
	}
	var days []time.Time
	if env.Days > 0 {
		days = businessDays(env.Now(), env.Days+1)
	} else {
		days = businessDays(env.Now(), 1+edgarWalkBack)
	}
	st, err := env.Repo.CollectorState(ctx, SourceEdgar, edgarDayScope)
	if err != nil {
		return Collected{}, err
	}
	var tried []string
	var rows []IndexRow
	published := 0
	newest := st.Cursor
	for _, day := range days {
		stamp := int64(day.Year()*10000 + int(day.Month())*100 + day.Day())
		if env.Days == 0 && published > 0 && stamp <= st.Cursor {
			// An earlier tick read this day and the ones before it.
			break
		}
		res, err := get(env.Endpoints.indexURL(day))
		if err != nil {
			tried = append(tried, day.Format("20060102")+":error")
			continue
		}
		tried = append(tried, fmt.Sprintf("%s:%d", day.Format("20060102"), res.Status))
		if res.Status != http.StatusOK {
			continue
		}
		published++
		dayRows := ParseFormIndex(string(res.Body))
		if len(dayRows) > edgarMaxFilings {
			dayRows = dayRows[:edgarMaxFilings]
		}
		rows = append(rows, dayRows...)
		newest = max(newest, stamp)
		if env.Days == 0 && st.Cursor == 0 {
			// The first run reads the newest index only.
			break
		}
	}
	if published == 0 {
		return Collected{}, &CollectError{Reason: "no index could be fetched: " + strings.Join(tried, ", "), Retryable: true}
	}
	var docs []Doc
	for _, row := range rows {
		if ctx.Err() != nil {
			return Collected{}, ctx.Err()
		}
		u := env.Endpoints.filingURL(row)
		// A filing stored by an earlier tick is emitted from the store: the
		// index was read again, its filings are not downloaded again.
		if d, ok, err := env.Repo.DocumentByURL(ctx, u); err != nil {
			return Collected{}, err
		} else if ok {
			docs = append(docs, Doc{SourceID: d.SourceID, Adapter: d.Adapter, URL: d.URL, FetchedAt: d.FetchedAt, PublishedAt: d.PublishedAt,
				Title: d.Title, Body: d.Body, Filing: d.Filing})
			continue
		}
		res, err := get(u)
		if err != nil || res.Status != http.StatusOK {
			continue
		}
		f := ParseFiling(res.Body, row)
		docs = append(docs, Doc{SourceID: SourceEdgar, Adapter: adapterEdgar, URL: u, FetchedAt: env.Now(), PublishedAt: f.FirstSale,
			Title: "Form " + row.Form + ": " + f.Issuer, Body: filingBody(f, row), Filing: &f})
	}
	out := Collected{Docs: docs}
	if newest > st.Cursor {
		out.Commit = func(ctx context.Context) error {
			return env.Repo.PutCollectorState(ctx, SourceEdgar, edgarDayScope, collectorState{Cursor: newest})
		}
	}
	return out, nil
}
