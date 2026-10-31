package analytics

import (
	"context"
	"testing"
	"time"

	"github.com/styropyr0/visionary/internal/agentapi"
)

func TestAgentReportStoresHardware(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	util, temp, mem, total := 97.0, 74.0, 20480.0, 24564.0
	rep := &agentapi.Report{
		Host: "gpu-box", At: now.Format(time.RFC3339Nano),
		Samples: []agentapi.Sample{{
			At:     now.Format(time.RFC3339Nano),
			Values: map[string]float64{"interval_s": 15, "psi_mem_some_avg10": 4.2, "major_faults_ps": 12.5, "conntrack_count": 1234, "gpu_count": 1, "gpu_temp_c": 74},
			GPUs:   []agentapi.GPU{{Index: 0, UUID: "GPU-1", Name: "RTX 4090", Vendor: "nvidia", UtilPercent: &util, TempC: &temp, MemUsedMB: &mem, MemTotalMB: &total}},
		}},
		GPUProcesses: []agentapi.GPUProcess{{GPUUUID: "GPU-1", PID: 4242, Name: "python3", UsedMemMB: 18000}},
		Sensors:      []agentapi.Sensor{{Name: "coretemp Package id 0", Kind: "temperature", Value: 61, Unit: "C"}, {Name: "", Kind: "fan"}},
	}
	rows, n := hostRows(rep, now.Format(tsLayout))
	if n["gpus"] != 1 || n["gpu_processes"] != 1 || n["sensors"] != 1 {
		t.Fatalf("counts: %v", n)
	}
	for _, tb := range rows.order {
		for _, st := range otlpInserts(tb, rows.tables[tb], otlpSQLMaxParams, false) {
			if err := s.db.Exec(ctx, st.SQL, st.args...); err != nil {
				t.Fatal(err)
			}
		}
	}
	check := func(q string, want ...any) {
		t.Helper()
		res, err := s.db.Query(ctx, q)
		if err != nil || len(res.Rows) != 1 {
			t.Fatalf("%s: %v %v", q, res, err)
		}
		for i, w := range want {
			if toString(res.Rows[0][i]) != toString(w) {
				t.Errorf("%s: column %d = %v, want %v", q, i, res.Rows[0][i], w)
			}
		}
	}
	check(`SELECT psi_mem_some_avg10, major_faults_ps, conntrack_count, gpu_count, sdk FROM system_metrics WHERE instance = 'gpu-box'`, 4.2, 12.5, 1234, 1, "agent")
	check(`SELECT name, util_percent, temp_c, mem_used_mb, power_w FROM host_gpus WHERE host = 'gpu-box'`, "RTX 4090", 97, 74, 20480, nil)
	check(`SELECT pid, name, used_mem_mb FROM host_gpu_processes WHERE host = 'gpu-box'`, 4242, "python3", 18000)
	check(`SELECT name, value, unit FROM host_sensors WHERE host = 'gpu-box'`, "coretemp Package id 0", 61, "C")
}

func TestOTLPPagingAndGPU(t *testing.T) {
	r := &otlpMetricRow{obs: map[string]map[string]otlpObs{
		"system.paging.faults": {
			"a": {map[string]string{"system.paging.type": "minor"}, 900},
			"b": {map[string]string{"system.paging.type": "major"}, 12},
		},
		"system.paging.operations": {
			"a": {map[string]string{"direction": "page_in", "type": "major"}, 30},
			"b": {map[string]string{"direction": "page_out", "type": "major"}, 40},
			"c": {map[string]string{"direction": "page_in", "type": "minor"}, 5000},
		},
		"hw.gpu.utilization": {
			"a": {map[string]string{"hw.id": "0", "hw.gpu.task": "general"}, 0.5},
			"b": {map[string]string{"hw.id": "1", "hw.gpu.task": "general"}, 0.9},
			"c": {map[string]string{"hw.id": "1", "hw.gpu.task": "encoder"}, 1},
		},
		"hw.gpu.memory.usage": {"a": {map[string]string{"hw.id": "0"}, 3}, "b": {map[string]string{"hw.id": "1"}, 1}},
		"hw.gpu.memory.limit": {"a": {map[string]string{"hw.id": "0"}, 4}, "b": {map[string]string{"hw.id": "1"}, 4}},
	}}
	got := r.columns()
	want := map[string]float64{"minor_faults_ps": 900, "major_faults_ps": 12, "swap_in_ps": 30, "swap_out_ps": 40, "gpu_count": 2, "gpu_util_percent": 90, "gpu_mem_percent": 75}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %v, want %v", k, got[k], w)
		}
	}
}
