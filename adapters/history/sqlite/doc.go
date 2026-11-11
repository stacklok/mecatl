// Package sqlite stores independent kernel histories in SQLite WAL databases on
// a trusted, application-owned local filesystem. It retains exact opaque commit
// bytes; payload and ancestry verification belong to history.History. Acceptance
// uses WAL with synchronous FULL and ordinary SQLite filesystem assumptions.
// It does not provide execution ownership, fencing, or network-filesystem support.
package sqlite
