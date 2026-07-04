package kite

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	"github.com/pquerna/otp/totp"
	kiteconnect "github.com/zerodha/gokiteconnect/v4"
)

// AutoLogin performs the full Kite browser login flow and returns a fresh access token.
// Requires TOTP secret (the base32 secret from the 2FA authenticator setup page).
//
// Set KITE_LOGIN_DEBUG=1 to run in a visible (non-headless) browser window.
// On failure, a screenshot is saved to /tmp/kite-login-debug.png for diagnosis.
//
// NOTE: CSS selectors below target the Kite login page as of mid-2024.
// If login breaks, inspect https://kite.zerodha.com in a browser and update selectors.
func AutoLogin(apiKey, apiSecret, userID, password, totpSecret string) (string, error) {
	kc := kiteconnect.New(apiKey)
	loginURL := kc.GetLoginURL()

	debug := os.Getenv("KITE_LOGIN_DEBUG") == "1"

	// In debug mode, build options from scratch without Headless so a real window opens.
	// DefaultExecAllocatorOptions already includes Headless in chromedp v0.15+, so we
	// cannot simply omit our own flag — we must not include it at all.
	var allocOpts []chromedp.ExecAllocatorOption
	if debug {
		allocOpts = []chromedp.ExecAllocatorOption{
			chromedp.NoFirstRun,
			chromedp.NoDefaultBrowserCheck,
			chromedp.Flag("no-sandbox", true),
			chromedp.Flag("disable-dev-shm-usage", true),
		}
	} else {
		allocOpts = append(chromedp.DefaultExecAllocatorOptions[:],
			chromedp.Flag("no-sandbox", true),
			chromedp.Flag("disable-gpu", true),
			chromedp.Flag("disable-dev-shm-usage", true),
		)
	}

	// On macOS, chromedp may not find Chrome via PATH — probe standard app locations.
	if p := findChrome(); p != "" {
		allocOpts = append(allocOpts, chromedp.ExecPath(p))
	}

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), allocOpts...)
	defer cancelAlloc()

	// browserCtx is kept separate from the timeout so we can screenshot after expiry.
	browserCtx, cancelBrowser := chromedp.NewContext(allocCtx)
	ctx, cancelTimeout := context.WithTimeout(browserCtx, 90*time.Second)

	// Capture the redirect URL via a network request event.
	// page.EventFrameNavigated does not fire for failed navigations (connection-refused),
	// but network.EventRequestWillBeSent fires the instant Chrome decides to follow the
	// redirect — before any TCP connection is attempted.
	// Network domain must be explicitly enabled for these events to flow.
	redirectCh := make(chan string, 1)
	chromedp.ListenTarget(ctx, func(ev interface{}) {
		if e, ok := ev.(*network.EventRequestWillBeSent); ok {
			if strings.Contains(e.Request.URL, "request_token=") {
				select {
				case redirectCh <- e.Request.URL:
				default:
				}
			}
		}
	})

	var currentURL string

	err := chromedp.Run(ctx,
		network.Enable(),
		chromedp.Navigate(loginURL),

		// Fill user ID
		chromedp.WaitVisible(`input[id="userid"]`, chromedp.ByQuery),
		chromedp.ActionFunc(reactSet(`input[id="userid"]`, userID)),

		// Fill password
		chromedp.WaitVisible(`input[id="password"]`, chromedp.ByQuery),
		chromedp.ActionFunc(reactSet(`input[id="password"]`, password)),

		// Submit credentials
		chromedp.Click(`button[type="submit"]`, chromedp.ByQuery),

		// Wait for TOTP screen. Kite reuses input[id="userid"] across all login steps;
		// on the TOTP step it switches to type="number" with maxlength=6.
		chromedp.WaitVisible(`input[type="number"]`, chromedp.ByQuery),

		// Generate and fill TOTP.
		chromedp.ActionFunc(func(ctx context.Context) error {
			// If within 4 s of the 30-second TOTP window boundary, wait for the next window.
			if remaining := 30 - (time.Now().Unix() % 30); remaining <= 4 {
				time.Sleep(time.Duration(remaining+1) * time.Second)
			}
			code, err := totp.GenerateCode(totpSecret, time.Now())
			if err != nil {
				return fmt.Errorf("generate totp: %w", err)
			}

			if err := chromedp.Click(`input[type="number"]`, chromedp.ByQuery).Do(ctx); err != nil {
				return fmt.Errorf("click totp input: %w", err)
			}
			if err := chromedp.SendKeys(`input[type="number"]`, code, chromedp.ByQuery).Do(ctx); err != nil {
				return fmt.Errorf("sendkeys totp: %w", err)
			}
			return nil
		}),

		// Submit TOTP
		chromedp.Click(`button[type="submit"]`, chromedp.ByQuery),

		// Wait for the redirect URL captured by ListenTarget above.
		// We can't use Location() here because Chrome navigates to the redirect URL
		// (e.g. 127.0.0.1?request_token=...) but immediately shows a connection-refused
		// error page, at which point Location() returns the Chrome error page URL.
		chromedp.ActionFunc(func(ctx context.Context) error {
			select {
			case url := <-redirectCh:
				currentURL = url
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}),
	)
	cancelTimeout()
	if err != nil {
		saveScreenshot(browserCtx) // browserCtx still valid; timeout context already cancelled above
		cancelBrowser()
		return "", fmt.Errorf("browser login: %w", err)
	}
	cancelBrowser()

	requestToken, err := extractRequestToken(currentURL)
	if err != nil {
		return "", err
	}

	session, err := kc.GenerateSession(requestToken, apiSecret)
	if err != nil {
		return "", fmt.Errorf("generate session: %w", err)
	}

	return session.AccessToken, nil
}

// saveScreenshot captures the current browser state to /tmp/kite-login-debug.png.
// Must be called with the browser context (not the expired timeout context).
func saveScreenshot(browserCtx context.Context) {
	ctx, cancel := context.WithTimeout(browserCtx, 10*time.Second)
	defer cancel()
	var buf []byte
	if err := chromedp.Run(ctx, chromedp.CaptureScreenshot(&buf)); err != nil {
		log.Printf("[login] could not capture screenshot: %v", err)
		return
	}
	path := "/tmp/kite-login-debug.png"
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		log.Printf("[login] could not save screenshot: %v", err)
		return
	}
	log.Printf("[login] screenshot saved to %s — open it to see where the login stopped", path)
}

// reactSet returns a chromedp ActionFunc that sets an input value in a way React's synthetic
// event system recognises. Plain SendKeys/SetValue bypass React's onChange, leaving its
// internal state stale and the form submission sending a blank value.
func reactSet(selector, value string) func(context.Context) error {
	return func(ctx context.Context) error {
		script := fmt.Sprintf(`
			(function() {
				var el = document.querySelector(%q);
				var setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value').set;
				setter.call(el, %q);
				el.dispatchEvent(new Event('input', { bubbles: true }));
				el.dispatchEvent(new Event('change', { bubbles: true }));
			})()
		`, selector, value)
		return chromedp.Evaluate(script, nil).Do(ctx)
	}
}

// findChrome returns the path to Chrome/Chromium on macOS, or "" to let chromedp use PATH.
func findChrome() string {
	candidates := []string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func extractRequestToken(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parse redirect url %q: %w", rawURL, err)
	}
	token := u.Query().Get("request_token")
	if token == "" {
		return "", fmt.Errorf("request_token not found in redirect URL: %s", rawURL)
	}
	return token, nil
}

