// Clean-room: docs/provenance.md#CR-SCAN-SKELETON

package discovery

import (
	"database/sql"
	"fmt"

	"applysmith/internal/tracker"
	"applysmith/internal/workspace"
)

// serveGH serves a Greenhouse board and a detail document for each job.
func (e *env) serveGH(slug string, jobs []obj, detail func(id int) obj) {
	e.t.Helper()
	e.srv.json("/v1/boards/"+slug+"/jobs", ghJobs(slug, jobs...))
	for _, j := range jobs {
		id := j["id"].(int)
		d := obj{"id": id, "title": j["title"], "content": "<p>Build things for " + slug + ".</p>"}
		if detail != nil {
			d = detail(id)
		}
		e.srv.json(fmt.Sprintf("/v1/boards/%s/jobs/%d", slug, id), d)
	}
}

// ghIDs builds jobs with ids from..to, titled by the given function.
func ghIDs(from, to int, title func(i int) string, location string) []obj {
	var out []obj
	for i := from; i <= to; i++ {
		out = append(out, ghItem(i, title(i), location))
	}
	return out
}

func engineer(i int) string { return fmt.Sprintf("Software Engineer %d", i) }

func (e *env) inbox(q InboxQuery) []InboxRow {
	e.t.Helper()
	rows, err := e.rp.InboxList(e.ctx, q)
	if err != nil {
		e.t.Fatal(err)
	}
	return rows
}

func (e *env) tracker() *tracker.Tracker {
	e.t.Helper()
	tr, err := tracker.New(e.st, tracker.Options{Now: e.clk.now})
	if err != nil {
		e.t.Fatal(err)
	}
	return tr
}

// trackedRow adds a tracker row for a posting URL.
func (e *env) trackedRow(company, role, url string) tracker.Entry {
	e.t.Helper()
	en, err := e.tracker().Add(e.ctx, tracker.AddRow{Company: company, Role: role, URL: url, Status: tracker.Evaluated})
	if err != nil {
		e.t.Fatal(err)
	}
	return en
}

func eng() workspace.Portals { return portals([]string{"engineer"}, nil) }

func (e *env) archive(id int64) {
	e.t.Helper()
	err := e.st.Write(e.ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(e.ctx, `UPDATE tracker_entries SET archived_at = ? WHERE id = ?`, iso(e.clk.now()), id)
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) exec(q string, args ...any) {
	e.t.Helper()
	err := e.st.Write(e.ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(e.ctx, q, args...)
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
}
