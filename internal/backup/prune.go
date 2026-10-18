package backup

import (
	"errors"
	"fmt"
	"os"
	"time"
)

// Prune deletes the archives in dir that a grandfather-father-son schedule
// no longer needs. It keeps the newest archive of each of the keepDaily most
// recent days that have one, likewise for ISO weeks and calendar months
// (UTC), and always the newest archive. Periods without backups do not
// count, so archives are not lost when backups stop for a while. Only
// regular files that match the archive naming pattern are touched. It
// returns the paths it removed.
func Prune(dir string, keepDaily, keepWeekly, keepMonthly int) (removed []string, err error) {
	if keepDaily < 0 || keepWeekly < 0 || keepMonthly < 0 {
		return nil, errors.New("prune: keep counts must not be negative")
	}
	all, err := archives(dir)
	if err != nil {
		return nil, err
	}
	keep := retain(all, keepDaily, keepWeekly, keepMonthly)
	var errs []error
	for i, a := range all {
		if keep[i] {
			continue
		}
		if err := os.Remove(a.Path); err != nil {
			errs = append(errs, err)
			continue
		}
		removed = append(removed, a.Path)
	}
	if len(errs) > 0 {
		return removed, fmt.Errorf("prune: %w", errors.Join(errs...))
	}
	return removed, nil
}

// retain marks the archives to keep. all must be sorted newest first, so the
// first archive met in each period is that period's newest.
func retain(all []Entry, daily, weekly, monthly int) []bool {
	keep := make([]bool, len(all))
	if len(all) > 0 {
		keep[0] = true
	}
	periods := func(n int, key func(time.Time) [3]int) {
		var last [3]int
		for i := 0; i < len(all) && n > 0; i++ {
			if k := key(all[i].Created); i == 0 || k != last {
				keep[i] = true
				last = k
				n--
			}
		}
	}
	periods(daily, func(t time.Time) [3]int { y, m, d := t.Date(); return [3]int{y, int(m), d} })
	periods(weekly, func(t time.Time) [3]int { y, w := t.ISOWeek(); return [3]int{y, w, 0} })
	periods(monthly, func(t time.Time) [3]int { y, m, _ := t.Date(); return [3]int{y, int(m), 0} })
	return keep
}
