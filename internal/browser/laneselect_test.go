package browser

import "testing"

func TestScanLane_DefaultAndWatchSetting(t *testing.T) {
	got := SelectScanLane(LaneRequest{Host: "careers.example.test"})
	if got.Lane != LaneScan || got.Tab.LoadEverything {
		t.Fatalf("default: %+v", got)
	}
	got = SelectScanLane(LaneRequest{Host: "careers.example.test", Watch: true})
	if got.Lane != LaneApply || !got.Tab.LoadEverything {
		t.Fatalf("watch: %+v: the scan runs in the apply window and blocks nothing", got)
	}
	// The guarded list wins over the setting; a host that merely ends in a
	// guarded name is not guarded.
	if got = SelectScanLane(LaneRequest{Host: "www.indeed.com", Watch: true}); got.Lane != LaneScan {
		t.Fatalf("guarded with watch: %+v", got)
	}
	if got = SelectScanLane(LaneRequest{Host: "notindeed.com", Watch: true}); got.Lane != LaneApply {
		t.Fatalf("a lookalike host: %+v", got)
	}
}
