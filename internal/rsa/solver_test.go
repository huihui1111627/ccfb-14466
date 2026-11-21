package rsa

import (
	"testing"

	"eonsa/internal/model"
)

func smallNet() *model.Network {
	net := model.NewNetwork(1)
	for i := 0; i < 4; i++ {
		net.AddNode(&model.Node{ID: i})
	}
	// 0-1-2-3 主干，0-3 直达备用
	net.AddLink(model.NewLink(0, 0, 1, 100, 20))
	net.AddLink(model.NewLink(1, 1, 2, 100, 20))
	net.AddLink(model.NewLink(2, 2, 3, 100, 20))
	net.AddLink(model.NewLink(3, 0, 3, 400, 20))
	return net
}

func TestSolveFirstFitConsistency(t *testing.T) {
	net := smallNet()
	s := NewSolver(FirstFit)
	r := s.Solve(net, Request{ID: "a", Src: 0, Dst: 3, RateGbps: 100})
	if !r.Success {
		t.Fatalf("solve failed: %s", r.Reason)
	}
	// 全程频隙位置必须一致
	start := r.Conn.Segments[0].Start
	for _, seg := range r.Conn.Segments {
		if seg.Start != start {
			t.Fatalf("slot position changed along path: %d vs %d", seg.Start, start)
		}
		for _, lid := range seg.Links {
			if net.GetLink(lid) == nil {
				t.Fatal("missing link")
			}
		}
	}
	// 首次适配应从 0 号频隙开始
	if start != 0 {
		t.Fatalf("first-fit start = %d, want 0", start)
	}
}

func TestPoliciesDifferAndLastFitHigh(t *testing.T) {
	for _, p := range []PlacementPolicy{FirstFit, LastFit, FragAware} {
		net := smallNet()
		s := NewSolver(p)
		r := s.Solve(net, Request{ID: "a", Src: 0, Dst: 3, RateGbps: 100})
		if !r.Success {
			t.Fatalf("%s solve failed: %s", p, r.Reason)
		}
		slots := r.Conn.Segments[0].Slots
		switch p {
		case FirstFit:
			if r.Conn.Segments[0].Start != 0 {
				t.Fatalf("first-fit start = %d, want 0", r.Conn.Segments[0].Start)
			}
		case LastFit:
			if want := 20 - slots; r.Conn.Segments[0].Start != want {
				t.Fatalf("last-fit start = %d, want %d", r.Conn.Segments[0].Start, want)
			}
		}
	}
}

func TestBlockingReportsFailLinks(t *testing.T) {
	net := smallNet()
	// 把全部四条链路占满（含备用直达路径），保证请求必然阻塞
	for lid := 0; lid < 4; lid++ {
		l := net.GetLink(lid)
		for s := 0; s+4 <= l.Slots; s += 5 {
			if !l.Allocate(1000+lid*100+s, s, 4, 1) {
				t.Fatalf("fill link %d slot %d failed", lid, s)
			}
		}
	}
	s := NewSolver(FirstFit)
	r := s.Solve(net, Request{ID: "b", Src: 0, Dst: 3, RateGbps: 400})
	if r.Success {
		t.Fatal("expected blocking")
	}
	if len(r.FailLinks) == 0 {
		t.Fatal("blocking result must report bottleneck links")
	}
}

func TestRegenerationOnLongPath(t *testing.T) {
	net := model.NewNetwork(1)
	for i := 0; i < 5; i++ {
		net.AddNode(&model.Node{ID: i})
	}
	// 每段 1500km，4 段共 6000km，BPSK 极限 4000km，至少 1 个中继
	for i := 0; i < 4; i++ {
		net.AddLink(model.NewLink(i, i, i+1, 1500, 64))
	}
	s := NewSolver(FirstFit)
	r := s.Solve(net, Request{ID: "long", Src: 0, Dst: 4, RateGbps: 200})
	if !r.Success {
		t.Fatalf("long path solve failed: %s", r.Reason)
	}
	if r.Conn.RegenCount() < 1 {
		t.Fatalf("expected regeneration, got %d segments", len(r.Conn.Segments))
	}
	// 中继段之间允许换频谱位置，但每段内部必须一致
	for _, seg := range r.Conn.Segments {
		for _, lid := range seg.Links {
			iv := net.GetLink(lid).Intervals()
			found := false
			for _, x := range iv {
				if x.SegID == seg.ID {
					found = true
				}
			}
			if !found {
				t.Fatalf("segment %d missing on link %d", seg.ID, lid)
			}
		}
	}
}

func TestFragmentMetricReflectsScattering(t *testing.T) {
	l := model.NewLink(0, 0, 1, 1, 40)
	// 交替占用：每 2 个 slot 占 1 个，空闲总量 20（50%）但最大块仅 1
	for s := 1; s < 40; s += 2 {
		if !l.Allocate(s+100, s, 1, 0) {
			t.Fatal("fill failed")
		}
	}
	m := MeasureLink(l)
	if m.FreeSlots < 20 {
		t.Fatalf("free = %d, want >=20", m.FreeSlots)
	}
	if m.LargestFreeBlock != 1 {
		t.Fatalf("largest block = %d, want 1", m.LargestFreeBlock)
	}
	if m.ExternalFrag < 0.9 {
		t.Fatalf("external fragmentation = %.3f, want >=0.9", m.ExternalFrag)
	}
}
