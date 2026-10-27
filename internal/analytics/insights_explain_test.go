package analytics

import (
	"strings"
	"testing"
)

// A filtered scan of a big table gets an index to add, a scan feeding a hash
// join says so, and a small table is only a note.
func TestPlanFindings(t *testing.T) {
	plan := `Hash Join  (cost=310.40..2250.10 rows=1304 width=64)
  Hash Cond: (l.id = t.listing_id)
  ->  Seq Scan on search_listings l  (cost=0.00..1890.00 rows=1304 width=48)
        Filter: ((status = 'active'::text) AND (shop_id = $1))
  ->  Hash  (cost=190.00..190.00 rows=9348 width=16)
        ->  Seq Scan on search_term_listings t  (cost=0.00..190.00 rows=9348 width=16)
  ->  Seq Scan on search_shops s  (cost=0.00..1.20 rows=20 width=8)`
	got := planFindings("postgres", plan)
	if len(got) != 3 {
		t.Fatalf("got %d findings: %+v", len(got), got)
	}
	if got[0].Severity != "warning" || got[0].Index != "CREATE INDEX ON search_listings (status, shop_id);" {
		t.Errorf("filtered scan: %+v", got[0])
	}
	if got[1].Severity != "warning" || got[1].Index != "" || got[1].Rows != 9348 || !strings.Contains(got[1].Detail, "hash table for a join") {
		t.Errorf("hash scan: %+v", got[1])
	}
	if got[2].Severity != "info" || got[2].Table != "search_shops" {
		t.Errorf("small table: %+v", got[2])
	}
	if cols := filterColumns("(lower(name) ~~ '%a%'::text)"); cols != nil {
		t.Errorf("LIKE filter got columns %v", cols)
	}
}

