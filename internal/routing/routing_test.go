package routing

import (
	"testing"

	"eonsa/internal/model"
)

func buildNet() *model.Network {
	net := model.NewNetwork(1)
	for i := 0; i < 5; i++ {
		net.AddNode(&model.Node{ID: i})
	}
	// 线性主干 0-1-2-3-4，外加 0-4 直达与 1-3 捷径
	links := []struct {
		u, v int
		d    float64
	}{
		{0, 1, 10}, {1, 2, 10}, {2, 3, 10}, {3, 4, 10},
		{0, 4, 100}, {1, 3, 50},
	}
	for _, e := range links {
		net.AddLink(model.NewLink(0, e.u, e.v, e.d, 100))
	}
	return net
}

func TestKShortestPaths(t *testing.T) {
	net := buildNet()
	paths := KShortestPaths(net, 0, 4, 4)
	if len(paths) < 2 {
		t.Fatalf("expected >=2 paths, got %d: %v", len(paths), paths)
	}
	// 第一条必须是最短主干
	wantFirst := []int{0, 1, 2, 3, 4}
	for i, n := range wantFirst {
		if paths[0][i] != n {
			t.Fatalf("shortest path = %v, want %v", paths[0], wantFirst)
		}
	}
	// 所有路径必须是简单路径且端点正确
	seen := map[string]bool{}
	for _, p := range paths {
		if p[0] != 0 || p[len(p)-1] != 4 {
			t.Fatalf("bad endpoints: %v", p)
		}
		vis := map[int]bool{}
		for _, n := range p {
			if vis[n] {
				t.Fatalf("path contains cycle: %v", p)
			}
			vis[n] = true
		}
		key := pathKey(p)
		if seen[key] {
			t.Fatalf("duplicate path: %v", p)
		}
		seen[key] = true
		links, ok := net.PathLinks(p)
		if !ok {
			t.Fatalf("path not realizable: %v", p)
		}
		// 路径长度必须非递减
		if len(links) == 0 {
			t.Fatal("empty path")
		}
	}
	// 长度非递减
	prev := 0.0
	for _, p := range paths {
		links, _ := net.PathLinks(p)
		l := net.PathLength(links)
		if l < prev-1e-9 {
			t.Fatalf("paths not sorted by length: %v", paths)
		}
		prev = l
	}
}

func TestNoPath(t *testing.T) {
	net := model.NewNetwork(1)
	net.AddNode(&model.Node{ID: 0})
	net.AddNode(&model.Node{ID: 1})
	net.AddNode(&model.Node{ID: 2})
	net.AddLink(model.NewLink(0, 0, 1, 10, 10))
	if got := KShortestPaths(net, 0, 2, 3); len(got) != 0 {
		t.Fatalf("expected no path, got %v", got)
	}
}
