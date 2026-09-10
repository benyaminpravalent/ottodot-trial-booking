// Package devpg starts a real, local PostgreSQL 16 without Docker, using
// embedded-postgres (downloads the official binaries once and caches them under
// ~/.embedded-postgres-go). It exists so `go test` and local development work on
// machines without Docker. Production and the documented happy path use docker compose.
package devpg

import (
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
)

// Instance is a running embedded PostgreSQL.
type Instance struct {
	Port int
	pg   *embeddedpostgres.EmbeddedPostgres
	dir  string
}

// URL returns a connection string for the given database on this instance.
func (i *Instance) URL(dbName string) string {
	return fmt.Sprintf("postgres://postgres:postgres@127.0.0.1:%d/%s?sslmode=disable", i.Port, dbName)
}

// Start boots PostgreSQL 16 on the given port (0 = pick a free one) with a database
// called dbName. runtimeDir holds the data directory; "" = a fresh temp dir.
func Start(port int, dbName, runtimeDir string, logs io.Writer) (*Instance, error) {
	if port == 0 {
		p, err := freePort()
		if err != nil {
			return nil, err
		}
		port = p
	}
	if runtimeDir == "" {
		d, err := os.MkdirTemp("", "ottodot-pg-")
		if err != nil {
			return nil, err
		}
		runtimeDir = d
	}
	if logs == nil {
		logs = io.Discard
	}
	home, _ := os.UserHomeDir()
	cfg := embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.V16).
		Port(uint32(port)).
		Database(dbName).
		Username("postgres").
		Password("postgres").
		RuntimePath(runtimeDir).
		CachePath(filepath.Join(home, ".embedded-postgres-go")).
		StartTimeout(120 * time.Second).
		Logger(logs)

	pg := embeddedpostgres.NewDatabase(cfg)
	if err := pg.Start(); err != nil {
		return nil, fmt.Errorf("start embedded postgres: %w", err)
	}
	return &Instance{Port: port, pg: pg, dir: runtimeDir}, nil
}

// Stop shuts the server down.
func (i *Instance) Stop() error {
	return i.pg.Stop()
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
