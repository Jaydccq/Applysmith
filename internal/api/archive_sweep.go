package api

import (
	"context"
	"fmt"

	"applysmith/internal/tracker"
)

// archiveSweep is the body of the scheduled archive.sweep job: the default
// archive rules under the system actor. Without apply it only counts the rows.
func (s *Server) archiveSweep(d *domain) func(context.Context, bool) (string, error) {
	return func(ctx context.Context, apply bool) (string, error) {
		rows, err := d.tracker.Archive(ctx, tracker.ActorSystem, tracker.ArchiveOptions{DryRun: !apply})
		if err != nil {
			return "", err
		}
		if !apply {
			return fmt.Sprintf("%d rows would be archived; nothing changed (set apply on the schedule to archive them)", len(rows)), nil
		}
		if len(rows) > 0 {
			s.notify("tracker")
		}
		return fmt.Sprintf("%d rows archived", len(rows)), nil
	}
}
