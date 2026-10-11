// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// ErrOutOfSpace marks a write that failed because the database server ran
// out of disk.
var ErrOutOfSpace = errors.New("database out of space")

// Explain turns a database error into one an operator can act on: a full
// disk becomes ErrOutOfSpace with what to do. Other errors are returned as
// they are.
func Explain(err error) error {
	if err == nil || errors.Is(err, ErrOutOfSpace) || !outOfSpace(err) {
		return err
	}
	return fmt.Errorf("%w: free disk space on the database server, then run it again; what was written before stays (%w)", ErrOutOfSpace, err)
}

func outOfSpace(err error) bool {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		// 53100 disk_full; a failed file extension can also come as an I/O
		// error with the OS message.
		return pg.Code == "53100" || strings.Contains(pg.Message, "No space left on device")
	}
	return strings.Contains(err.Error(), "No space left on device")
}
