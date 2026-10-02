// Package storage 是二进制对象存储的抽象。
//
// M5 只落地本地磁盘实现(ADR-007),接口本身按 S3 那种远端对象存储的
// 形状设计:Put/Get/Delete/Exists + 内容寻址的 key。加一个 S3 实现时
// 调用方不需要改代码。
//
// 这里的接口刻意**不含** List 与 Move:皮肤站的键是内容哈希,
// 天然不可枚举,后台清理走的是数据库里的引用计数而不是扫目录。
package storage

import (
	"context"

	"github.com/yggauth/yggauth/internal/platform/apperr"
)

// Storage 是二进制对象存储。
type Storage interface {
	// Put 写入对象。同名覆盖是允许的。
	Put(ctx context.Context, key string, data []byte) error
	// Get 读取对象。
	Get(ctx context.Context, key string) ([]byte, error)
	// Delete 删除对象。删除不存在的对象不算错误。
	Delete(ctx context.Context, key string) error
	// Exists 判断对象是否存在。
	Exists(ctx context.Context, key string) (bool, error)
}

// ErrNotFound 表示对象不存在。
//
// 单独一个哨兵错误而不是复用 apperr.ErrNotFound:调用方要区分
// 「这个皮肤确实没上传过」(该回源/降级)和「存储层出故障了」
// (该报 500)。两种情况处理方式完全相反。
var ErrNotFound = apperr.New(apperr.CodeNotFound, "对象不存在")

// textureKey 生成纹理对象的内容寻址键。
//
// 按哈希前 2 位分目录:单个目录里放几十万个小文件,ext4 的目录索引
// 会退化成一棵平衡树,ls 慢、inode 查找慢。分成 256 个子目录后
// 每个目录平均也就几百个文件。
func textureKey(hash string) string {
	if len(hash) < 2 {
		return "invalid/" + hash
	}
	return "textures/" + hash[:2] + "/" + hash + ".png"
}

// AvatarKey 生成头像对象键。
//
// 用 profile 的 uuid 而不是皮肤哈希:一个玩家只应有一张头像,
// 皮肤换了就覆盖同一个键。否则每次换皮肤都会留下一个永远不会被
// 回收的孤儿文件。
func AvatarKey(profileUUID string) string {
	return "avatars/" + profileUUID + ".png"
}

// TextureKey 导出纹理键构造函数。
func TextureKey(hash string) string { return textureKey(hash) }
