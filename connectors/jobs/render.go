package jobs

import (
	"context"
	"fmt"
	"time"

	"github.com/chromedp/chromedp"

	"roamind.ai/connectors/safehttp"
)

// Renderer loads a page in a browser, running its scripts, and returns
// its HTML; it's for pages that build their content with JavaScript.
type Renderer interface {
	Render(ctx context.Context, url string) (string, error)
}

// Chrome renders pages in headless Chrome. Each page gets a fresh profile,
// so no cookies or logins, and the browser reaches the web only through a
// proxy that connects to public addresses; images aren't loaded. The
// rendered HTML is only read as text, never shown to the model with tools.
type Chrome struct {
	// Path is Chrome's binary.
	Path string
	// Timeout bounds one page, from launch to its HTML.
	Timeout time.Duration
	proxy   *safehttp.Proxy
	slots   chan struct{}
}

// NewChrome returns a renderer running the Chrome at path, at most slots
// pages at a time. Close it to stop its proxy.
func NewChrome(path string, timeout time.Duration, slots int) (*Chrome, error) {
	p, err := safehttp.NewProxy()
	if err != nil {
		return nil, err
	}
	return &Chrome{Path: path, Timeout: timeout, proxy: p, slots: make(chan struct{}, max(slots, 1))}, nil
}

// Close stops the renderer's proxy.
func (c *Chrome) Close() error { return c.proxy.Close() }

// settle is how long a page's text must stay the same to count as loaded.
const settle = time.Second

func (c *Chrome) Render(ctx context.Context, url string) (string, error) {
	select {
	case c.slots <- struct{}{}:
		defer func() { <-c.slots }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(c.Path),
		chromedp.ProxyServer("http://"+c.proxy.Addr),
		// Chrome sends loopback straight past a proxy unless told not to.
		chromedp.Flag("proxy-bypass-list", "<-loopback>"),
		chromedp.Flag("force-webrtc-ip-handling-policy", "disable_non_proxied_udp"),
		chromedp.Flag("webrtc-ip-handling-policy", "disable_non_proxied_udp"),
		chromedp.Flag("blink-settings", "imagesEnabled=false"),
		chromedp.Flag("disable-extensions", true),
		chromedp.UserAgent("Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Safari/537.36 Roamind"),
	)
	actx, stop := chromedp.NewExecAllocator(ctx, opts...)
	defer stop()
	bctx, closeBrowser := chromedp.NewContext(actx)
	defer closeBrowser()

	var html string
	err := chromedp.Run(bctx,
		chromedp.Navigate(url),
		// Half the time is for scripts to fill the page in.
		chromedp.ActionFunc(func(ctx context.Context) error { return waitForText(ctx, c.Timeout/2) }),
		chromedp.OuterHTML("html", &html, chromedp.ByQuery),
	)
	if err != nil {
		return "", fmt.Errorf("render %s: %w", url, err)
	}
	if len(html) > MaxBody {
		return "", fmt.Errorf("render %s: larger than %d MB", url, MaxBody>>20)
	}
	return html, nil
}

// waitForText waits until the page's text has stopped changing for settle,
// or for at most wait, when it keeps what has loaded.
func waitForText(ctx context.Context, wait time.Duration) error {
	deadline := time.After(wait)
	last, since := -1, time.Now()
	for {
		var n int
		if err := chromedp.Evaluate(`document.body ? document.body.innerText.length : 0`, &n).Do(ctx); err != nil {
			return err
		}
		if n != last {
			last, since = n, time.Now()
		} else if n > 0 && time.Since(since) >= settle {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return nil
		case <-time.After(250 * time.Millisecond):
		}
	}
}
