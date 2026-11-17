package tracker

import (
	"reflect"
	"testing"
)

func TestHistory_AppliedAndInterviewedCompanies(t *testing.T) {
	f := newFixture(t)
	f.history(histRow{company: "Acme", role: "Engineer", key: "acme"})
	beta := f.history(histRow{company: "Beta", role: "Engineer", key: "beta", applied: "2027-01-02",
		steps: []step{moved("2027-01-05T00:00:00Z", Applied, "interview"), moved("2027-01-09T00:00:00Z", "interview", "rejected")}})
	gamma := f.history(histRow{company: "Gamma", role: "Engineer", key: "gamma"})
	f.exec(`UPDATE tracker_entries SET status = 'rejected' WHERE id = ?`, beta)
	f.exec(`UPDATE tracker_entries SET status = 'evaluated' WHERE id = ?`, gamma)
	h, err := f.tr.History(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := (History{Applied: []string{"Acme", "Beta"}, Interviewed: []string{"Beta"}}); !reflect.DeepEqual(h, want) {
		t.Fatalf("history = %+v, want %+v", h, want)
	}
}
