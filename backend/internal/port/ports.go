// Package port 声明业务层需要的能力契约（consumer-owned ports）。
//
// 接口按“业务能力”而不是“数据库表”设计：
//   - AuthStore / CatalogStore / OrderStore / ... 是持久化能力；
//   - InTx 暴露事务作用域，由业务层决定事务边界，具体 SQL 留在适配器；
//   - PaymentGateway / VerificationSender / LLMClient / MCPGateway 是外部服务能力；
//   - SessionCache / LiveNotifier / RateLimiter 等是运行时基础设施能力。
package port

import (
	"context"
	"io"
	"net/url"
	"time"

	"qingfly/edu-mall/internal/domain"
)

// ---------- 认证 ----------

type AuthStore interface {
	FindIdentity(ctx context.Context, channel, target string) (userID string, found bool, err error)
	FindCredential(ctx context.Context, channel, target string) (userID, passwordHash string, found bool, err error)
	GetUser(ctx context.Context, id string) (domain.User, error)
	GetUserIdentity(ctx context.Context, userID, channel string) (target string, verifiedAt int64, found bool, err error)
	GetUserBySession(ctx context.Context, hash string) (domain.User, int64, error)
	LastCodeAt(ctx context.Context, channel, target, purpose string) (int64, bool, error)
	CreateCode(ctx context.Context, code domain.AuthCode) error
	ConsumeCode(ctx context.Context, proof domain.CodeProof, purpose, subject, expectedHash string, at int64) error
	SetCodeState(ctx context.Context, id, state string) (bool, error)
	DeleteSession(ctx context.Context, hash string) error
	Cleanup(ctx context.Context, now, codeBefore int64) error
	BackfillEmailIdentities(ctx context.Context) error
	InTx(ctx context.Context, fn func(AuthTx) error) error
}

// AuthTx 是认证用例的事务能力。验证码比较、密码策略等业务规则仍留在服务层。
type AuthTx interface {
	FindIdentity(ctx context.Context, channel, target string) (userID string, found bool, err error)
	InsertUser(ctx context.Context, id, email, name, passwordHash string, at int64) error
	InsertIdentity(ctx context.Context, userID, channel, target string, verifiedAt int64) error
	GetUser(ctx context.Context, id string) (domain.User, error)
	GetUserPasswordForUpdate(ctx context.Context, id string) (string, error)
	GetUserIdentity(ctx context.Context, userID, channel string) (target string, verifiedAt int64, found bool, err error)
	UpsertIdentity(ctx context.Context, userID, channel, target string, verifiedAt int64) error
	UpdateUserEmail(ctx context.Context, userID, email string) error
	UpdatePassword(ctx context.Context, userID, passwordHash string) error
	UpdateIdentityVerified(ctx context.Context, channel, target string, at int64) error
	InsertSession(ctx context.Context, hash, userID string, expiresAt int64) error
	DeleteSession(ctx context.Context, hash, userID string) error
	ListUserSessions(ctx context.Context, userID string) ([]string, error)
	CountSession(ctx context.Context, hash, userID string) (int, error)
	DeleteUserSessions(ctx context.Context, userID string) error
	InvalidateUserCodes(ctx context.Context, userID string) error
}

// ---------- 商品与收藏 ----------

type CatalogStore interface {
	List(ctx context.Context, q domain.CatalogQuery, viewerID string) (domain.ProductPage, error)
	Get(ctx context.Context, id string) (domain.Product, error)
	ByIDs(ctx context.Context, ids []string, listedOnly bool) ([]domain.Product, error)
	SearchAvailable(ctx context.Context, keyword string, limit int) ([]domain.Product, error)
	Create(ctx context.Context, p domain.Product) error
	Withdraw(ctx context.Context, id, sellerID string, at int64) (bool, error)
	FavoriteIDs(ctx context.Context, userID string) ([]string, error)
	AddFavorite(ctx context.Context, userID, productID string, at int64) (bool, error)
	RemoveFavorite(ctx context.Context, userID, productID string) error
	SeedDemo(ctx context.Context, user domain.User, password string, products []domain.Product) error
}

// CatalogCache 是商品/信息流/热度榜缓存能力；key 规则留在适配器。
type CatalogCache interface {
	Product(ctx context.Context, id string, ttl time.Duration, load func(context.Context) (domain.Product, error)) (domain.Product, error)
	Feed(ctx context.Context, key string, ttl time.Duration, load func(context.Context) (domain.ProductPage, error)) (domain.ProductPage, error)
	Hot(ctx context.Context, load func(context.Context) ([]domain.HotProduct, error)) ([]domain.HotProduct, error)
	InvalidateProducts(ctx context.Context, ids ...string)
	Stats() any
}

// ScoredID 是热度榜中的商品 ID 与得分。
type ScoredID struct {
	ID    string
	Score float64
}

// RankStore 记录商品热度并生成时间衰减榜单。
type RankStore interface {
	Record(ctx context.Context, productID, visitor string, kind domain.HeatKind)
	Remove(ctx context.Context, productID string)
	TopProductIDs(ctx context.Context, limit int) ([]ScoredID, error)
}

// ---------- 订单 ----------

type OrderStore interface {
	FindByIdem(ctx context.Context, buyerID, idem string) (domain.Order, error)
	FlashOrder(ctx context.Context, saleID, buyerID string) (domain.Order, bool, error)
	List(ctx context.Context, userID string) ([]domain.Order, error)
	GetOwned(ctx context.Context, orderID, userID string) (domain.Order, error)
	Events(ctx context.Context, orderID string) ([]domain.OrderEvent, error)
	ExpiredIDs(ctx context.Context, now int64, limit int) ([]string, error)
	InTx(ctx context.Context, fn func(OrderTx) error) error
}

// OrderTx 是下单、取消、状态流转与超时关单的事务能力。
type OrderTx interface {
	LockProduct(ctx context.Context, id string) (domain.Product, error)
	ReserveProduct(ctx context.Context, id string, at int64) (bool, error)
	ReleaseProduct(ctx context.Context, id string, at int64) error
	LockOrder(ctx context.Context, id string) (domain.Order, error)
	LockOwnedOrder(ctx context.Context, id, userID, role string) (domain.Order, error)
	CountPaymentSessions(ctx context.Context, orderID string) (int, error)
	InsertOrder(ctx context.Context, o domain.Order, idem string) error
	InsertEvent(ctx context.Context, orderID, status, actor string, at int64) error
	UpdateOrderStatus(ctx context.Context, id, status string, at int64) error
	InsertRefund(ctx context.Context, orderID, reason string, at int64) error
	MarkProductSold(ctx context.Context, productID string, at int64) error
	DecrementFlashStock(ctx context.Context, saleID string, at int64) (bool, error)
	ReleaseFlashStock(ctx context.Context, saleID string, at int64) error
	FlashSaleOrderInfo(ctx context.Context, saleID string) (sellerID, title, image, campus string, priceCt int64, err error)
	OrderByFlashBuyer(ctx context.Context, saleID, buyerID string) (domain.Order, bool, error)
}

// ---------- 支付 ----------

type PaymentStore interface {
	ReconcileCandidates(ctx context.Context, now int64, limit int) ([]domain.Order, error)
	MarkChecked(ctx context.Context, orderID string, at int64) error
	InTx(ctx context.Context, fn func(PaymentTx) error) error
}

// PaymentTx 复用订单事务能力，并增加支付会话/回调表操作。
type PaymentTx interface {
	OrderTx
	PaymentTradeNo(ctx context.Context, orderID string) (tradeNo string, found bool, err error)
	UpsertPayment(ctx context.Context, orderID, checkoutURL string, at int64) error
	UpdatePaymentTradeNo(ctx context.Context, orderID, tradeNo string) error
	FindCallback(ctx context.Context, notifyID string) (domain.PaymentCallback, bool, error)
	InsertCallback(ctx context.Context, cb domain.PaymentCallback) error
}

// PaymentGateway 是签名、验签与主动查单能力；密钥只在适配器内保存。
type PaymentGateway interface {
	Enabled() bool
	Checkout(ctx context.Context, o domain.Order, mobile bool) (string, error)
	VerifyNotification(values url.Values) (domain.PaymentEvidence, error)
	Query(ctx context.Context, orderID string) (domain.GatewayOrder, error)
	Close(ctx context.Context, orderID string) (domain.GatewayOrder, error)
}

// ---------- 会话 IM ----------

type ChatStore interface {
	FindByProductBuyer(ctx context.Context, productID, buyerID string) (string, error)
	Create(ctx context.Context, id, productID, buyerID, sellerID string, at int64) error
	List(ctx context.Context, userID string) ([]domain.Conversation, error)
	Seq(ctx context.Context, conversationID, userID string) (int64, error)
	Messages(ctx context.Context, conversationID string, after int64, limit int) ([]domain.Message, error)
	MessageByClientID(ctx context.Context, conversationID, senderID, clientMsgID string) (domain.Message, error)
	MarkRead(ctx context.Context, conversationID, userID string, seq int64) error
	InTx(ctx context.Context, fn func(ChatTx) error) error
}

type ChatTx interface {
	Increment(ctx context.Context, conversationID, userID, preview string, at int64) (buyerID, sellerID string, seq int64, err error)
	InsertMessage(ctx context.Context, m domain.Message) error
}

// ---------- Agent ----------

type AgentStore interface {
	Threads(ctx context.Context, userID string) ([]domain.AgentThread, error)
	ThreadTitle(ctx context.Context, threadID, userID string) (string, error)
	ThreadAgent(ctx context.Context, threadID, userID string) (string, error)
	Runs(ctx context.Context, userID, threadID string, limit int) ([]domain.AgentRun, error)
	FindRunByIdem(ctx context.Context, userID, idem string) (domain.AgentRun, domain.AgentSnapshot, bool, error)
	RunningCount(ctx context.Context, userID string) (int, error)
	FailStaleRuns(ctx context.Context, before int64) error
	SaveRunResult(ctx context.Context, runID, answer, status, errorMessage string) error
	RecordToolEvent(ctx context.Context, runID, capabilityID, name, status string, at int64) error
	Capabilities(ctx context.Context) ([]domain.AgentCapability, error)
	Capability(ctx context.Context, id string) (domain.AgentCapability, error)
	Definitions(ctx context.Context) ([]domain.AgentDefinition, error)
	Definition(ctx context.Context, id string) (domain.AgentDefinition, error)
	PublishedSnapshots(ctx context.Context) ([]domain.AgentSnapshot, error)
	SnapshotForAgent(ctx context.Context, agentID string) (domain.AgentSnapshot, error)
	RunSnapshot(ctx context.Context, runID string) (domain.AgentSnapshot, error)
	Versions(ctx context.Context, agentID string) ([]domain.AgentVersion, error)
	Audit(ctx context.Context, limit int) ([]domain.AgentAudit, error)
	Enabled(ctx context.Context, agentID string) (enabled, archived bool, err error)
	CapabilityEnabled(ctx context.Context, id string) (bool, error)
	Seed(ctx context.Context, caps []domain.AgentCapability, def domain.AgentDefinition, snap domain.AgentSnapshot, at int64) error
	BackfillThreadBindings(ctx context.Context, agentID string) error
	InTx(ctx context.Context, fn func(AgentTx) error) error
}

type AgentTx interface {
	Capability(ctx context.Context, id string) (domain.AgentCapability, error)
	InsertCapability(ctx context.Context, c domain.AgentCapability) error
	UpdateCapability(ctx context.Context, c domain.AgentCapability, expectedRevision int) (bool, error)
	CapabilityReferenced(ctx context.Context, id string) (int, error)
	DeleteCapability(ctx context.Context, id string) (bool, error)
	InsertDefinition(ctx context.Context, d domain.AgentDefinition) error
	UpdateDefinition(ctx context.Context, d domain.AgentDefinition, expectedRevision int) (bool, error)
	Definition(ctx context.Context, id string) (domain.AgentDefinition, error)
	DefinitionForUpdate(ctx context.Context, id string) (domain.AgentDefinition, error)
	ArchiveDefinition(ctx context.Context, id string, expectedRevision int, at int64) (bool, error)
	InsertVersion(ctx context.Context, agentID string, version int, snapshot domain.AgentSnapshot, publishedBy string, at int64) error
	UpdatePublished(ctx context.Context, agentID string, draft domain.AgentDraft, publishedVersion int, at int64) error
	VersionSnapshot(ctx context.Context, agentID string, version int) (domain.AgentSnapshot, error)
	InsertAudit(ctx context.Context, actorID, action, targetID string, revision int, at int64) error
	CreateRun(ctx context.Context, userID, idem string, thread *domain.AgentThread, bindingAgent string, run domain.AgentRun, snapshot domain.AgentSnapshot) error
}

// ---------- 限时抢购 ----------

type FlashStore interface {
	Create(ctx context.Context, sale domain.FlashSale) error
	Get(ctx context.Context, id string) (domain.FlashSale, error)
	List(ctx context.Context, since int64, limit int) ([]domain.FlashSale, error)
	WarmList(ctx context.Context, since int64, limit int) ([]domain.FlashSale, error)
	ReconcileList(ctx context.Context, now int64) ([]domain.FlashSale, error)
	Cancel(ctx context.Context, id string, at int64) (bool, error)
}

// FlashCache 缓存活动元数据；实时库存始终从 Redis 单独读取。
type FlashCache interface {
	Sale(ctx context.Context, id string, ttl time.Duration, load func(context.Context) (domain.FlashSale, error)) (domain.FlashSale, error)
	List(ctx context.Context, ttl time.Duration, load func(context.Context) ([]domain.FlashSale, error)) ([]domain.FlashSale, error)
	Invalidate(ctx context.Context, id string)
}

// FlashPending 是 Stream 中待重投的消息。
type FlashPending struct {
	ID         string
	RetryCount int64
}

// FlashRedis 是抢购的 Redis 库存闸门、资格与消息流适配器。
type FlashRedis interface {
	Preheat(ctx context.Context, sale domain.FlashSale, force bool) error
	FillLiveStock(ctx context.Context, sales ...*domain.FlashSale)
	SetStatus(ctx context.Context, saleID, status string) error
	Attempt(ctx context.Context, sale domain.FlashSale, userID, orderID string) (domain.Ticket, error)
	Ticket(ctx context.Context, saleID, userID string) (domain.Ticket, bool, error)
	Transition(ctx context.Context, saleID, userID, orderID, state string, give int, fromState string) error
	Release(ctx context.Context, saleID, userID, orderID string)
	Inspect(ctx context.Context, saleID string) (redisStock, queued int, err error)
	CASStock(ctx context.Context, saleID string, oldStock, newStock int) (bool, error)
	EnsureGroup(ctx context.Context) error
	Read(ctx context.Context, consumer string, count int, block time.Duration) ([]domain.FlashOrderMessage, error)
	Claim(ctx context.Context, consumer string, minIdle time.Duration, messageID string) ([]domain.FlashOrderMessage, error)
	Pending(ctx context.Context, minIdle time.Duration, count int) ([]FlashPending, error)
	Ack(ctx context.Context, messageID string) error
	DeadLetter(ctx context.Context, msg domain.FlashOrderMessage, deliveries int64) error
	Trim(ctx context.Context) error
	Stats(ctx context.Context) map[string]any
	ClearSoldOut(saleID string)
	Consumer() string
	ClaimIdle() time.Duration
	MaxDeliveries() int64
}

// ---------- 运行时基础设施 ----------

type SessionCacheState int

const (
	SessionCacheMiss SessionCacheState = iota
	SessionCacheHit
	SessionCacheRevoked
)

type SessionCache interface {
	Get(ctx context.Context, hash string) (domain.User, int64, SessionCacheState)
	Store(ctx context.Context, hash string, user domain.User, expiresAt int64)
	Revoke(ctx context.Context, hashes ...string)
	RefreshUser(ctx context.Context, userID string)
}

type PresenceStore interface {
	Touch(ctx context.Context, userID string)
	Online(ctx context.Context, userIDs []string) map[string]bool
}

type LiveNotifier interface {
	Notify(users []string, event any)
	Subscribe(userID string) (chan []byte, bool)
	Unsubscribe(userID string, ch chan []byte)
	Listen(ctx context.Context) error
}

type TimeoutQueue interface {
	Add(ctx context.Context, id string, at time.Time) error
	Remove(ctx context.Context, id string) error
	Stats(ctx context.Context) (ready, inflight, dead int64, err error)
	Run(ctx context.Context, handler func(context.Context, string) error) error
}

// RateLimiter 返回“是否允许 + 建议等待时长”。
type RateLimiter interface {
	Window(ctx context.Context, key string, limit int, window time.Duration) (retryAfter time.Duration, allowed bool)
	Bucket(ctx context.Context, key string, rate float64, burst int) (retryAfter time.Duration, allowed bool)
}

type VerificationSender interface {
	Send(ctx context.Context, message domain.VerificationMessage) error
}

// LLMClient 以流式方式调用模型，onDelta 用于向 SSE 客户端增量输出。
type LLMClient interface {
	Stream(ctx context.Context, req domain.LLMRequest, onDelta func(string) error) (domain.LLMTurn, error)
}

// MCPGateway 负责 MCP 协议、会话与 schema 校验；guard 在真正调用外部工具前再次确认授权。
type MCPGateway interface {
	ListTools(ctx context.Context, serverID string) ([]domain.MCPTool, error)
	CallTool(ctx context.Context, serverID string, capability domain.AgentCapability, args string, guard func() error) (string, error)
}

// ConnectionRegistry 是后端可信模型/MCP 连接的运行时目录，可在不重启进程时更新。
type ConnectionRegistry interface {
	Model(key string) (domain.ModelProvider, bool)
	MCP(serverID string) (domain.MCPServer, bool)
	ModelNames() []string
	MCPServerNames() []string
	SetDefaultModel(provider domain.ModelProvider)
	SetMCP(serverID string, server domain.MCPServer)
	ClearMCP()
}

type MediaStore interface {
	SaveJPEG(ctx context.Context, data []byte) (url string, err error)
	Open(name string) (reader io.ReadSeeker, modTime time.Time, err error)
}

// OrderIDs 生成订单号（雪花 ID）；普通实体 ID 由 pkg/idgen 生成。
type OrderIDs interface {
	Next() (string, error)
}

// PaymentSettings 暴露支付是否可用，不泄露密钥。
type PaymentSettings interface {
	Enabled() bool
}

// AgentSettings 暴露 Agent 是否具备可用模型，不泄露密钥。
type AgentSettings interface {
	Available() bool
}
