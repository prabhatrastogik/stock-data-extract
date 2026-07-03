package kite

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/pquerna/otp/totp"
	kiteconnect "github.com/zerodha/gokiteconnect/v4"
)

// AutoLogin performs the full Kite browser login flow and returns a fresh access token.
// Requires TOTP secret (the base32 secret from the 2FA authenticator setup page).
//
// NOTE: CSS selectors below target the Kite login page as of mid-2024.
// If login breaks, inspect https://kite.zerodha.com in a browser and update selectors.
func AutoLogin(apiKey, apiSecret, userID, password, totpSecret string) (string, error) {
	kc := kiteconnect.New(apiKey)
	loginURL := kc.GetLoginURL()

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:],
			chromedp.Flag("headless", true),
			chromedp.Flag("no-sandbox", true),
			chromedp.Flag("disable-gpu", true),
			chromedp.Flag("disable-dev-shm-usage", true),
		)...,
	)
	defer cancelAlloc()

	ctx, cancelBrowser := chromedp.NewContext(allocCtx)
	defer cancelBrowser()
	ctx, cancelTimeout := context.WithTimeout(ctx, 90*time.Second)
	defer cancelTimeout()

	var currentURL string

	err := chromedp.Run(ctx,
		chromedp.Navigate(loginURL),

		// Fill user ID
		chromedp.WaitVisible(`input[id="userid"]`, chromedp.ByQuery),
		chromedp.Clear(`input[id="userid"]`, chromedp.ByQuery),
		chromedp.SendKeys(`input[id="userid"]`, userID, chromedp.ByQuery),

		// Fill password
		chromedp.WaitVisible(`input[id="password"]`, chromedp.ByQuery),
		chromedp.Clear(`input[id="password"]`, chromedp.ByQuery),
		chromedp.SendKeys(`input[id="password"]`, password, chromedp.ByQuery),

		// Submit credentials
		chromedp.Click(`button[type="submit"]`, chromedp.ByQuery),

		// Wait for TOTP screen — Kite shows a 6-digit input after credentials
		chromedp.WaitVisible(`input[id="totp"]`, chromedp.ByQuery),

		// Generate and fill TOTP
		chromedp.ActionFunc(func(ctx context.Context) error {
			code, err := totp.GenerateCode(totpSecret, time.Now())
			if err != nil {
				return fmt.Errorf("generate totp: %w", err)
			}
			return chromedp.SendKeys(`input[id="totp"]`, code, chromedp.ByQuery).Do(ctx)
		}),

		// Submit TOTP
		chromedp.Click(`button[type="submit"]`, chromedp.ByQuery),

		// Wait until the browser navigates to a URL containing request_token
		chromedp.ActionFunc(func(ctx context.Context) error {
			return waitForURLContains(ctx, "request_token=", 60*time.Second)
		}),

		chromedp.Location(&currentURL),
	)
	if err != nil {
		return "", fmt.Errorf("browser login: %w", err)
	}

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

// waitForURLContains polls the browser location until it contains substr or timeout expires.
func waitForURLContains(ctx context.Context, substr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var loc string
		if err := chromedp.Run(ctx, chromedp.Location(&loc)); err != nil {
			return err
		}
		if strings.Contains(loc, substr) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return fmt.Errorf("timed out waiting for URL to contain %q", substr)
}

