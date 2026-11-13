package analytics

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestSDKContainerIOAndProcesses(t *testing.T) {
	s := newTestServer(t, "sdkctr.db", nil)
	t.Cleanup(func() { _ = s.db.Close() })
	ctx := context.Background()
	if err := s.Init(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	for i := range 4 {
		if err := s.db.Exec(ctx, `INSERT INTO system_metrics (created_at, instance, interval_s, service, sdk, container_cpu_cores, container_mem_used_mb,
  net_rx_kbps, net_tx_kbps, container_read_kbps, container_write_kbps, container_pids) VALUES (?, 'a1b2c3d4e5f6', 15, 'api', 'go', 0.5, 200, 10, 5, 4, 2, 12)`,
			now.Add(-time.Duration(i)*15*time.Second).Format(tsLayout)); err != nil {
			t.Fatal(err)
		}
	}
	// Outside a container the network is the machine's: not a container's.
	if err := s.db.Exec(ctx, `INSERT INTO system_metrics (created_at, instance, interval_s, service, sdk, net_rx_kbps) VALUES (?, 'bare-host', 15, 'cron', 'go', 900)`,
		now.Format(tsLayout)); err != nil {
		t.Fatal(err)
	}
	q := url.Values{"from": {now.Add(-time.Hour).Format(time.RFC3339)}, "to": {now.Add(time.Minute).Format(time.RFC3339)}}
	panel := func(name string, extra url.Values) Result {
		t.Helper()
		v := url.Values{}
		for k, x := range q {
			v[k] = x
		}
		for k, x := range extra {
			v[k] = x
		}
		r := httptest.NewRequest("GET", "/api/containers/panels/"+name+"?"+v.Encode(), nil)
		r.SetPathValue("panel", name)
		w := httptest.NewRecorder()
		s.handleContainerPanel(w, r)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body)
		}
		var res Result
		if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
			t.Fatal(err)
		}
		return res
	}
	one := url.Values{"source": {"sdk"}, "host": {"a1b2c3d4e5f6"}, "container": {"api"}}
	sum := func(res Result, col string) float64 {
		i, total := -1, 0.0
		for j, c := range res.Columns {
			if c == col {
				i = j
			}
		}
		if i < 0 {
			t.Fatalf("no %s in %v", col, res.Columns)
		}
		for _, row := range res.Rows {
			f, _ := cellFloat(row[i])
			total += f
		}
		return total
	}
	if net := panel("network", one); len(net.Rows) == 0 || sum(net, "received") == 0 {
		t.Errorf("network: %+v", net)
	}
	if disk := panel("disk", one); len(disk.Rows) == 0 || sum(disk, "read_bytes") == 0 {
		t.Errorf("disk: %+v", disk)
	}
	if pids := panel("pids", one); len(pids.Rows) == 0 || sum(pids, "pids") == 0 {
		t.Errorf("pids: %+v", pids)
	}
	all := panel("network", url.Values{"source": {"sdk"}})
	for _, row := range all.Rows {
		if toString(row[1]) == "cron" {
			t.Errorf("a host outside a container charted as one: %v", row)
		}
	}

	rows := s.sdkContainers(ctx, timeRange{From: now.Add(-time.Hour), To: now.Add(time.Minute)})
	if len(rows) != 1 || toInt(rows[0][17]) != 12 {
		t.Errorf("list: %v", rows)
	}
}
