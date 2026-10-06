// Package rand 提供加密安全的随机值生成。
//
// 平台层能力,与业务无关。
//
// **安全要求**:会话令牌、邮箱令牌、邀请码、clientToken 这些值一旦可预测,
// 就等于把账号交出去。因此本包只提供 crypto/rand 路径,绝不退化成 math/rand。
// 需要非安全用途(比如测试里的随机分片)请直接用 math/rand,并在调用处注明。
package rand

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"fmt"
	mathrand "math/rand/v2"
	"strings"
)

// Token 熵要求:32 字节是会话令牌的最低标准(docs/security.md 3.2)。
const (
	// TokenBytes 是通用令牌字节数。
	TokenBytes = 32
	// UUIDTokenBytes 是需要 UUID 外形的令牌字节数(16 字节 → 32 位 hex)
	UUIDTokenBytes = 16
)

// Bytes 返回 n 字节的加密安全随机数。
func Bytes(n int) ([]byte, error) {
	if n <= 0 {
		return nil, fmt.Errorf("随机字节数必须为正,当前: %d", n)
	}
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return nil, fmt.Errorf("生成随机字节失败: %w", err)
	}
	return buf, nil
}

// URLSafe 返回 URL 安全的 base64 随机串(无填充)。
//
// 适合放进 URL、Cookie、JSON。
func URLSafe(nBytes int) (string, error) {
	buf, err := Bytes(nBytes)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// Token 返回 32 字节熵的会话令牌。
func Token() (string, error) { return URLSafe(TokenBytes) }

// Hex 返回指定字节数的十六进制随机串。
func Hex(nBytes int) (string, error) {
	buf, err := Bytes(nBytes)
	if err != nil {
		return "", err
	}
	const hexDigits = "0123456789abcdef"
	out := make([]byte, len(buf)*2)
	for i, b := range buf {
		out[i*2] = hexDigits[b>>4]
		out[i*2+1] = hexDigits[b&0x0f]
	}
	return string(out), nil
}

// UUIDToken 返回 UUID 外形的随机串:32 位小写 hex、无横线。
//
// MC 协议的 accessToken **必须**是这个格式 —— MC 服务端会直接解析它
// (docs/minecraft.md 2.1 约束)。
func UUIDToken() (string, error) { return Hex(UUIDTokenBytes) }

// userCodeAlphabet 排除易混淆字符(0/O、1/I/L),用户手输的码要能一眼看对。
const userCodeAlphabet = "BCDFGHJKLMNPQRSTVWXZ23456789"

// UserCode 返回适合人工抄写的短码。
//
// 分组形式 XXXX-XXXX,用于设备码流程的用户码(RFC 8628)。
func UserCode(groups, groupLen int) (string, error) {
	if groups <= 0 || groupLen <= 0 {
		return "", fmt.Errorf("用户码分组参数非法: groups=%d groupLen=%d", groups, groupLen)
	}
	total := groups * groupLen
	buf, err := Bytes(total)
	if err != nil {
		return "", err
	}
	parts := make([]string, 0, groups)
	for i := 0; i < groups; i++ {
		var sb strings.Builder
		for j := 0; j < groupLen; j++ {
			sb.WriteByte(userCodeAlphabet[int(buf[i*groupLen+j])%len(userCodeAlphabet)])
		}
		parts = append(parts, sb.String())
	}
	return strings.Join(parts, "-"), nil
}

// Base32Token 返回 RFC 4648 base32 随机串(无填充),适合需要大小写不敏感的场景。
func Base32Token(nBytes int) (string, error) {
	buf, err := Bytes(nBytes)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(base32.StdEncoding.EncodeToString(buf), "="), nil
}

// Int63n 返回 [0,n) 的随机数。
//
// **仅用于非安全场景**(如测试数据、采样)。安全相关的选择一律用前几个函数。
func Int63n(n int64) int64 { return mathrand.Int64N(n) }
