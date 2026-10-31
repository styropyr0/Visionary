package main

import (
	"context"

	"github.com/styropyr0/visionary/internal/agentapi"
	"github.com/styropyr0/visionary/sdks/go/hoststats"
)

// gpus copies the sampler's GPU reading into the wire format
func gpus(in []hoststats.GPU) []agentapi.GPU {
	if len(in) == 0 {
		return nil
	}
	out := make([]agentapi.GPU, len(in))
	for i, g := range in {
		out[i] = agentapi.GPU{
			Index: g.Index, UUID: g.UUID, Name: clipRunes(g.Name, 128), Vendor: g.Vendor,
			UtilPercent: g.UtilPercent, MemUtilPercent: g.MemUtilPct, MemUsedMB: g.MemUsedMB, MemTotalMB: g.MemTotalMB,
			TempC: g.TempC, PowerW: g.PowerW, PowerLimitW: g.PowerLimitW, FanPercent: g.FanPercent,
			ClockMHz: g.ClockMHz, MaxClockMHz: g.MaxClockMHz,
		}
	}
	return out
}

// hardware is the report's GPU processes and sensor readings
func (a *agent) hardware(ctx context.Context, rep *agentapi.Report) {
	if a.sampler == nil {
		return
	}
	for _, p := range a.sampler.GPUProcesses(ctx) {
		rep.GPUProcesses = append(rep.GPUProcesses, agentapi.GPUProcess{GPUUUID: p.GPUUUID, GPUIndex: p.GPUIndex, PID: p.PID, Name: clipRunes(p.Name, 256), UsedMemMB: p.UsedMemMB})
	}
	for _, s := range a.sampler.Sensors() {
		rep.Sensors = append(rep.Sensors, agentapi.Sensor{Name: s.Name, Kind: s.Kind, Value: s.Value, Unit: s.Unit})
	}
}
