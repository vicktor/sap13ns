package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"
	"time"

	"sap13ns/internal/assistant"
	"sap13ns/internal/auth"
	"sap13ns/internal/db"
	"sap13ns/internal/store"
)

// assistantCmd implements "sap13ns assistant reindex|status".
func assistantCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: sap13ns assistant reindex|status [flags]")
	}
	sub, args := args[0], args[1:]
	var full, changed bool
	cfg, log, _, err := setup("assistant "+sub, args, true, func(f *flag.FlagSet) {
		if sub == "reindex" {
			f.BoolVar(&full, "full", false, "embed every passage again instead of reusing unchanged embeddings")
			f.BoolVar(&changed, "changed", false, "only index new and changed posts (what the server does every 10 minutes)")
		}
	})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, err := db.Open(ctx, cfg.DatabaseURL, 4)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool, log); err != nil {
		return err
	}
	e := assistant.New(store.New(pool), auth.NewKeys(cfg.SecretKey), log)

	switch sub {
	case "reindex":
		rep, err := e.Reconcile(ctx, assistant.ReconcileOptions{Rechunk: !changed, Reembed: full})
		if err != nil {
			return err
		}
		fmt.Printf("indexed %d posts, %d failed, %d removed, %d passages embedded in %s\n",
			rep.Indexed, rep.Failed, rep.Removed, rep.Embedded, rep.Duration.Round(time.Millisecond))
		if rep.Failed > 0 {
			fmt.Println("failed posts stay searchable by full text; see `sap13ns assistant status`")
		}
		return nil
	case "status":
		st, err := e.Status(ctx)
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		search := "full text only"
		if st.Semantic {
			search = "hybrid (full text + semantic)"
		}
		row := func(k string, v any) { fmt.Fprintf(w, "%s\t%v\n", k, v) }
		row("search", search)
		if st.Problem != "" {
			row("note", st.Problem)
		}
		row("pgvector", st.Pgvector)
		row("embedding model", or(st.Model, "-"))
		row("embedding column", or(st.Vector, "-"))
		row("eligible posts", st.Eligible)
		row("indexed", st.Indexed)
		row("pending", st.Pending)
		row("failed", st.Failed)
		row("hidden by editors", st.Excluded)
		row("passages", fmt.Sprintf("%d (%d embedded)", st.Chunks, st.Embedded))
		if !st.State.LastRun.IsZero() {
			row("last pass", fmt.Sprintf("%s, %s, %d indexed, %d removed", st.State.LastRun.Local().Format(time.DateTime),
				st.State.Duration, st.State.Indexed, st.State.Removed))
		}
		if st.State.LastError != "" {
			row("last error", st.State.LastError)
		}
		w.Flush()
		for _, f := range st.Failures {
			fmt.Printf("  #%d %s: %s (%d attempts)\n", f.PostID, f.Title, f.Error, f.Attempts)
		}
		return nil
	}
	return fmt.Errorf("unknown assistant command %q", sub)
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
