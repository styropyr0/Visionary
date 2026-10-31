package visionary

import (
	"context"
	"time"

	"github.com/styropyr0/visionary/sdks/go/skiplist"
)

const skipSettingsPoll = 30 * time.Second

// skipLoop keeps the Settings → Data skip list fresh.
func (a *Agent) skipLoop(ctx context.Context) {
	t := time.NewTimer(0)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		qctx, cancel := context.WithTimeout(quiet(ctx), 15*time.Second)
		rows, err := a.db.rows(qctx, "SELECT name, value FROM visionary_settings WHERE name LIKE 'requests.%'")
		cancel()
		// No server or table yet: keep the last list.
		if err == nil {
			var list []string
			for _, r := range rows {
				if len(r) >= 2 && r[0] == skiplist.Setting {
					list = skiplist.Parse(r[1])
				}
			}
			a.skip.Store(&list)
		}
		t.Reset(skipSettingsPoll)
	}
}

func (a *Agent) skipped(path string) bool {
	list := a.skip.Load()
	return list != nil && skiplist.Match(*list, path)
}
