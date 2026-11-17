package tracker

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestDedup_MergedEventRecordsEveryRemovedReportNumber(t *testing.T) {
	f := newFixture(t)
	a := f.twin("Example Corp", "Software Engineer", "k:1", "")
	b := f.twin("Example Corp", "Software Engineer", "k:2", "")
	c := f.twin("Example Corp", "Software Engineer", "k:3", "")
	f.twin("Other Corp", "Software Engineer", "k:4", "") // unrelated, no report
	f.exec(`UPDATE tracker_entries SET report_number = 11, score = 3.0, score_kind = 'final' WHERE id = ?`, a)
	f.exec(`UPDATE tracker_entries SET report_number = 12, score = 4.5, score_kind = 'final' WHERE id = ?`, b)
	f.exec(`UPDATE tracker_entries SET report_number = 13, score = 3.5, score_kind = 'final' WHERE id = ?`, c)
	groups, err := f.tr.DedupApply(f.ctx, ActorUser)
	if err != nil || len(groups) != 1 || !groups[0].Merged {
		t.Fatalf("%+v %v", groups, err)
	}
	survivor := groups[0].Survivor
	merged := f.events(survivor, EventMerged)
	if len(merged) != 1 {
		t.Fatalf("%+v", merged)
	}
	var detail struct {
		Removed        []int64 `json:"removed"`
		RemovedReports []int64 `json:"removed_reports"`
	}
	if err := json.Unmarshal(merged[0].Detail, &detail); err != nil {
		t.Fatal(err)
	}
	var want []int64
	for _, row := range []struct{ id, report int64 }{{a, 11}, {b, 12}, {c, 13}} {
		if row.id != survivor {
			want = append(want, row.report)
		}
	}
	if !slices.Equal(detail.RemovedReports, want) || len(detail.Removed) != 2 {
		t.Errorf("removed_reports = %v, want %v (removed %v)", detail.RemovedReports, want, detail.Removed)
	}
}
