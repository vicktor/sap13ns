package network

import (
	"context"
	"sync"
	"time"

	"sap13ns/internal/db"
	"sap13ns/internal/store"
	"sap13ns/internal/web"
)

// NetworkStats measures the network for its usage statistics: its own
// tables, then every served site in its schema, a few at a time. A site
// that cannot be measured is counted in Failed, not fatal.
func (h portalHooks) NetworkStats(ctx context.Context, from, to time.Time) (*web.NetStats, error) {
	n := h.r.net
	from = time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, time.UTC)
	to = time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, time.UTC)
	out := &web.NetStats{From: from, To: to, Total: &store.SiteStats{}}
	err := n.db.QueryRow(ctx, `SELECT
		count(*) FILTER (WHERE status = 'active'), count(*) FILTER (WHERE status = 'pending'),
		count(*) FILTER (WHERE status = 'suspended'), coalesce(sum(storage_bytes), 0)::bigint,
		(SELECT count(*) FROM users), (SELECT count(*) FROM subscriptions WHERE state = 'active')
		FROM sites WHERE kind = 'tenant' AND status IN ('pending', 'active', 'suspended')`).
		Scan(&out.Active, &out.Pending, &out.Suspended, &out.StorageBytes, &out.Accounts, &out.Subscriptions)
	if err != nil {
		return nil, err
	}
	rows, err := n.db.Query(ctx, `SELECT coalesce(p.name, s.plan, ''), count(*) FROM sites s LEFT JOIN plans p ON p.slug = s.plan
		WHERE s.kind = 'tenant' AND s.status IN ('pending', 'active', 'suspended')
		GROUP BY 1, p.position ORDER BY p.position NULLS LAST, 1`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var c web.NetCount
		if err := rows.Scan(&c.Name, &c.N); err != nil {
			rows.Close()
			return nil, err
		}
		out.ByPlan = append(out.ByPlan, c)
	}
	if rows.Close(); rows.Err() != nil {
		return nil, rows.Err()
	}
	rows, err = n.db.Query(ctx, `SELECT day, sum(sites)::bigint, sum(accounts)::bigint FROM (
			SELECT (created_at AT TIME ZONE 'UTC')::date AS day, 1 AS sites, 0 AS accounts FROM sites
				WHERE kind = 'tenant' AND created_at >= $1 AND created_at < $2::date + 1
			UNION ALL SELECT (created_at AT TIME ZONE 'UTC')::date, 0, 1 FROM users
				WHERE created_at >= $1 AND created_at < $2::date + 1) d
		GROUP BY day ORDER BY day`, from, to)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var d web.NetDay
		if err := rows.Scan(&d.Day, &d.Sites, &d.Accounts); err != nil {
			rows.Close()
			return nil, err
		}
		out.Growth = append(out.Growth, d)
		out.NewSites += d.Sites
		out.NewAccounts += d.Accounts
	}
	if rows.Close(); rows.Err() != nil {
		return nil, rows.Err()
	}

	sites, err := n.ServedSites(ctx)
	if err != nil {
		return nil, err
	}
	plans, err := h.planIndex(ctx)
	if err != nil {
		return nil, err
	}
	var (
		mu   sync.Mutex
		wg   sync.WaitGroup
		slot = make(chan struct{}, 4)
	)
	for _, s := range sites {
		wg.Add(1)
		slot <- struct{}{}
		go func() {
			defer func() { <-slot; wg.Done() }()
			hd, err := db.Scope(n.pool, s.Schema)
			var st *store.SiteStats
			if err == nil {
				st, err = store.New(hd).SiteStats(ctx, from, to)
			}
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if ctx.Err() == nil {
					h.r.log.Warn("network statistics", "site", s.ID, "err", err)
				}
				out.Failed++
				return
			}
			plan := s.Plan
			if p, ok := plans[s.Plan]; ok {
				plan = p.Name
			}
			out.Sites = append(out.Sites, web.NetSiteStats{ID: s.ID, Slug: s.Slug, Plan: plan, Status: s.Status,
				StorageBytes: s.StorageBytes, Stats: st, Period: st.Period()})
			out.Total.Add(st)
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
