package rsa

import (
	"math"

	"eonsa/internal/model"
)

// LinkFrag 单条链路的碎片指标集合。
type LinkFrag struct {
	LinkID           int
	FreeSlots        int
	LargestFreeBlock int
	// ExternalFrag 外部碎片率：1 - (最大空闲块 / 总空闲)。
	// 空闲总量充足但最大空闲块很小时该指标接近 1，正好刻画
	// “总空闲还有三成、大通道开不出来”的情形。
	ExternalFrag float64
	// Entropy 空闲块大小分布的归一化香农熵，作为辅助指标：
	// 空闲被切成越多大小相近的碎块，熵越高。
	Entropy float64
}

// MeasureLink 计算一条链路的碎片指标。
func MeasureLink(l *model.Link) LinkFrag {
	blocks := l.FreeBlocks()
	free := l.FreeSlots()
	largest := 0
	entropy := 0.0
	for _, b := range blocks {
		size := b.End - b.Start
		if size > largest {
			largest = size
		}
		if free > 0 && size > 0 {
			p := float64(size) / float64(free)
			entropy -= p * math.Log2(p)
		}
	}
	ef := 0.0
	if free > 0 {
		ef = 1 - float64(largest)/float64(free)
	}
	// 完全没有空闲时碎片率定义为 1（任何新通道都放不下）
	if free == 0 {
		ef = 1
	}
	maxEntropy := 0.0
	if len(blocks) > 1 {
		maxEntropy = math.Log2(float64(len(blocks)))
	}
	normEntropy := 0.0
	if maxEntropy > 0 {
		normEntropy = entropy / maxEntropy
	}
	return LinkFrag{
		LinkID:           l.ID,
		FreeSlots:        free,
		LargestFreeBlock: largest,
		ExternalFrag:     ef,
		Entropy:          normEntropy,
	}
}

// NetworkFrag 全网碎片统计。
type NetworkFrag struct {
	// MeanExternalFrag 按空闲量加权的平均外部碎片率。
	MeanExternalFrag float64
	// WorstEF 全网最差链路的外部碎片率。
	WorstEF float64
	// MeanEntropy 按空闲量加权的平均归一化熵。
	MeanEntropy float64
	// BlockingPotential 开不出“最大 MCS 下 n 个频隙通道”的链路占比，
	// n 取当前所有链路最大空闲块的中位数，直接反映大通道可开率。
	BlockingPotential float64
	LinkMetrics       []LinkFrag
}

// MeasureNetwork 计算全网碎片指标。
func MeasureNetwork(net *model.Network) NetworkFrag {
	out := NetworkFrag{LinkMetrics: make([]LinkFrag, 0, len(net.Links))}
	var sumFree, sumEFree, sumEntFree float64
	largestBlocks := make([]int, 0, len(net.Links))
	for _, l := range net.Links {
		m := MeasureLink(l)
		out.LinkMetrics = append(out.LinkMetrics, m)
		sumFree += float64(m.FreeSlots)
		sumEFree += m.ExternalFrag * float64(m.FreeSlots)
		sumEntFree += m.Entropy * float64(m.FreeSlots)
		if m.ExternalFrag > out.WorstEF {
			out.WorstEF = m.ExternalFrag
		}
		largestBlocks = append(largestBlocks, m.LargestFreeBlock)
	}
	if sumFree > 0 {
		out.MeanExternalFrag = sumEFree / sumFree
		out.MeanEntropy = sumEntFree / sumFree
	}
	// 中位数最大空闲块作为“典型大通道”的参照
	med := median(largestBlocks)
	blocked := 0
	for _, b := range largestBlocks {
		if b < med {
			blocked++
		}
	}
	if len(largestBlocks) > 0 {
		out.BlockingPotential = float64(blocked) / float64(len(largestBlocks))
	}
	return out
}

func median(xs []int) int {
	if len(xs) == 0 {
		return 0
	}
	cp := append([]int(nil), xs...)
	// 简单插入排序，链路规模几十条足够
	for i := 1; i < len(cp); i++ {
		for j := i; j > 0 && cp[j-1] > cp[j]; j-- {
			cp[j-1], cp[j] = cp[j], cp[j-1]
		}
	}
	mid := len(cp) / 2
	if len(cp)%2 == 1 {
		return cp[mid]
	}
	return (cp[mid-1] + cp[mid]) / 2
}
