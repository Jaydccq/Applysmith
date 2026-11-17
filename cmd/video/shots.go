package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// A shot is one dashboard page captured into video/shots.
type shot struct{ name, path string }

// The inbox is captured while the sample postings still wait; the rest after
// they are evaluated and the sample form was filled.
var (
	shotsBefore = []shot{{"inbox", "/discovery?tab=inbox"}}
	shotsAfter  = []shot{
		{"home", "/"},
		{"report", "/reports?id=1"},
		{"runs", "/runs"},
		{"tracker", "/tracker"},
		{"answer-bank", "/answer-bank"},
		{"profile", "/profile"},
		{"ask", "/ask"},
		{"setup", "/setup"},
		{"browser", "/browser"},
	}
)

// shots makes every screenshot the films use from a demo workspace it creates
// in a temporary folder, so nothing personal can reach a video: it builds the
// program, walks the demo tutorial (offline provider, three evaluations, a dry
// run of the sample form) and captures the pages on the way.
func shots() error {
	tmp, err := os.MkdirTemp("", "applysmith-video-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	bin := filepath.Join(tmp, "applysmith")
	ws := filepath.Join(tmp, "demo")
	if out, err := exec.Command("go", "build", "-tags", "dashboard", "-o", bin, "./cmd/applysmith").CombinedOutput(); err != nil {
		return fmt.Errorf("build applysmith (run `make generate` first): %w\n%s", err, out)
	}
	cli := func(stdin string, args ...string) error {
		cmd := exec.Command(bin, append(args, "--workspace", ws)...)
		cmd.Stdin = strings.NewReader(stdin)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("applysmith %s: %w\n%s", strings.Join(args, " "), err, out)
		}
		return nil
	}
	if err := os.MkdirAll("video/shots", 0o755); err != nil {
		return err
	}

	if err := cli("", "demo", "init"); err != nil {
		return err
	}
	link, stop, err := serveDemo(bin, ws)
	if err != nil {
		return err
	}
	err = capture(link, shotsBefore)
	stop()
	if err != nil {
		return err
	}

	// The provider is chosen while no server holds the workspace.
	if err := cli(`{"offline": true}`, "setup", "apply", "llm", "--file", "-"); err != nil {
		return err
	}
	link, stop, err = serveDemo(bin, ws)
	if err != nil {
		return err
	}
	defer stop()
	for _, row := range []string{"1", "2", "3"} {
		if err := cli("", "evaluate", row); err != nil {
			return err
		}
	}
	origin, _, _ := strings.Cut(link, "/?")
	if err := cli("", "apply", origin+"/demo/sample-application"); err != nil {
		return err
	}
	return capture(link, shotsAfter)
}

// serveDemo starts a server for the demo workspace on a free port and returns
// its one-time sign-in link.
func serveDemo(bin, ws string) (string, func(), error) {
	cmd := exec.Command(bin, "serve", "--workspace", ws, "--port", "0")
	out, err := cmd.StdoutPipe()
	if err != nil {
		return "", nil, err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return "", nil, err
	}
	stop := func() {
		cmd.Process.Signal(os.Interrupt)
		cmd.Wait()
	}
	lines := bufio.NewScanner(out)
	for lines.Scan() {
		if _, link, ok := strings.Cut(lines.Text(), "Open the dashboard: "); ok {
			go func() {
				for lines.Scan() {
				}
			}()
			return strings.TrimSpace(link), stop, nil
		}
	}
	stop()
	return "", nil, errors.New("the demo server printed no dashboard link")
}

// capture signs in with link and writes one screenshot per page.
func capture(link string, pages []shot) error {
	origin, _, _ := strings.Cut(link, "/?")
	ctx, cancel, err := browser(1440, 1300, 1.5)
	if err != nil {
		return err
	}
	defer cancel()
	if err := chromedp.Do(ctx, chromedp.Navigate(link), chromedp.Sleep(2*time.Second)); err != nil {
		return err
	}
	text, err := chromedp.Run(ctx, chromedp.Evaluate[string](`document.body.innerText`))
	if err != nil {
		return err
	}
	if !strings.Contains(text, "Demo workspace.") {
		return errors.New("the dashboard does not show the demo banner; nothing was captured")
	}
	for _, s := range pages {
		if err := chromedp.Do(ctx, chromedp.Navigate(origin+s.path), chromedp.Sleep(2500*time.Millisecond)); err != nil {
			return fmt.Errorf("%s: %w", s.name, err)
		}
		quality := int64(82)
		img, err := chromedp.Call(ctx, page.CaptureScreenshot, page.CaptureScreenshotParams{Format: page.CaptureScreenshotFormatWebp, Quality: &quality})
		if err != nil {
			return fmt.Errorf("%s: %w", s.name, err)
		}
		file := filepath.Join("video", "shots", s.name+".webp")
		if err := os.WriteFile(file, img.Data, 0o644); err != nil {
			return err
		}
		fmt.Println("wrote", file)
	}
	return nil
}
