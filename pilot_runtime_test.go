package main

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestPilotConfigMustBePrivateAndExplicit(t *testing.T) {
	good := Config{PilotOrigin: "https://pilot.example.test:4011", PilotAdmins: []string{"dan@example.test"}, SocketPath: "/run/pilot/app.sock"}
	if err := validatePilotConfig(good); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Config){func(c *Config) { c.PilotOrigin = "http://pilot.test" }, func(c *Config) { c.PilotAdmins = []string{" "} }, func(c *Config) { c.SocketPath = "relative.sock" }, func(c *Config) { c.PilotOrigin = "https://pilot.test/path" }} {
		cfg := good
		change(&cfg)
		if validatePilotConfig(cfg) == nil {
			t.Fatal("insecure config accepted")
		}
	}
}

func TestPilotSocketRecoversAfterCrashWithoutRemovingFiles(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "rp-sock-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "app.sock")
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	l.SetUnlinkOnClose(false)
	l.Close()
	recovered, err := listenPilotSocket(path)
	if err != nil {
		t.Fatal(err)
	}
	recovered.Close()
	os.Remove(path)
	if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if l, err := listenPilotSocket(path); err == nil {
		l.Close()
		t.Fatal("ordinary file removed")
	}
	b, _ := os.ReadFile(path)
	if string(b) != "keep" {
		t.Fatal("ordinary file changed")
	}
}

func TestPilotSocketRejectsActiveListener(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "rp-sock-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "app.sock")
	first, err := listenPilotSocket(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if other, err := listenPilotSocket(path); err == nil {
		other.Close()
		t.Fatal("active socket replaced")
	}
}
