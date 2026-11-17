// Command video renders the product videos under video/ from code: the scenes
// are a web page whose every frame is a function of time, the frames are
// screenshots of a headless Chrome, the music is synthesized here, and ffmpeg
// joins them. See video/README.md.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

const (
	width  = 1920
	height = 1080
	fps    = 30

	usage = `usage, from the repository root:
  go run ./cmd/video render [intro|setup]   write video/dist/applysmith-<film>.mp4
  go run ./cmd/video music  [intro|setup]   write only video/dist/<film>.wav
  go run ./cmd/video shots                  capture video/shots/ from a new demo workspace`
)

var films = []string{"intro", "setup"}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "video:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	if _, err := os.Stat("video/player.html"); err != nil {
		return errors.New("run from the repository root: video/player.html not found")
	}
	if err := os.MkdirAll("video/dist", 0o755); err != nil {
		return err
	}
	switch args[0] {
	case "render", "music":
		chosen := films
		if len(args) > 1 {
			chosen = args[1:]
		}
		for _, film := range chosen {
			if _, ok := scores[film]; !ok {
				return fmt.Errorf("unknown film %q\n%s", film, usage)
			}
			var err error
			if args[0] == "music" {
				err = musicOnly(film)
			} else {
				err = render(film)
			}
			if err != nil {
				return fmt.Errorf("%s: %w", film, err)
			}
		}
		return nil
	case "shots":
		return shots()
	}
	return errors.New(usage)
}

// browser starts a headless Chrome with a viewport of w by h CSS pixels at the
// given device scale.
func browser(w, h int, scale float64) (context.Context, context.CancelFunc, error) {
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.WindowSize(w, h),
		chromedp.Flag("hide-scrollbars", true),
		chromedp.Flag("force-color-profile", "srgb"),
	)
	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	ctx, cancelCtx := chromedp.NewContext(alloc)
	cancel := func() { cancelCtx(); cancelAlloc() }
	_, err := chromedp.Call(ctx, emulation.SetDeviceMetricsOverride, emulation.SetDeviceMetricsOverrideParams{
		Width: int64(w), Height: int64(h), DeviceScaleFactor: scale,
	})
	if err != nil {
		cancel()
		return nil, nil, err
	}
	return ctx, cancel, nil
}

// serveRepo serves the repository over loopback so the player can load its
// own files and the artwork the dashboard and the docs already hold.
func serveRepo() (string, func(), error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	srv := &http.Server{Handler: http.FileServer(http.Dir("."))}
	go srv.Serve(ln)
	return "http://" + ln.Addr().String(), func() { srv.Close() }, nil
}

// filmSeconds opens the player on a film and returns its length once fonts
// and images are loaded.
func filmSeconds(ctx context.Context, base, film string) (float64, error) {
	if err := chromedp.Do(ctx, chromedp.Navigate(base+"/video/player.html?film="+film)); err != nil {
		return 0, err
	}
	seconds, err := chromedp.Run(ctx, chromedp.Evaluate[float64](`window.ready`, chromedp.EvalAwaitPromise))
	if err != nil {
		return 0, fmt.Errorf("the player did not load (run `go run ./cmd/video shots` first if video/shots is empty): %w", err)
	}
	if seconds <= 0 {
		return 0, errors.New("the player reported no scenes")
	}
	return seconds, nil
}

func musicOnly(film string) error {
	base, stop, err := serveRepo()
	if err != nil {
		return err
	}
	defer stop()
	ctx, cancel, err := browser(width, height, 1)
	if err != nil {
		return err
	}
	defer cancel()
	seconds, err := filmSeconds(ctx, base, film)
	if err != nil {
		return err
	}
	out := filepath.Join("video", "dist", film+".wav")
	fmt.Printf("%s: %.1f s of music -> %s\n", film, seconds, out)
	return writeWAV(out, compose(scores[film], seconds))
}

func render(film string) error {
	base, stop, err := serveRepo()
	if err != nil {
		return err
	}
	defer stop()
	ctx, cancel, err := browser(width, height, 1)
	if err != nil {
		return err
	}
	defer cancel()
	seconds, err := filmSeconds(ctx, base, film)
	if err != nil {
		return err
	}

	wav := filepath.Join("video", "dist", film+".wav")
	if err := writeWAV(wav, compose(scores[film], seconds)); err != nil {
		return err
	}
	out := filepath.Join("video", "dist", "applysmith-"+film+".mp4")
	ffmpeg := exec.Command("ffmpeg", "-y", "-loglevel", "error",
		"-f", "image2pipe", "-framerate", fmt.Sprint(fps), "-c:v", "png", "-i", "-",
		"-i", wav,
		"-c:v", "libx264", "-preset", "medium", "-crf", "17", "-pix_fmt", "yuv420p",
		"-colorspace", "bt709", "-color_primaries", "bt709", "-color_trc", "bt709",
		"-c:a", "aac", "-b:a", "192k", "-shortest", "-movflags", "+faststart", out)
	ffmpeg.Stderr = os.Stderr
	frames, err := ffmpeg.StdinPipe()
	if err != nil {
		return err
	}
	if err := ffmpeg.Start(); err != nil {
		return fmt.Errorf("start ffmpeg: %w", err)
	}

	total := int(seconds * fps)
	began := time.Now()
	for i := 0; i < total; i++ {
		_, err := chromedp.Run(ctx, chromedp.Evaluate[bool](fmt.Sprintf("seek(%f)", float64(i)/fps)))
		if err == nil {
			var shot page.CaptureScreenshotResult
			shot, err = chromedp.Call(ctx, page.CaptureScreenshot, page.CaptureScreenshotParams{Format: page.CaptureScreenshotFormatPng})
			if err == nil {
				_, err = frames.Write(shot.Data)
			}
		}
		if err != nil {
			frames.Close()
			ffmpeg.Wait()
			return fmt.Errorf("frame %d: %w", i, err)
		}
		if i%150 == 0 {
			fmt.Printf("%s: frame %d of %d (%s)\n", film, i, total, time.Since(began).Round(time.Second))
		}
	}
	if err := frames.Close(); err != nil {
		return err
	}
	if err := ffmpeg.Wait(); err != nil {
		return fmt.Errorf("ffmpeg: %w", err)
	}
	fmt.Printf("%s: wrote %s (%.1f s)\n", film, out, seconds)
	return nil
}
