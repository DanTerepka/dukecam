package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
)

func listenPilotSocket(path string) (net.Listener, error) {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("socket path is occupied by a non-socket file")
		}
		conn, err := net.DialTimeout("unix", path, time.Second)
		if err == nil {
			conn.Close()
			return nil, fmt.Errorf("pilot socket is already active")
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return net.Listen("unix", path)
}

func validatePilotConfig(cfg Config) error {
	u, err := url.Parse(cfg.PilotOrigin)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return fmt.Errorf("RADPAD_ORIGIN must be an HTTPS origin without a path")
	}
	admins := 0
	for _, s := range cfg.PilotAdmins {
		if strings.TrimSpace(s) != "" {
			admins++
		}
	}
	if admins == 0 {
		return fmt.Errorf("RADPAD_ADMINS must explicitly allow at least one Tailscale login")
	}
	if !filepath.IsAbs(cfg.SocketPath) {
		return fmt.Errorf("RADPAD_SOCKET_PATH must be an absolute private Unix socket path")
	}
	return nil
}

func (a *App) PilotHealth(c echo.Context) error {
	ctx, cancel := context.WithTimeout(c.Request().Context(), 2*time.Second)
	defer cancel()
	if a.db == nil || a.db.pool.Ping(ctx) != nil {
		return c.NoContent(http.StatusServiceUnavailable)
	}
	for _, path := range []string{a.config.StoragePath, a.config.ThumbPath} {
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			return c.NoContent(http.StatusServiceUnavailable)
		}
	}
	return c.NoContent(http.StatusNoContent)
}
