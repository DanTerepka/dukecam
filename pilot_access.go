package main

import (
	"context"
	"net"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
)

// Identity headers are trusted ONLY on the private Unix listener consumed by
// Tailscale Serve. A TCP client cannot become an administrator by sending one.
type pilotUnixConnectionKey struct{}

func pilotConnectionContext(ctx context.Context, conn net.Conn) context.Context {
	return context.WithValue(ctx, pilotUnixConnectionKey{}, conn.LocalAddr().Network() == "unix")
}

func pilotAccess(origin string, adminLogins []string) echo.MiddlewareFunc {
	uploadSlot := make(chan struct{}, 1)
	admins := make(map[string]bool, len(adminLogins))
	for _, login := range adminLogins {
		if login = strings.TrimSpace(strings.ToLower(login)); login != "" {
			admins[login] = true
		}
	}
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			h := c.Response().Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Referrer-Policy", "same-origin")
			h.Set("Cache-Control", "private, no-store")
			h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline' 'unsafe-eval'; style-src 'self' 'unsafe-inline'; img-src 'self' blob: data:; media-src 'self' blob:; connect-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'none'; form-action 'self'")
			if c.Path() == "/api/health" && (c.Request().Method == "GET" || c.Request().Method == "HEAD") {
				return next(c)
			}
			trusted, _ := c.Request().Context().Value(pilotUnixConnectionKey{}).(bool)
			login := strings.ToLower(strings.TrimSpace(c.Request().Header.Get("Tailscale-User-Login")))
			if !trusted || origin == "" || !admins[login] {
				return echo.NewHTTPError(http.StatusForbidden, "Connect through the private Rad Pad pilot on Tailscale with an approved account.")
			}
			if !pilotRouteAllowed(c.Request().Method, c.Path()) {
				return echo.NewHTTPError(http.StatusNotFound, "This feature is not enabled in the photo pilot.")
			}
			if c.Request().Method != "GET" && c.Request().Method != "HEAD" {
				// No cookie/password auth or cross-origin writes. Browser writes must come
				// from the configured HTTPS origin, including background service workers.
				if c.Request().Header.Get("Origin") != origin || c.Request().Header.Get("Sec-Fetch-Site") == "cross-site" {
					return echo.NewHTTPError(http.StatusForbidden, "A same-origin request is required.")
				}
			}
			c.Set("pilot_actor", login)
			if c.Path() == "/api/upload" {
				select {
				case uploadSlot <- struct{}{}:
					defer func() { <-uploadSlot }()
				case <-c.Request().Context().Done():
					return echo.NewHTTPError(http.StatusRequestTimeout, "Upload interrupted; retry the saved queue item.")
				}
			}
			return next(c)
		}
	}
}

// An allowlist keeps unreviewed upstream endpoints (including external work
// orders, public sharing and destructive image transforms) out of the pilot.
func pilotRouteAllowed(method, path string) bool {
	if method == "GET" || method == "HEAD" {
		switch path {
		case "/", "/p/:slug", "/admin", "/offline", "/api/photos/:slug", "/media/photo/:slug/:filename", "/media/thumb/:slug/:filename", "/static/*", "/sw.js", "/api/admin/project/:id/qr":
			return true
		}
	}
	if method == "POST" {
		switch path {
		case "/api/upload", "/api/admin/project", "/api/admin/worker", "/api/workers/register", "/api/admin/project/:id/toggle", "/api/admin/worker/:id/toggle":
			return true
		}
	}
	return method == "PATCH" && path == "/api/photo/:id"
}
