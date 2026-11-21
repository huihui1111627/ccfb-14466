package topogen

import (
	"testing"

	"eonsa/internal/model"
)

func TestGenerateIsDeterministicAndBiconnected(t *testing.T) {
	cfg := DefaultConfig()
	n1 := Generate(cfg)
	n2 := Generate(cfg)
	if len(n1.Nodes) != cfg.NodeCount || len(n2.Nodes) != cfg.NodeCount {
		t.Fatalf("node count = %d/%d, want %d", len(n1.Nodes), len(n2.Nodes), cfg.NodeCount)
	}
	if len(n1.Links) != len(n2.Links) {
		t.Fatal("same seed produced different link counts")
	}
	// 无割点：删除任意节点后其余节点仍连通
	for _, node := range n1.Nodes {
		if !remainsConnected(n1, node.ID) {
			t.Fatalf("node %s is an articulation point", node.Name)
		}
	}
	// 链路属性与节点度数
	degree := map[int]int{}
	for _, l := range n1.Links {
		if l.LengthKm <= 0 {
			t.Fatalf("link %d non-positive length", l.ID)
		}
		if l.Slots != cfg.Slots {
			t.Fatalf("link %d slots = %d", l.ID, l.Slots)
		}
		degree[l.U]++
		degree[l.V]++
	}
	for id, d := range degree {
		if d < 2 {
			t.Fatalf("node %d degree = %d, want >=2", id, d)
		}
	}
}

// remainsConnected 删除 removed 节点后检查其余节点是否连通。
func remainsConnected(net *model.Network, removed int) bool {
	start := -1
	for _, n := range net.Nodes {
		if n.ID != removed {
			start = n.ID
			break
		}
	}
	if start < 0 {
		return true
	}
	seen := map[int]bool{removed: true, start: true}
	stack := []int{start}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, lid := range net.AdjLinks(cur) {
			nb := net.GetLink(lid).OtherEnd(cur)
			if !seen[nb] {
				seen[nb] = true
				stack = append(stack, nb)
			}
		}
	}
	for _, n := range net.Nodes {
		if !seen[n.ID] {
			return false
		}
	}
	return true
}

func TestDifferentSeedsDiffer(t *testing.T) {
	cfg := DefaultConfig()
	a := Generate(cfg)
	cfg.Seed = 999
	b := Generate(cfg)
	same := true
	for i := range a.Links {
		la, lb := a.Links[i], b.Links[i]
		if la.U != lb.U || la.V != lb.V {
			same = false
			break
		}
	}
	if same {
		t.Fatal("different seeds produced identical topology")
	}
}
