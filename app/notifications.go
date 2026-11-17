package main

import (
	"context"
	"errors"
	"net/url"
	"time"

	"github.com/egoist/mygo"

	"applysmith/internal/api/wire"
	"applysmith/internal/launch"
	"applysmith/internal/notify/stream"
	"applysmith/internal/vault"
)

// startNotifications turns the server's notification stream into native
// notifications. The stream is read over HTTP with the API token for an
// attached server and an in-process one alike, so there is one path to keep
// correct. A failure to prepare the token only costs the native banners.
func (a *shell) startNotifications(o launch.Options, origin string) {
	id, err := o.Workspace.ID()
	if err != nil {
		a.logger().Error("notifications off: workspace identity", "err", err)
		return
	}
	v, err := vault.New(id, o.Backend, vault.WithGetenv(o.Getenv))
	if err != nil {
		a.logger().Error("notifications off: vault", "err", err)
		return
	}
	token := func() (string, error) { return v.Get(vault.APIToken) }
	a.mu.Lock()
	a.token = token
	a.mu.Unlock()
	sub := stream.New(stream.Config{
		Origin: origin,
		Token:  token,
		Show:   a.showNotification,
		Close:  func(id string) { mygo.NewNotification(mygo.NotificationOptions{ID: id}).Close() },
		Log:    a.logger(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	a.mu.Lock()
	a.origin, a.notifications, a.notifyStop, a.notifyDone = origin, sub, cancel, done
	a.mu.Unlock()
	go func() {
		defer close(done)
		sub.Run(ctx)
	}()
}

// stopNotifications ends the subscription and waits for it.
func (a *shell) stopNotifications() {
	a.mu.Lock()
	cancel, done := a.notifyStop, a.notifyDone
	a.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
}

// showNotification shows or replaces the native notification of an
// announcement, unless the user is already looking at the page it names.
func (a *shell) showNotification(n wire.NotificationAnnounce) error {
	if a.viewing(n.Link) {
		return nil
	}
	err := mygo.NewNotification(mygo.NotificationOptions{ID: n.ID, Title: n.Title, Body: n.Body}).Show()
	a.notifyDenied.Store(errors.Is(err, mygo.ErrNotificationsDenied))
	return err
}

// viewing reports whether the main window is visible, focused and showing the
// page of link.
func (a *shell) viewing(link string) bool {
	a.mu.Lock()
	w, origin := a.win, a.origin
	a.mu.Unlock()
	if w == nil {
		return false
	}
	want, ok := stream.Target(origin, link)
	if !ok || !w.IsVisible() || !w.IsFocused() {
		return false
	}
	return samePage(w.Page().URL(), want)
}

func samePage(current, want string) bool {
	c, err1 := url.Parse(current)
	w, err2 := url.Parse(want)
	return err1 == nil && err2 == nil && c.Path == w.Path && c.RawQuery == w.RawQuery
}

// notificationClicked brings the window forward and opens the page of the
// announcement, recovering an earlier process's route from the current board.
func (a *shell) notificationClicked(id string) {
	a.showWindow()
	a.mu.Lock()
	sub, origin, w := a.notifications, a.origin, a.win
	a.mu.Unlock()
	if sub == nil || w == nil {
		return
	}
	// The click runs on the main thread, and the acknowledgement is a request.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// The page may ask about unsaved text first; the kept tab need not wait.
		open := func(to string) error { go a.openPage(w.Page(), to); return nil }
		if err := a.openNotification(ctx, id, sub.Clicked(ctx, id), origin, open); err != nil {
			a.logger().Warn("notification page not opened", "err", err)
		}
	}()
}

// openNotification rechecks the board before focusing a kept application tab.
// The route still opens when the tab was closed or cannot be focused. A click
// from a previous process can recover its route from the current board.
func (a *shell) openNotification(ctx context.Context, id, link, origin string, load func(string) error) error {
	var board wire.BoardEntryList
	var entry *wire.BoardEntry
	if err := a.readServer(ctx, "/api/runs?limit=500", &board); err == nil {
		for i := range board.Entries {
			if board.Entries[i].ID == id {
				entry = &board.Entries[i]
				if link == "" {
					link = entry.Link
				}
				break
			}
		}
	}
	target, ok := stream.Target(origin, link)
	if !ok {
		return nil
	}
	if err := load(target); err != nil {
		return err
	}
	if entry != nil && entry.Kind == "apply" && entry.CanFocusTab {
		if err := a.callServer(ctx, "/api/apply/runs/"+url.PathEscape(entry.ID)+"/show-tab", nil); err != nil {
			a.logger().Warn("notification opened; kept application tab not shown", "err", err)
		}
	}
	return nil
}
