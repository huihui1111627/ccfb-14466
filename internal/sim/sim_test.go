package sim

import (
	"testing"

	"eonsa/internal/rsa"
	"eonsa/internal/topogen"
)

func TestRunProducesMonthlyStats(t *testing.T) {
	net := DefaultNetwork()
	tcfg := TraceConfig{Months: 6, InitialPerMonth: 12, AnnualGrowth: 0.1, MeanHold: 8, Seed: 7}
	trace := GenerateTrace(net, tcfg)
	if len(trace.Events) == 0 {
		t.Fatal("empty trace")
	}
	res := Run(net.Clone(), trace, rsa.FirstFit, false, DefaultSimConfig())
	if len(res.Stats) != 6 {
		t.Fatalf("stats months = %d, want 6", len(res.Stats))
	}
	last := res.Stats[len(res.Stats)-1]
	if last.Arrivals == 0 {
		t.Fatal("no arrivals recorded")
	}
	if last.Utilization < 0 || last.Utilization > 1 {
		t.Fatalf("utilization out of range: %f", last.Utilization)
	}
	if last.BlockingRate < 0 || last.BlockingRate > 1 {
		t.Fatalf("blocking rate out of range: %f", last.BlockingRate)
	}
	if last.ExternalFrag < 0 || last.ExternalFrag > 1.0001 {
		t.Fatalf("fragmentation out of range: %f", last.ExternalFrag)
	}
}

func TestDefragScenarioDoesNotLoseConnections(t *testing.T) {
	net := DefaultNetwork()
	tcfg := TraceConfig{Months: 12, InitialPerMonth: 20, AnnualGrowth: 0.15, MeanHold: 30, Seed: 11}
	trace := GenerateTrace(net, tcfg)
	cfg := DefaultSimConfig()
	cfg.DefragThresholdEF = 0.0 // 强制每次评估都整理
	on := Run(net.Clone(), trace, rsa.FirstFit, true, cfg)
	if len(on.DefragHistory) == 0 {
		t.Fatal("expected at least one defrag run")
	}
	// 正确性约束 1：每次整理只挪动、不拆除，碎片率不高于整理前
	for _, p := range on.DefragHistory {
		if p.AfterEF > p.BeforeEF+1e-9 {
			t.Fatalf("defrag increased fragmentation: %.4f -> %.4f", p.BeforeEF, p.AfterEF)
		}
	}
	// 正确性约束 2：终态网络中每条连接的每个中继段仍在其全部链路上
	// 合法占用，且同一段在各链路上的频隙位置完全一致
	final := on.FinalNet
	active := 0
	for _, c := range final.Conns() {
		active++
		for _, seg := range c.Segments {
			for _, lid := range seg.Links {
				got, exists := final.GetLink(lid).SegmentStart(seg.ID)
				if !exists {
					t.Fatalf("segment %d of conn %s missing on link %d", seg.ID, c.ID, lid)
				}
				if got != seg.Start {
					t.Fatalf("segment %d slot mismatch on link %d: %d vs %d", seg.ID, lid, got, seg.Start)
				}
			}
		}
	}
	last := on.Stats[len(on.Stats)-1]
	if last.ActiveConns != active {
		t.Fatalf("active conn count mismatch: stat=%d actual=%d", last.ActiveConns, active)
	}
}

func TestAllScenariosShareTraceAndComplete(t *testing.T) {
	net := DefaultNetwork()
	tcfg := TraceConfig{Months: 4, InitialPerMonth: 10, AnnualGrowth: 0.1, MeanHold: 6, Seed: 3}
	trace := GenerateTrace(net, tcfg)
	results := RunAllScenarios(net, trace, DefaultSimConfig())
	if len(results) != 6 {
		t.Fatalf("scenario count = %d, want 6", len(results))
	}
	names := map[string]bool{}
	for _, r := range results {
		if names[r.Name] {
			t.Fatalf("duplicate scenario name %s", r.Name)
		}
		names[r.Name] = true
		if len(r.Stats) != 4 {
			t.Fatalf("scenario %s stats = %d months", r.Name, len(r.Stats))
		}
	}
}

func TestTraceIsTopologyIndependent(t *testing.T) {
	tcfg := TraceConfig{Months: 6, InitialPerMonth: 10, AnnualGrowth: 0.1, MeanHold: 6, Seed: 2026}
	n1 := DefaultNetwork()
	otherCfg := topogen.DefaultConfig()
	otherCfg.NodeCount = 24
	otherCfg.Seed = 555
	n2 := topogen.Generate(otherCfg)
	t1 := GenerateTrace(n1, tcfg)
	t2 := GenerateTrace(n2, tcfg)
	if len(t1.Events) != len(t2.Events) {
		t.Fatalf("trace length depends on topology: %d vs %d", len(t1.Events), len(t2.Events))
	}
	for i := range t1.Events {
		a, b := t1.Events[i], t2.Events[i]
		if a.Time != b.Time || a.IsDepart != b.IsDepart || a.Req.ID != b.Req.ID ||
			a.Req.RateGbps != b.Req.RateGbps {
			t.Fatalf("event %d differs between topologies", i)
		}
	}
}
