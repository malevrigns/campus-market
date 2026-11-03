package service

import (
	"context"

	"qingfly/edu-mall/internal/port"
)

// AdminService 汇总管理员运行态视图；它只读取基础设施统计，不参与交易逻辑。
type AdminService struct {
	instance string
	nodeID   func() int64
	dbStats  func() map[string]any
	cache    port.CatalogCache
	timeouts port.TimeoutQueue
	flash    *FlashService
}

func NewAdmin(instance string, nodeID func() int64, dbStats func() map[string]any, cache port.CatalogCache, timeouts port.TimeoutQueue, flash *FlashService) *AdminService {
	return &AdminService{instance: instance, nodeID: nodeID, dbStats: dbStats, cache: cache, timeouts: timeouts, flash: flash}
}

func (s *AdminService) Runtime(ctx context.Context) (map[string]any, error) {
	ready, inflight, dead, err := s.timeouts.Stats(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"instance":      s.instance,
		"cache":         s.cache.Stats(),
		"order_timeout": map[string]int64{"ready": ready, "inflight": inflight, "dead": dead},
		"flash":         s.flash.Stats(ctx),
		"db_pool":       s.dbStats(),
	}
	if s.nodeID != nil {
		out["snowflake"] = s.nodeID()
	}
	return out, nil
}
