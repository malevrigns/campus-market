package mysqlrepo

import (
	"database/sql"

	"qingfly/edu-mall/internal/domain"
	"qingfly/edu-mall/internal/infra/mysqlx"
)

// Stores 组合所有 MySQL 仓储。它们共享一个连接池，但各自实现独立的能力接口，
// 避免把整个数据库暴露给业务层。
type Stores struct {
	Auth    *AuthRepo
	Catalog *CatalogRepo
	Order   *OrderRepo
	Payment *PaymentRepo
	Chat    *ChatRepo
	Agent   *AgentRepo
	Flash   *FlashRepo
}

func New(db *sql.DB) *Stores {
	base := &base{db: db}
	return &Stores{
		Auth:    &AuthRepo{base: base},
		Catalog: &CatalogRepo{base: base},
		Order:   &OrderRepo{base: base},
		Payment: &PaymentRepo{base: base},
		Chat:    &ChatRepo{base: base},
		Agent:   &AgentRepo{base: base},
		Flash:   &FlashRepo{base: base},
	}
}

// base 提供连接池与通用错误映射。
type base struct {
	db *sql.DB
}

// duplicate 在事务内立即把唯一键冲突翻译成领域错误，便于业务层做幂等恢复。
func duplicate(err error) error {
	if err != nil && mysqlx.IsDuplicate(err) {
		return &domain.DuplicateError{Constraint: mysqlx.DuplicateKeyName(err), Err: err}
	}
	return err
}
