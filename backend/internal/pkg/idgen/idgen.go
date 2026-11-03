// Package idgen 生成随机不透明 ID。ID 只用于业务实体，不承担排序职责。
package idgen

import (
	"crypto/rand"
	"encoding/hex"
)

// New 返回 prefix + 32 位十六进制随机串。
func New(prefix string) string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand 失败意味着进程环境已不可信，继续运行会产生重复 ID。
		panic(err)
	}
	return prefix + hex.EncodeToString(b)
}
