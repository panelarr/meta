// SPDX-License-Identifier: GPL-3.0-or-later

// Command meta runs the Panelarr metadata service.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/panelarr/meta/internal/buildinfo"
	"github.com/panelarr/meta/internal/store"
)

const usage = `meta: the Panelarr metadata service

Usage:
  meta migrate [up|down|status]   apply, roll back or list database migrations
  meta version                    print the version

The database comes from --database-url or META_DATABASE_URL.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Stdout)
	stop()
	if errors.Is(err, errUsage) {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "meta:", err)
		os.Exit(1)
	}
}

var errUsage = errors.New("usage")

func run(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errUsage
	}
	switch args[0] {
	case "version":
		_, err := fmt.Fprintln(out, buildinfo.Version)
		return err
	case "migrate":
		return migrate(ctx, args[1:], out)
	default:
		return errUsage
	}
}

func migrate(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	dsn := fs.String("database-url", os.Getenv("META_DATABASE_URL"), "PostgreSQL URL")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	if *dsn == "" {
		return errors.New("no database: set META_DATABASE_URL or --database-url")
	}
	s, err := store.Open(ctx, *dsn)
	if err != nil {
		return err
	}
	defer s.Close()

	cmd := "up"
	if fs.NArg() > 0 {
		cmd = fs.Arg(0)
	}
	switch cmd {
	case "up":
		res, err := s.Migrate(ctx)
		if err != nil {
			return err
		}
		if len(res) == 0 {
			_, err = fmt.Fprintln(out, "database is up to date")
			return err
		}
		for _, r := range res {
			if _, err := fmt.Fprintf(out, "applied %d %s\n", r.Version, r.Name); err != nil {
				return err
			}
		}
	case "down":
		res, err := s.MigrateDown(ctx)
		if err != nil {
			return err
		}
		for _, r := range res {
			if _, err := fmt.Fprintf(out, "rolled back %d %s\n", r.Version, r.Name); err != nil {
				return err
			}
		}
	case "status":
		st, err := s.Status(ctx)
		if err != nil {
			return err
		}
		for _, m := range st {
			state := "pending"
			if m.Applied {
				state = "applied"
			}
			if _, err := fmt.Fprintf(out, "%-8s %d %s\n", state, m.Version, m.Name); err != nil {
				return err
			}
		}
	default:
		return errUsage
	}
	return nil
}
