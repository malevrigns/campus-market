package domain

// 商品枚举是业务规则的一部分，服务层校验、接口层只做类型绑定。
var (
	Categories = []string{"数码电子", "教材书籍", "生活日用", "服饰鞋包", "运动户外", "美妆个护"}
	Campuses   = []string{"东区", "西区", "南湖校区", "未来城"}
	Conditions = []string{"全新", "九九新", "九成新", "八成新", "七成新"}
)

type Product struct {
	ID          string `json:"id"`
	SellerID    string `json:"seller_id"`
	SellerName  string `json:"seller_name"`
	Title       string `json:"title"`
	Description string `json:"description"`
	PriceCt     int64  `json:"price_ct"`
	Category    string `json:"category"`
	Campus      string `json:"campus"`
	Condition   string `json:"condition"`
	Image       string `json:"image"`
	Status      string `json:"status"`
	Demo        bool   `json:"demo"`
	CreatedAt   int64  `json:"created_at"`
}

type ProductPage struct {
	Items      []Product `json:"items"`
	Total      int       `json:"total"`
	HasMore    bool      `json:"has_more"`
	NextCursor string    `json:"next_cursor,omitempty"`
}

// CatalogQuery 是商品列表的业务查询条件，由接口层从 URL 参数解析。
type CatalogQuery struct {
	Keyword     string
	Category    string
	Campus      string
	Sort        string
	Offset      int
	CursorValue int64
	CursorID    string
	Mine        bool
	Saved       bool
}

// PublishInput 是发布商品的业务输入。
type PublishInput struct {
	Title       string
	Description string
	PriceCt     int64
	Category    string
	Campus      string
	Condition   string
	Image       string
}

// HotProduct 是热度榜条目。
type HotProduct struct {
	Product
	Score float64 `json:"score"`
}

// HeatKind 是热度事件类型：浏览 +1、收藏 +3、下单 +5。
type HeatKind int

const (
	HeatView HeatKind = iota
	HeatFavorite
	HeatOrder
)

// FavoriteState 是收藏操作的幂等结果。
type FavoriteState struct {
	Saved bool `json:"saved"`
}

// WithdrawState 是下架操作的幂等结果。
type WithdrawState struct {
	Withdrawn bool `json:"withdrawn"`
}
