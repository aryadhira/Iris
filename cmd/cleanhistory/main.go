// Command cleanhistory rewrites stored assistant replies through the same cleaner the live path uses.
//
// It is deliberately conservative: only replies older than --older-than are touched, and the newest assistant
// reply in each room is always skipped, because rooms/folder.go and the continuation pump still read
// "STATUS: RUNNING" out of stored text for the turn that is in flight.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aryadhira/iris/internal/reply"
)

func main() {
	dsn := flag.String("dsn", os.Getenv("IRIS_DATABASE_URL"), "postgres DSN")
	olderThan := flag.Duration("older-than", 2*60*60*1e9, "only replies older than this are cleaned")
	dry := flag.Bool("dry-run", true, "print changes without writing")
	limit := flag.Int("limit", 500, "max rows to consider")
	flag.Parse()
	if *dsn == "" {
		fmt.Fprintln(os.Stderr, "missing --dsn or IRIS_DATABASE_URL")
		os.Exit(2)
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, *dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect:", err)
		os.Exit(1)
	}
	defer pool.Close()

	rows, err := pool.Query(ctx, `
		select m.id::text, m.room_id::text, m.body
		from messages m
		where m.role = 'assistant'
		  and m.created_at < now() - $1::interval
		  -- a turn that is still in flight keeps its STATUS text: folder.go and the continuation
		  -- pump read it. Only rooms with no active run may be cleaned.
		  and not exists (
			select 1 from runs r
			where r.room_id = m.room_id
			  and r.status in ('queued', 'running', 'waiting_for_approval', 'stopping')
		  )
		order by m.created_at
		limit $2`, *olderThan, *limit)
	if err != nil {
		fmt.Fprintln(os.Stderr, "query:", err)
		os.Exit(1)
	}
	defer rows.Close()

	type change struct {
		id, room, before, after string
	}
	var changes []change
	for rows.Next() {
		var c change
		if err := rows.Scan(&c.id, &c.room, &c.before); err != nil {
			fmt.Fprintln(os.Stderr, "scan:", err)
			os.Exit(1)
		}
		c.after = reply.Clean(c.before)
		if c.after != c.before {
			changes = append(changes, c)
		}
	}
	if err := rows.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "rows:", err)
		os.Exit(1)
	}

	fmt.Printf("considered replies older than %s; %d need cleaning\n", *olderThan, len(changes))
	for _, c := range changes {
		fmt.Printf("  %s room=%s %d -> %d bytes\n", c.id[:8], c.room[:8], len(c.before), len(c.after))
		if *dry {
			fmt.Printf("    before: %q\n    after : %q\n", firstLine(c.before), firstLine(c.after))
		}
	}
	if *dry || len(changes) == 0 {
		if *dry {
			fmt.Println("dry run: nothing written (pass --dry-run=false to apply)")
		}
		return
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "begin:", err)
		os.Exit(1)
	}
	defer tx.Rollback(ctx)
	for _, c := range changes {
		if _, err := tx.Exec(ctx, `update messages set body = $2 where id = $1`, c.id, c.after); err != nil {
			fmt.Fprintln(os.Stderr, "update:", err)
			os.Exit(1)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "commit:", err)
		os.Exit(1)
	}
	fmt.Printf("cleaned %d replies\n", len(changes))
}

func firstLine(s string) string {
	// Collapse everything: these bodies often open with blank lines, and an empty preview
	// tells the operator nothing about what is about to change.
	flat := strings.Join(strings.Fields(s), " ")
	if len(flat) > 110 {
		return flat[:110] + "…"
	}
	return flat
}
