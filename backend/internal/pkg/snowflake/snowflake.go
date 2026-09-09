// Package snowflake implements a Twitter-Snowflake style unique ID generator,
// used for order numbers and message IDs. Thread-safe, monotonic, trend-increasing;
// avoids auto-increment / UUID ordering & collision problems.
package snowflake

import (
	"fmt"
	"sync"
	"time"
)

// epoch: 2020-01-01 UTC in milliseconds (custom epoch).
const epoch int64 = 1577836800000

const (
	nodeBits uint8 = 10 // worker id: up to 1024 instances
	stepBits uint8 = 12 // sequence: up to 4096 per millisecond
)

const (
	nodeShift     uint8 = stepBits
	stepMask      int64 = -1 ^ (-1 << stepBits)
	maxBackwardMs int64 = 5 // tolerate small clock skew
)

// Node is one snowflake generator instance (one worker/service node).
type Node struct {
	mu     sync.Mutex
	node   int64
	lastTs int64
	step   int64
}

// NewNode creates a generator for the given node id (0..1023).
func NewNode(node int64) *Node {
	if node < 0 || node >= (1<<nodeBits) {
		panic(fmt.Sprintf("snowflake: node id %d out of range [0,%d)", node, 1<<nodeBits))
	}
	return &Node{node: node}
}

// Next generates the next ID. Small clock skew is tolerated; large skew errors out.
func (n *Node) Next() (int64, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	ts := time.Now().UnixMilli()
	if ts < n.lastTs {
		if delta := n.lastTs - ts; delta > maxBackwardMs {
			return 0, fmt.Errorf("snowflake: clock moved backwards by %dms", delta)
		}
		ts = n.lastTs
	} else if ts == n.lastTs {
		n.step = (n.step + 1) & stepMask
		if n.step == 0 {
			for ts <= n.lastTs {
				ts = time.Now().UnixMilli()
			}
		}
	} else {
		n.step = 0
	}
	n.lastTs = ts
	return ((ts - epoch) << nodeShift) | (n.node << stepBits) | n.step, nil
}
