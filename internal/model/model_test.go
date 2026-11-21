package model

import "testing"

func TestFeasibleStartsWithGuard(t *testing.T) {
	l := NewLink(0, 0, 1, 100, 10)
	// 在 [4,7) 放一个 3-slot 段，guard=1，外扩后挡住 [3,8)
	if !l.Allocate(1, 4, 3, 1) {
		t.Fatal("allocate failed")
	}
	starts := l.FeasibleStarts(2, 1)
	// 合法起点：[0,2)、[1,3)、[8,10) -> 0,1,8
	want := map[int]bool{0: true, 1: true, 8: true}
	if len(starts) != len(want) {
		t.Fatalf("got %v, want %v", starts, want)
	}
	for _, s := range starts {
		if !want[s] {
			t.Fatalf("unexpected start %d in %v", s, starts)
		}
	}
	// 4 个连续频隙已无处可放
	if got := l.FeasibleStarts(4, 1); len(got) != 0 {
		t.Fatalf("expected no feasible start for 4 slots, got %v", got)
	}
}

func TestFreeBlocksAndLargest(t *testing.T) {
	l := NewLink(0, 0, 1, 100, 20)
	l.Allocate(1, 2, 3, 0)
	l.Allocate(2, 10, 2, 0)
	blocks := l.FreeBlocks()
	// 期望空闲块：[0,2) [5,10) [12,20)
	want := []Interval{{0, 2, 0}, {5, 10, 0}, {12, 20, 0}}
	if len(blocks) != len(want) {
		t.Fatalf("blocks = %v, want %v", blocks, want)
	}
	for i, b := range blocks {
		if b.Start != want[i].Start || b.End != want[i].End {
			t.Fatalf("block %d = %v, want %v", i, b, want[i])
		}
	}
	if l.LargestFreeBlock() != 8 {
		t.Fatalf("largest = %d, want 8", l.LargestFreeBlock())
	}
	l.Release(1)
	// 释放 [2,5) 后与 [0,2) 合并为 [0,10)，最大块 10
	if l.LargestFreeBlock() != 10 {
		t.Fatalf("after release largest = %d, want 10", l.LargestFreeBlock())
	}
}

func TestCloneIsolation(t *testing.T) {
	net := NewNetwork(1)
	net.AddNode(&Node{ID: 0})
	net.AddNode(&Node{ID: 1})
	net.AddLink(NewLink(0, 0, 1, 50, 10))
	c := &Connection{ID: "x", Segments: []*Segment{{Path: []int{0, 1}, Links: []int{0}, Slots: 2, Start: 0}}}
	if !net.AllocateConnection(c) {
		t.Fatal("allocate failed")
	}
	cp := net.Clone()
	cp.ReleaseConnection("x")
	if cp.GetLink(0).UsedDataSlots() != 0 {
		t.Fatal("clone release did not take effect")
	}
	if net.GetLink(0).UsedDataSlots() != 2 {
		t.Fatal("clone operation leaked into original network")
	}
}

func TestSlotsNeededAndModulation(t *testing.T) {
	// 400G QPSK: 400/(12.5*2)=16
	if got := SlotsNeeded(400, Modulations[2]); got != 16 {
		t.Fatalf("slots = %d, want 16", got)
	}
	m, ok := BestModulation(500)
	if !ok || m.Name != "16QAM" {
		t.Fatalf("500km modulation = %v ok=%v", m, ok)
	}
	m, ok = BestModulation(1500)
	if !ok || m.Name != "QPSK" {
		t.Fatalf("1500km modulation = %v ok=%v", m, ok)
	}
	if _, ok := BestModulation(5000); ok {
		t.Fatal("5000km must require regeneration")
	}
}
