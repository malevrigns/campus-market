package domain

type Order struct {
	ID          string       `json:"id"`
	BuyerID     string       `json:"buyer_id"`
	SellerID    string       `json:"seller_id"`
	ProductID   string       `json:"product_id"`
	FlashSaleID string       `json:"flash_sale_id,omitempty"`
	Title       string       `json:"title"`
	Image       string       `json:"image"`
	AmountCt    int64        `json:"amount_ct"`
	Campus      string       `json:"campus"`
	Status      string       `json:"status"`
	CreatedAt   int64        `json:"created_at"`
	UpdatedAt   int64        `json:"updated_at"`
	ExpiresAt   int64        `json:"expires_at"`
	Events      []OrderEvent `json:"events,omitempty"`
}

type OrderEvent struct {
	Status    string `json:"status"`
	CreatedAt int64  `json:"created_at"`
}

// CreateOrderInput 是创建普通订单的业务输入。金额、卖家、支付状态均不可由客户端指定。
type CreateOrderInput struct {
	ProductID string
	Campus    string
	Idem      string
}

// PaymentEvidence 是经过签名与商户校验的支付证据。
type PaymentEvidence struct {
	NotifyID  string
	OrderID   string
	TradeNo   string
	Status    string
	AmountCt  int64
	FromQuery bool
}

// GatewayOrder 是对账查询返回的订单快照。
type GatewayOrder struct {
	Code    string
	SubCode string
	OrderID string
	TradeNo string
	Status  string
	Amount  string
}

// PaymentStart 是支付入口的响应。
type PaymentStart struct {
	CheckoutURL string `json:"checkout_url"`
	OrderID     string `json:"order_id"`
	AmountCt    int64  `json:"amount_ct"`
}

// PaymentCallback 是已接受的支付回调，用于重复通知幂等。
type PaymentCallback struct {
	NotifyID  string
	OrderID   string
	TradeNo   string
	Status    string
	AmountCt  int64
	CreatedAt int64
}
