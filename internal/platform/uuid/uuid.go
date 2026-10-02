// Package uuid 封装 UUIDv4 的生成与解析。
//
// 平台层能力,与业务无关。全库主键统一用 UUIDv4(docs/03-data-model.md 开篇),
// 这里只做一层薄封装,目的是让「用什么生成 ID」这件事在全项目只有一个入口,
// 将来要换成 UUIDv7 时改一个文件即可。
package uuid

import (
	"crypto/rand"
	"fmt"

	"github.com/google/uuid"
)

// UUID 是本项目使用的 UUID 类型。
type UUID = uuid.UUID

// Nil 是空 UUID。
var Nil = uuid.Nil

// New 生成一个随机 UUIDv4。
func New() UUID { return uuid.Must(uuid.NewRandom()) }

// NewString 生成随机 UUIDv4 的字符串形式。
func NewString() string { return New().String() }

// MustParse 解析 UUID,失败时 panic。
//
// 仅用于常量、测试数据这类「解析失败就是代码写错了」的场景。
func MustParse(s string) UUID {
	v, err := uuid.Parse(s)
	if err != nil {
		panic(fmt.Sprintf("uuid: 解析 %q 失败: %v", s, err))
	}
	return v
}

// Parse 解析 UUID 字符串。
func Parse(s string) (UUID, error) { return uuid.Parse(s) }

// NewV5 按 RFC 4122 派生确定性 UUIDv5。
//
// 这是玩家档案 UUID 的生成方式:profile_uuid = uuidv5(NAMESPACE, account_id)。
// 命名空间常量一旦发布**永不可改** —— 改了所有玩家 UUID 都会变,
// 历史皮肤、别名、白名单全部失效(docs/06-mc-protocol.md 3.1)。
func NewV5(namespace UUID, name string) UUID {
	return uuid.NewSHA1(namespace, []byte(name))
}

// RandomBytes 生成 n 字节的加密安全随机数。
func RandomBytes(n int) ([]byte, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return nil, fmt.Errorf("生成随机字节失败: %w", err)
	}
	return buf, nil
}
