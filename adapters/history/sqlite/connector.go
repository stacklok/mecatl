package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/url"

	driverSQLite "modernc.org/sqlite"
)

// connector establishes and verifies policy on every physical connection using
// the caller context. DSN _pragma uses Background in this driver and is avoided.
// Returning the original connection preserves all driver optional interfaces.
type connector struct {
	driver.Connector
	create bool
}

func (s connector) Connect(ctx context.Context) (driver.Conn, error) {
	c, err := s.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	if err = configure(ctx, c, s.create); err != nil {
		return nil, errors.Join(err, c.Close())
	}
	return c, nil
}
func configure(ctx context.Context, c driver.Conn, create bool) error {
	for _, setting := range []string{"busy_timeout=5000", "fullfsync=ON", "checkpoint_fullfsync=ON", "synchronous=FULL"} {
		if _, err := c.(driver.ExecerContext).ExecContext(ctx, "PRAGMA "+setting, nil); err != nil {
			return err
		}
	}
	// Only owned temporary databases may be changed into WAL mode. Existing
	// final databases must already have it; even an empty unknown file is not
	// ours to initialize. The sync policies above also govern passive recovery.
	if create {
		if _, err := c.(driver.ExecerContext).ExecContext(ctx, "PRAGMA journal_mode=WAL", nil); err != nil {
			return err
		}
	}
	for _, p := range []struct{ name, want string }{
		{"journal_mode", "wal"}, {"synchronous", "2"}, {"fullfsync", "1"}, {"checkpoint_fullfsync", "1"},
		{"wal_autocheckpoint", "1000"}, {"busy_timeout", "5000"}, {"locking_mode", "normal"}, {"read_uncommitted", "0"},
	} {
		rows, err := c.(driver.QueryerContext).QueryContext(ctx, "PRAGMA "+p.name, nil)
		if err != nil {
			return err
		}
		values := make([]driver.Value, 1)
		err = rows.Next(values)
		closeErr := rows.Close()
		if err != nil || closeErr != nil {
			return errors.Join(err, closeErr)
		}
		if fmt.Sprint(values[0]) != p.want {
			return errors.New("SQLite connection policy mismatch")
		}
	}
	return nil
}

func openDatabase(path string, create bool) (*sql.DB, error) {
	u := url.URL{Scheme: "file", Path: path}
	q := url.Values{"_txlock": {"immediate"}, "mode": {"rw"}}
	if create {
		q.Set("mode", "rwc")
	}
	u.RawQuery = q.Encode()
	c, err := driverSQLite.NewConnector(u.String())
	if err != nil {
		return nil, err
	}
	db := sql.OpenDB(connector{Connector: c, create: create})
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return db, nil
}
