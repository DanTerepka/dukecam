package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

func TestPilotAccessFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name, login, method, path, origin string
		trusted                           bool
		want                              int
	}{
		{"anonymous", "", "GET", "/", "", true, 403},
		{"spoofed TCP identity", "dan@example.test", "GET", "/", "", false, 403},
		{"other tailnet user", "other@example.test", "GET", "/", "", true, 403},
		{"admin", "dan@example.test", "GET", "/", "", true, 200},
		{"missing mutation origin", "dan@example.test", "POST", "/api/upload", "", true, 403},
		{"foreign mutation origin", "dan@example.test", "POST", "/api/upload", "https://evil.test", true, 403},
		{"valid mutation", "dan@example.test", "POST", "/api/upload", "https://pilot.example.test", true, 200},
		{"no public gallery", "dan@example.test", "GET", "/share/job", "", true, 404},
		{"no destructive deletion", "dan@example.test", "DELETE", "/api/photo/1", "https://pilot.example.test", true, 404},
		{"immutable original", "dan@example.test", "POST", "/api/photo/1/rotate", "https://pilot.example.test", true, 404},
		{"no unreviewed inspection upload", "dan@example.test", "POST", "/api/inspections/1/photos", "https://pilot.example.test", true, 404},
		{"no integration writes", "dan@example.test", "POST", "/api/propertyos/photos", "https://pilot.example.test", true, 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := echo.New()
			e.Use(pilotAccess("https://pilot.example.test", []string{"dan@example.test"}))
			handler := func(c echo.Context) error { return c.String(200, "allowed") }
			for _, path := range []string{"/", "/api/upload", "/share/:slug", "/api/photo/:id", "/api/photo/:id/rotate", "/api/inspections/:id/photos", "/api/propertyos/photos"} {
				e.Any(path, handler)
			}
			req := httptest.NewRequest(tc.method, tc.path, nil)
			req.Header.Set("Tailscale-User-Login", tc.login)
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if tc.trusted {
				req = req.WithContext(context.WithValue(req.Context(), pilotUnixConnectionKey{}, true))
			}
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("got %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
			if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatal("missing content sniffing protection")
			}
		})
	}
}

func TestPilotConnectionTrustIsUnixOnly(t *testing.T) {
	for _, network := range []string{"tcp", "unix"} {
		c := fakePilotConn{network: network}
		trusted, _ := pilotConnectionContext(context.Background(), c).Value(pilotUnixConnectionKey{}).(bool)
		if trusted != (network == "unix") {
			t.Fatalf("wrong trust for %s", network)
		}
	}
}

type fakePilotConn struct {
	net.Conn
	network string
}

func (c fakePilotConn) LocalAddr() net.Addr { return fakePilotAddr(c.network) }

type fakePilotAddr string

func (a fakePilotAddr) Network() string { return string(a) }
func (a fakePilotAddr) String() string  { return "test" }

func TestPilotMediaRequiresIdentity(t *testing.T) {
	e := echo.New()
	e.Use(pilotAccess("https://pilot.example.test", []string{"dan@example.test"}))
	e.GET("/media/photo/:slug/:filename", func(c echo.Context) error { return c.NoContent(http.StatusOK) })
	req := httptest.NewRequest("GET", "/media/photo/job/photo.jpg", nil)
	req = req.WithContext(context.WithValue(req.Context(), pilotUnixConnectionKey{}, true))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("photo exposed without identity: %d", rec.Code)
	}
}
