package gmail

import (
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"applysmith/internal/gmail/classify"
	"applysmith/internal/gmail/imaptest"
	"applysmith/internal/store"
)

func TestGmail_Pipeline_SyncStoresTypedSignalsAndNoBodyForOtherMail(t *testing.T) {
	s := newSyncEnv(t)
	s.sy.Classify = NewClassifyFunc(classify.Defaults(), nil)
	s.sy.Version = classify.Version
	s.imap.Add(imaptest.AllMail, msgSpec{ID: "a1", From: "Example Corp <" + addr("no-reply", "greenhouse-mail.io") + ">", To: addr("alex", "example.com"),
		Subject: "Thank you for applying to Example Corp", Text: "We have received your application for the Backend Engineer position.", HTML: `<a href="https://boards.greenhouse.io/exampleco/jobs/123456">job</a>`, MS: 1_800_000_000_000})
	s.imap.Add(imaptest.AllMail, msgSpec{ID: "b1", From: addr("news", "example.org"), Subject: "Gardening tips", Text: "Plant tomatoes.", MS: 1_800_000_000_001})
	s.imap.Add(imaptest.AllMail, msgSpec{ID: "c1", From: "Alex Example <" + addr("alex", "example.com") + ">", To: "Sam <" + addr("sam", "example.org") + ">",
		Subject: "Re: chat", Text: "My availability works for me on Thursday for the interview at Example.", MS: 1_800_000_000_002, Labels: []string{`\Sent`}})
	s.search("Q1", "a1", "b1", "c1")
	rec := s.run(TriggerCLI)
	if rec.Status != "succeeded" || rec.Counts.Stored != 3 || rec.Counts.Ignored != 1 {
		t.Fatalf("run = %+v", rec)
	}
	sigs, err := s.repo.Signals(s.ctx)
	if err != nil || len(sigs) != 2 {
		t.Fatalf("signals = %+v, %v", sigs, err)
	}
	byID := map[string]SignalRow{}
	for _, sg := range sigs {
		byID[sg.MessageID] = sg
	}
	if r := byID["a1"]; r.Type != classify.TypeApplied || r.Tier != classify.TierActionable || r.Direction != classify.DirIncoming || r.Employer != "Example Corp" {
		t.Fatalf("receipt row = %+v", r)
	}
	if r := byID["c1"]; r.Direction != classify.DirOutgoing {
		t.Fatalf("sent row = %+v", r)
	}
	var bodies int
	if err := s.st.Read().QueryRow(`SELECT count(*) FROM gmail_bodies WHERE message_id = 'b1'`).Scan(&bodies); err != nil || bodies != 0 {
		t.Fatalf("a body was stored for non-job mail: %d, %v", bodies, err)
	}
	// The identifiers read from the raw pass are in the evidence trail.
	var detail string
	if err := s.st.Read().QueryRow(`SELECT detail_json FROM gmail_decisions WHERE message_id = 'a1'`).Scan(&detail); err != nil || !strings.Contains(detail, "123456") {
		t.Fatalf("detail = %q, %v", detail, err)
	}
}

func TestGmail_Data_OnlyInSQLite(t *testing.T) {
	s := newSyncEnv(t)
	s.sy.Classify = NewClassifyFunc(classify.Defaults(), nil)
	s.sy.Version = classify.Version
	s.imap.Add(imaptest.AllMail, msgSpec{ID: "e1", From: "Example Corp <" + addr("no-reply", "greenhouse-mail.io") + ">", Subject: "Thank you for applying to Example Corp",
		Text: "We have received your application. Unique marker zebra-quartz-77.", MS: 1_800_000_000_000})
	s.search("Q1", "e1")
	s.run(TriggerCLI)
	if _, err := s.repo.Sweep(s.ctx, BodyMaxBytes); err != nil {
		t.Fatal(err)
	}
	// Every file the workspace holds is the database or its journal files.
	err := filepath.WalkDir(s.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(s.root, p)
		if !strings.HasPrefix(rel, store.StateDir+string(filepath.Separator)) {
			t.Errorf("Gmail wrote %s outside the state directory", rel)
		}
		if !strings.HasPrefix(filepath.Base(rel), store.DBFile) && !strings.HasPrefix(filepath.Base(rel), "backups") {
			t.Errorf("unexpected file %s", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
