package service

import (
	"context"
	"strconv"
	"strings"
	"time"

	"qingfly/edu-mall/internal/domain"
	"qingfly/edu-mall/internal/pkg/idgen"
	"qingfly/edu-mall/internal/port"
)

type CatalogService struct {
	store   port.CatalogStore
	cache   port.CatalogCache
	rank    port.RankStore
	limiter port.RateLimiter
}

func NewCatalog(store port.CatalogStore, cache port.CatalogCache, rank port.RankStore, limiter port.RateLimiter) *CatalogService {
	return &CatalogService{store: store, cache: cache, rank: rank, limiter: limiter}
}

func (s *CatalogService) List(ctx context.Context, viewer *domain.User, q domain.CatalogQuery) (domain.ProductPage, error) {
	if len([]rune(q.Keyword)) > 100 {
		return domain.ProductPage{}, domain.Invalid("搜索词过长")
	}
	if q.Offset < 0 || q.Offset > 10000 {
		return domain.ProductPage{}, domain.Invalid("分页超出范围")
	}
	if q.CursorValue != 0 || q.CursorID != "" {
		if q.CursorID == "" {
			return domain.ProductPage{}, domain.Invalid("分页游标无效")
		}
	}
	personal := q.Mine || q.Saved
	if personal && viewer == nil {
		return domain.ProductPage{}, domain.Unauthorized("请先登录")
	}
	viewerID := ""
	if viewer != nil {
		viewerID = viewer.ID
	}
	if !personal && q.Keyword == "" && q.Category == "" && q.Campus == "" && (q.Sort == "" || q.Sort == "new") && q.Offset == 0 && q.CursorID == "" {
		return s.cache.Feed(ctx, "", 30*time.Second, func(ctx context.Context) (domain.ProductPage, error) {
			return s.store.List(ctx, q, "")
		})
	}
	return s.store.List(ctx, q, viewerID)
}

// ParseCursor 校验并拆分 keyset 游标：<排序键>_<ID>。
func ParseCursor(raw string) (int64, string, error) {
	key, id, ok := strings.Cut(raw, "_")
	if !ok || id == "" {
		return 0, "", domain.Invalid("分页游标无效")
	}
	value, err := strconv.ParseInt(key, 10, 64)
	if err != nil {
		return 0, "", domain.Invalid("分页游标无效")
	}
	return value, id, nil
}

func (s *CatalogService) Get(ctx context.Context, id, visitor string) (domain.Product, error) {
	if id == "" || len(id) > 64 {
		return domain.Product{}, domain.NotFound("内容不存在或无权访问")
	}
	p, err := s.cache.Product(ctx, id, 10*time.Minute, func(ctx context.Context) (domain.Product, error) {
		return s.store.Get(ctx, id)
	})
	if err != nil {
		return p, err
	}
	if visitor != "" {
		s.rank.Record(ctx, p.ID, visitor, domain.HeatView)
	}
	return p, nil
}

func (s *CatalogService) Publish(ctx context.Context, user *domain.User, input domain.PublishInput) (domain.Product, error) {
	input.Title = strings.TrimSpace(input.Title)
	input.Description = strings.TrimSpace(input.Description)
	if !validImageURL(input.Image) || !validText(input.Title, 2, 80) || len([]rune(input.Description)) > 3000 || input.PriceCt < 1 || input.PriceCt > 10000000 || !contains(domain.Categories, input.Category) || !contains(domain.Campuses, input.Campus) || !contains(domain.Conditions, input.Condition) {
		return domain.Product{}, domain.Invalid("请检查标题、金额、分类、校区、成色和 HTTPS 图片地址")
	}
	if retry, allowed := s.limiter.Window(ctx, "publish:"+user.ID, 60, time.Hour); !allowed {
		return domain.Product{}, domain.Retryable(domain.KindTooMany, "发布太频繁，请稍后再试", int(retry.Seconds()+0.999))
	}
	p := domain.Product{ID: idgen.New("p_"), SellerID: user.ID, SellerName: user.Name, Title: input.Title, Description: input.Description, PriceCt: input.PriceCt, Category: input.Category, Campus: input.Campus, Condition: input.Condition, Image: input.Image, Status: "available", CreatedAt: now()}
	if err := s.store.Create(ctx, p); err != nil {
		return domain.Product{}, err
	}
	s.cache.InvalidateProducts(ctx, p.ID)
	return p, nil
}

func (s *CatalogService) Withdraw(ctx context.Context, user *domain.User, productID string) (domain.WithdrawState, error) {
	ok, err := s.store.Withdraw(ctx, productID, user.ID, now())
	if err != nil {
		return domain.WithdrawState{}, err
	}
	if !ok {
		return domain.WithdrawState{}, domain.Conflict("只能下架本人未被预订的商品")
	}
	s.cache.InvalidateProducts(ctx, productID)
	s.rank.Remove(ctx, productID)
	return domain.WithdrawState{Withdrawn: true}, nil
}

func (s *CatalogService) Favorites(ctx context.Context, user *domain.User) ([]string, error) {
	return s.store.FavoriteIDs(ctx, user.ID)
}

func (s *CatalogService) AddFavorite(ctx context.Context, user *domain.User, productID string) (domain.FavoriteState, error) {
	if _, err := s.store.Get(ctx, productID); err != nil {
		return domain.FavoriteState{}, err
	}
	added, err := s.store.AddFavorite(ctx, user.ID, productID, now())
	if err != nil {
		return domain.FavoriteState{}, err
	}
	if added {
		s.rank.Record(ctx, productID, "", domain.HeatFavorite)
	}
	return domain.FavoriteState{Saved: true}, nil
}

func (s *CatalogService) RemoveFavorite(ctx context.Context, user *domain.User, productID string) (domain.FavoriteState, error) {
	if err := s.store.RemoveFavorite(ctx, user.ID, productID); err != nil {
		return domain.FavoriteState{}, err
	}
	return domain.FavoriteState{Saved: false}, nil
}

func (s *CatalogService) Hot(ctx context.Context, limit int) ([]domain.HotProduct, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	items, err := s.cache.Hot(ctx, s.buildHotList)
	if err != nil {
		return nil, err
	}
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (s *CatalogService) buildHotList(ctx context.Context) ([]domain.HotProduct, error) {
	scored, err := s.rank.TopProductIDs(ctx, 80)
	if err != nil {
		return nil, err
	}
	items := []domain.HotProduct{}
	if len(scored) == 0 {
		return items, nil
	}
	ids := make([]string, len(scored))
	for i, z := range scored {
		ids[i] = z.ID
	}
	products, err := s.store.ByIDs(ctx, ids, true)
	if err != nil {
		return nil, err
	}
	byID := map[string]domain.Product{}
	for _, p := range products {
		byID[p.ID] = p
	}
	for _, z := range scored {
		if p, ok := byID[z.ID]; ok {
			items = append(items, domain.HotProduct{Product: p, Score: z.Score})
		}
		if len(items) == 50 {
			break
		}
	}
	return items, nil
}

func (s *CatalogService) SearchForAgent(ctx context.Context, keyword string, limit int) ([]domain.Product, error) {
	if len([]rune(keyword)) > 300 {
		return nil, domain.Invalid("搜索词过长")
	}
	return s.store.SearchAvailable(ctx, keyword, limit)
}

// SeedDemo 只在显式开启时写入展示数据；展示商品不能交易或联系卖家。
func (s *CatalogService) SeedDemo(ctx context.Context) error {
	user := domain.User{ID: "demo_seller", Email: "showcase@invalid.example", Name: "集市编辑部"}
	seeds := []struct {
		title, description, category, campus, condition, image string
		price                                                  int64
	}{
		{"无线头戴式降噪耳机", "安静一点，听见自己。轻巧机身，日常听歌与自习。展示图仅作风格参考。", "数码电子", "东区", "九成新", "photo-1546435770-a3e426bf472b", 98000},
		{"复古胶片相机", "把校园生活拍成电影。轻巧机身，适合日常记录。展示图仅作风格参考。", "数码电子", "南湖校区", "九成新", "photo-1516035069371-29a1b244cc32", 268000},
		{"一整个周末的好书", "从设计到生活，给阅读留一些时间。展示图仅作风格参考。", "教材书籍", "西区", "八成新", "photo-1495446815901-a7297e633e8d", 4500},
		{"轻盈日常慢跑鞋", "下一段旅途，从轻盈的脚步开始。展示图仅作风格参考。", "服饰鞋包", "未来城", "九九新", "photo-1542291026-7eec264c27ff", 26000},
		{"Keychron 无线机械键盘", "每一次敲击都刚刚好。清爽桌搭，适合自习与创作。展示图仅作风格参考。", "数码电子", "东区", "九成新", "photo-1587829741301-dc798b83add3", 18000},
		{"暖光台灯 · 宿舍的角落", "小小光源，让每个夜晚都温柔一些。展示图仅作风格参考。", "生活日用", "西区", "九成新", "photo-1507473885765-e6ed057f782c", 6500},
		{"周末骑行 · 城市单车", "下课后，沿着湖边骑一圈。展示图仅作风格参考。", "运动户外", "南湖校区", "八成新", "photo-1485965120184-e220f721d03e", 48000},
		{"日常通勤双肩包", "装下电脑、课本和你的奇思妙想。展示图仅作风格参考。", "服饰鞋包", "东区", "九九新", "photo-1553062407-98eeb64c6a62", 8900},
	}
	products := make([]domain.Product, 0, len(seeds))
	for i, seed := range seeds {
		t := domain.NowMillis() - int64(i)*60000
		products = append(products, domain.Product{ID: "demo_" + strconv.Itoa(i+1), SellerID: user.ID, SellerName: user.Name, Title: seed.title, Description: seed.description, PriceCt: seed.price, Category: seed.category, Campus: seed.campus, Condition: seed.condition, Image: "https://images.unsplash.com/" + seed.image + "?auto=format&fit=crop&w=900&q=85", Status: "available", Demo: true, CreatedAt: t})
	}
	if err := s.store.SeedDemo(ctx, user, "disabled", products); err != nil {
		return err
	}
	s.cache.InvalidateProducts(context.Background())
	return nil
}

func contains(values []string, v string) bool {
	for _, candidate := range values {
		if candidate == v {
			return true
		}
	}
	return false
}

// InvalidateProducts 供订单/支付服务在事务提交后同步商品缓存。
func (s *CatalogService) InvalidateProducts(ctx context.Context, ids ...string) {
	s.cache.InvalidateProducts(ctx, ids...)
}

// RecordHeat 供订单服务记录下单热度。
func (s *CatalogService) RecordHeat(ctx context.Context, productID string, kind domain.HeatKind) {
	s.rank.Record(ctx, productID, "", kind)
}
