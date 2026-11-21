package defrag

import (
	"testing"

	"eonsa/internal/model"
)

// 构造一条链路上三段分散占用：[2,4)、[8,10)、[15,17)，guard=1
func scatteredNet(t *testing.T) *model.Network {
	net := model.NewNetwork(1)
	net.AddNode(&model.Node{ID: 0})
	net.AddNode(&model.Node{ID: 1})
	net.AddLink(model.NewLink(0, 0, 1, 100, 30))
	c1 := &model.Connection{ID: "c1", Segments: []*model.Segment{{Path: []int{0, 1}, Links: []int{0}, Slots: 2, Start: 2}}}
	c2 := &model.Connection{ID: "c2", Segments: []*model.Segment{{Path: []int{0, 1}, Links: []int{0}, Slots: 2, Start: 8}}}
	c3 := &model.Connection{ID: "c3", Segments: []*model.Segment{{Path: []int{0, 1}, Links: []int{0}, Slots: 2, Start: 15}}}
	for _, c := range []*model.Connection{c1, c2, c3} {
		if !net.AllocateConnection(c) {
			t.Fatalf("setup allocate %s failed", c.ID)
		}
	}
	return net
}

func TestDefragCompactsLeftWithoutSkipping(t *testing.T) {
	net := scatteredNet(t)
	plan := Defragment(net, DefaultConfig())
	if len(plan.Moves) == 0 {
		t.Fatal("expected moves")
	}
	// 所有移动必须向左
	for _, mv := range plan.Moves {
		if mv.To >= mv.From {
			t.Fatalf("move %d goes right: %d -> %d", mv.Seq, mv.From, mv.To)
		}
		if mv.DurationS <= 0 {
			t.Fatal("move duration must be positive")
		}
	}
	// 整理后三段必须紧贴左侧（考虑 guard=1）：起始为 0、3、6
	l := net.GetLink(0)
	iv := l.Intervals()
	wantStarts := []int{0, 3, 6}
	if len(iv) != 3 {
		t.Fatalf("interval count = %d, want 3", len(iv))
	}
	for i, x := range iv {
		if x.Start != wantStarts[i] {
			t.Fatalf("after defrag interval %d start = %d, want %d", i, x.Start, wantStarts[i])
		}
	}
	// 最大空闲块应该扩到频谱右端
	if l.LargestFreeBlock() != 30-8 { // 3*2 数据 + 2*1 保护
		t.Fatalf("largest free = %d, want %d", l.LargestFreeBlock(), 30-8)
	}
	if plan.AfterEF > plan.BeforeEF {
		t.Fatal("fragmentation must not increase after defrag")
	}
}

func TestDefragPreservesAllConnections(t *testing.T) {
	net := scatteredNet(t)
	before := map[string]bool{}
	for id, c := range net.Conns() {
		before[id] = true
		_ = c
	}
	Defragment(net, DefaultConfig())
	if len(net.Conns()) != len(before) {
		t.Fatal("connection count changed during defrag")
	}
	for id := range before {
		if _, ok := net.GetConn(id); !ok {
			t.Fatalf("connection %s lost during defrag", id)
		}
	}
	// 全程所有占用始终合法：整理后再做一次整理应无移动
	plan2 := Defragment(net, DefaultConfig())
	if len(plan2.Moves) != 0 {
		t.Fatalf("second defrag produced %d moves, expected idempotent", len(plan2.Moves))
	}
}

func TestDefragMultiLinkCommonPosition(t *testing.T) {
	// 两段链路、一条跨两段的连接：位置必须一起移动且保持一致
	net := model.NewNetwork(1)
	for i := 0; i < 3; i++ {
		net.AddNode(&model.Node{ID: i})
	}
	net.AddLink(model.NewLink(0, 0, 1, 50, 20))
	net.AddLink(model.NewLink(1, 1, 2, 50, 20))
	// 先在两条链路同位置 [5,7) 放占位段
	blocker := &model.Connection{ID: "b", Segments: []*model.Segment{{Path: []int{0, 1, 2}, Links: []int{0, 1}, Slots: 2, Start: 5}}}
	if !net.AllocateConnection(blocker) {
		t.Fatal("blocker alloc failed")
	}
	victim := &model.Connection{ID: "v", Segments: []*model.Segment{{Path: []int{0, 1, 2}, Links: []int{0, 1}, Slots: 2, Start: 10}}}
	if !net.AllocateConnection(victim) {
		t.Fatal("victim alloc failed")
	}
	Defragment(net, DefaultConfig())
	for _, lid := range []int{0, 1} {
		iv := net.GetLink(lid).Intervals()
		if iv[0].Start != 0 || iv[1].Start != 3 {
			t.Fatalf("link %d intervals = %v, want starts 0,3", lid, iv)
		}
	}
}
