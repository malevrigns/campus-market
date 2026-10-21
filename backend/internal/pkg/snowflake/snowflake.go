// Package snowflake 实现 Snowflake 风格的 64 位 ID：41 位毫秒时间戳 + 10 位节点号 + 12 位序列号。
// 订单号用它生成：趋势递增（InnoDB 聚簇索引顺序追加，避免随机主键页分裂）、不依赖数据库自增、
// 多实例不冲突（节点号通过 Redis 租约分配，见 lease.go），并天然携带下单时间。
package snowflake

import (
	"fmt"
	"sync"
	"time"
)

// Epoch 为 2020-01-01 UTC（毫秒），41 位时间戳可用约 69 年。
const Epoch int64 = 1577836800000

const (
	NodeBits uint8 = 10
	StepBits uint8 = 12
	MaxNode  int64 = 1<<NodeBits - 1

	stepMask      int64 = 1<<StepBits - 1
	maxBackwardMs int64 = 5
)

// Node 是单个节点上的发号器，并发安全。
type Node struct {
	mu     sync.Mutex
	node   int64
	lastTs int64
	step   int64
	now    func() int64
}

func NewNode(node int64) (*Node, error) {
	if node < 0 || node > MaxNode {
		return nil, fmt.Errorf("snowflake: node id %d out of range [0,%d]", node, MaxNode)
	}
	return &Node{node: node, now: func() int64 { return time.Now().UnixMilli() }}, nil
}

// Next 生成下一个 ID。
//
// 时钟回拨处理：回拨不超过 5ms 时沿用上一次的时间戳继续递增序列号（等价于"借用未来的毫秒"）；
// 回拨更多则拒绝发号，避免生成重复 ID。注意：回拨时必须把它当成"同一毫秒"处理并递增序列号，
// 否则会和上一毫秒最后一个 ID 重复。
func (n *Node) Next() (int64, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	ts := n.now()
	if ts < n.lastTs {
		if n.lastTs-ts > maxBackwardMs {
			return 0, fmt.Errorf("snowflake: clock moved backwards by %dms", n.lastTs-ts)
		}
		ts = n.lastTs
	}
	if ts == n.lastTs {
		n.step = (n.step + 1) & stepMask
		if n.step == 0 {
			// 本毫秒 4096 个序号已用完，自旋到下一毫秒。
			for ts <= n.lastTs {
				time.Sleep(100 * time.Microsecond)
				ts = n.now()
				if ts < n.lastTs && n.lastTs-ts > maxBackwardMs {
					return 0, fmt.Errorf("snowflake: clock moved backwards by %dms", n.lastTs-ts)
				}
			}
		}
	} else {
		n.step = 0
	}
	n.lastTs = ts
	return ((ts - Epoch) << (NodeBits + StepBits)) | (n.node << StepBits) | n.step, nil
}

// Time 从 ID 中解析生成时间。
func Time(id int64) time.Time {
	return time.UnixMilli((id >> (NodeBits + StepBits)) + Epoch)
}

func (n *Node) ID() int64 { return n.node }
