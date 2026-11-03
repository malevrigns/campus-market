package domain

// FlashSale 是一场限时抢购活动。
type FlashSale struct {
	ID              string `json:"id"`
	SellerID        string `json:"seller_id"`
	SellerName      string `json:"seller_name"`
	Title           string `json:"title"`
	Description     string `json:"description"`
	Image           string `json:"image"`
	Campus          string `json:"campus"`
	OriginalPriceCt int64  `json:"original_price_ct"`
	PriceCt         int64  `json:"price_ct"`
	TotalStock      int    `json:"total_stock"`
	Stock           int    `json:"stock"`
	StartAt         int64  `json:"start_at"`
	EndAt           int64  `json:"end_at"`
	Status          string `json:"status"`
	CreatedAt       int64  `json:"created_at"`
	// Phase 由时间与状态计算：upcoming / active / ended / cancelled。
	Phase string `json:"phase"`
	// LiveStock 是 Redis 中仍可抢的数量（-1 表示未知）。
	LiveStock int `json:"live_stock"`
}

// Ticket 是用户在某场活动中的抢购资格。
type Ticket struct {
	SaleID  string `json:"sale_id"`
	OrderID string `json:"order_id,omitempty"`
	// State: queued / success / failed / cancelled / none
	State       string `json:"state"`
	Reason      string `json:"reason,omitempty"`
	OrderStatus string `json:"order_status,omitempty"`
}

// CreateFlashSaleInput 是管理员创建活动的业务输入。
type CreateFlashSaleInput struct {
	Title           string
	Description     string
	Image           string
	Campus          string
	OriginalPriceCt int64
	PriceCt         int64
	TotalStock      int
	StartAt         int64
	EndAt           int64
}

// FlashList 是活动列表响应。
type FlashList struct {
	Items      []FlashSale `json:"items"`
	ServerTime int64       `json:"server_time"`
}

// FlashDetail 是活动详情响应。
type FlashDetail struct {
	Sale       FlashSale `json:"sale"`
	ServerTime int64     `json:"server_time"`
}

// FlashOrderMessage 是 Redis Stream 中待落库的抢购订单。
type FlashOrderMessage struct {
	StreamID string
	SaleID   string
	UserID   string
	OrderID  string
}

// CreatedFlashOrder 是落库成功后回调业务层的订单信息。
type CreatedFlashOrder struct {
	OrderID   string
	SaleID    string
	BuyerID   string
	SellerID  string
	ExpiresAt int64
}

// FlashStreamStats 是管理员运行态视图。
type FlashStreamStats struct {
	Length      int64 `json:"length,omitempty"`
	DeadLetters int64 `json:"dead_letters,omitempty"`
	Pending     int64 `json:"pending,omitempty"`
	Lag         int64 `json:"lag,omitempty"`
	Consumers   int64 `json:"consumers,omitempty"`
}

// FlashPersistDecision 描述一条 Stream 消息的处理结果。
type FlashPersistDecision int

const (
	FlashAck FlashPersistDecision = iota
	FlashRetry
)
