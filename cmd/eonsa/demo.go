package main

import (
	"eonsa/internal/model"
)

// demoNetwork 构造一个 4 节点线性演示网络并手工制造明显碎片：
// 每条链路 40 个频隙，放置若干位置交错、长短不一的连接，
// 使空闲总量尚多但最大连续块很小，便于演示整理效果。
func demoNetwork() *model.Network {
	net := model.NewNetwork(1)
	for i := 0; i < 4; i++ {
		net.AddNode(&model.Node{ID: i, Name: demoName(i), X: float64(i) * 200, Y: 100})
	}
	net.AddLink(model.NewLink(0, 0, 1, 120, 40))
	net.AddLink(model.NewLink(1, 1, 2, 120, 40))
	net.AddLink(model.NewLink(2, 2, 3, 120, 40))

	// 跨全程的连接（三段链路同位置），起始位置刻意错开
	full := []struct {
		id    string
		start int
		slots int
	}{
		{"D1", 3, 2}, {"D2", 9, 3}, {"D3", 17, 2}, {"D4", 26, 4}, {"D5", 35, 2},
	}
	for _, c := range full {
		conn := &model.Connection{
			ID: c.id, Src: 0, Dst: 3, RateGbps: 100,
			Segments: []*model.Segment{{
				Path: []int{0, 1, 2, 3}, Links: []int{0, 1, 2},
				Slots: c.slots, Start: c.start,
				Mod: model.Modulations[0],
			}},
		}
		if !net.AllocateConnection(conn) {
			panic("demo setup failed: " + c.id)
		}
	}
	// 只占局部链路的短连接，进一步切碎空闲块
	partials := []struct {
		id           string
		linkIDs      []int
		path         []int
		start, slots int
	}{
		{"P1", []int{0}, []int{0, 1}, 13, 2},
		{"P2", []int{1}, []int{1, 2}, 22, 2},
		{"P3", []int{2}, []int{2, 3}, 6, 2},
		{"P4", []int{0, 1}, []int{0, 1, 2}, 31, 2},
	}
	for _, c := range partials {
		conn := &model.Connection{
			ID: c.id, RateGbps: 100,
			Segments: []*model.Segment{{
				Path: c.path, Links: c.linkIDs,
				Slots: c.slots, Start: c.start,
				Mod: model.Modulations[0],
			}},
		}
		if !net.AllocateConnection(conn) {
			panic("demo setup failed: " + c.id)
		}
	}
	return net
}

func demoName(i int) string {
	names := []string{"城东", "枢纽A", "枢纽B", "城西"}
	if i < len(names) {
		return names[i]
	}
	return "N" + string(rune('0'+i))
}
