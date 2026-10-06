// Package apperr 定义业务错误码体系。
//
// 错误码按区间划分(见 docs/05-api.md 1.2):
//
//	0      成功
//	1xxxx  通用错误(参数、限流、服务器内部错误)
//	2xxxx  账号与认证
//	3xxxx  权限
//	4xxxx  OIDC
//	5xxxx  Minecraft
//
// 业务错误是**值**:可以比较、用 == 判断,不需要 errors.As 逐层解包。
// 内部错误才是 error 类型 —— 它们不该把细节泄露给调用方。
package apperr

import (
	"errors"
	"fmt"
	"net/http"
)

// Code 是业务错误码。
type Code int

// 错误码常量。数值一经发布不可更改,只能追加。
const (
	CodeOK Code = 0

	// 1xxxx 通用
	CodeInvalidArgument Code = 10001
	CodeRateLimited     Code = 10002
	CodeInternal        Code = 10003
	CodeNotFound        Code = 10004
	CodeConflict        Code = 10005
	CodePayloadTooLarge Code = 10006
	CodeUnavailable     Code = 10007

	// 2xxxx 账号与认证
	CodeUnauthorized    Code = 20001
	CodeInvalidPassword Code = 20002
	CodeAccountDisabled Code = 20003
	CodeEmailTaken      Code = 20004
	CodeUsernameTaken   Code = 20005
	CodeAccountLocked   Code = 20006
	CodeWeakPassword    Code = 20007
	CodeInvalidToken    Code = 20008
	CodeEmailUnverified Code = 20009
	CodeInviteRequired  Code = 20010
	CodeInviteInvalid   Code = 20011
	CodeSessionExpired  Code = 20012

	// 3xxxx 权限
	CodeForbidden Code = 30001
	// CodeForbidden 之上再加一层,用于「需要更高权限」的提权提示
	CodePermissionDenied Code = 30002

	// 4xxxx OIDC
	CodeOIDCInvalidRequest   Code = 40001
	CodeOIDCClientAuthFailed Code = 40002
	CodeOIDCRedirectMismatch Code = 40003
	CodeOIDCPKCEFailed       Code = 40004
	CodeOIDCScopeDenied      Code = 40005
	CodeOIDCUnsupportedGrant Code = 40006

	// 5xxxx Minecraft
	CodeMCNameInvalid    Code = 50001
	CodeMCNameTaken      Code = 50002
	CodeMCTokenInvalid   Code = 50003
	CodeMCLoginDisabled  Code = 50004
	CodeTextureInvalid   Code = 50005
	CodeTextureTooLarge  Code = 50006
	CodeMCSignatureError Code = 50007
	CodeMCReadOnly       Code = 50008
)

// codeMeta 是错误码的元数据。
type codeMeta struct {
	message    string
	httpStatus int
}

// meta 是错误码到元数据的映射。新增错误码必须同时登记在这里,
// 否则 Err() 会退化成「未知错误」,HTTP 状态也会落到 500。
var meta = map[Code]codeMeta{
	CodeOK: {message: "ok", httpStatus: http.StatusOK},

	CodeInvalidArgument: {message: "参数校验失败", httpStatus: http.StatusBadRequest},
	CodeRateLimited:     {message: "请求过于频繁", httpStatus: http.StatusTooManyRequests},
	CodeInternal:        {message: "服务器内部错误", httpStatus: http.StatusInternalServerError},
	CodeNotFound:        {message: "资源不存在", httpStatus: http.StatusNotFound},
	CodeConflict:        {message: "资源冲突", httpStatus: http.StatusConflict},
	CodePayloadTooLarge: {message: "请求体过大", httpStatus: http.StatusRequestEntityTooLarge},
	CodeUnavailable:     {message: "服务暂时不可用", httpStatus: http.StatusServiceUnavailable},

	CodeUnauthorized:    {message: "未认证或凭证无效", httpStatus: http.StatusUnauthorized},
	CodeInvalidPassword: {message: "邮箱或密码错误", httpStatus: http.StatusUnauthorized},
	CodeAccountDisabled: {message: "账号已被禁用", httpStatus: http.StatusForbidden},
	CodeEmailTaken:      {message: "邮箱已注册", httpStatus: http.StatusConflict},
	CodeUsernameTaken:   {message: "用户名已存在", httpStatus: http.StatusConflict},
	CodeAccountLocked:   {message: "登录失败次数过多,账号已锁定", httpStatus: http.StatusTooManyRequests},
	CodeWeakPassword:    {message: "密码不符合安全策略", httpStatus: http.StatusBadRequest},
	CodeInvalidToken:    {message: "令牌无效或已过期", httpStatus: http.StatusBadRequest},
	CodeEmailUnverified: {message: "邮箱尚未验证", httpStatus: http.StatusConflict},
	CodeInviteRequired:  {message: "该系统需要邀请码才能注册", httpStatus: http.StatusForbidden},
	CodeInviteInvalid:   {message: "邀请码无效或已用尽", httpStatus: http.StatusBadRequest},
	CodeSessionExpired:  {message: "登录态已过期,请重新登录", httpStatus: http.StatusUnauthorized},

	CodeForbidden:        {message: "权限不足", httpStatus: http.StatusForbidden},
	CodePermissionDenied: {message: "需要更高的权限", httpStatus: http.StatusForbidden},

	CodeOIDCInvalidRequest:   {message: "OIDC 请求参数错误", httpStatus: http.StatusBadRequest},
	CodeOIDCClientAuthFailed: {message: "OIDC 客户端认证失败", httpStatus: http.StatusUnauthorized},
	CodeOIDCRedirectMismatch: {message: "redirect_uri 不在白名单", httpStatus: http.StatusBadRequest},
	CodeOIDCPKCEFailed:       {message: "PKCE 校验失败", httpStatus: http.StatusBadRequest},
	CodeOIDCScopeDenied:      {message: "未授权该作用域", httpStatus: http.StatusBadRequest},
	CodeOIDCUnsupportedGrant: {message: "不支持的授权类型", httpStatus: http.StatusBadRequest},

	CodeMCNameInvalid:    {message: "MC 用户名格式非法", httpStatus: http.StatusBadRequest},
	CodeMCNameTaken:      {message: "MC 用户名已被占用", httpStatus: http.StatusBadRequest},
	CodeMCTokenInvalid:   {message: "MC 令牌无效", httpStatus: http.StatusUnauthorized},
	CodeMCLoginDisabled:  {message: "该账号未开放 MC 登录", httpStatus: http.StatusForbidden},
	CodeTextureInvalid:   {message: "材质格式非法", httpStatus: http.StatusBadRequest},
	CodeTextureTooLarge:  {message: "材质文件过大", httpStatus: http.StatusRequestEntityTooLarge},
	CodeMCSignatureError: {message: "MC 签名校验失败", httpStatus: http.StatusUnauthorized},
	CodeMCReadOnly:       {message: "皮肤站处于只读模式,禁止上传", httpStatus: http.StatusForbidden},
}

// Message 返回错误码的默认中文消息。
func (c Code) Message() string {
	if m, ok := meta[c]; ok {
		return m.message
	}
	return "未知错误"
}

// HTTPStatus 返回错误码对应的 HTTP 状态码。
//
// HTTP 状态码与业务 code **同时**正确设置:前端以 code 判断业务语义,
// 网关/监控以 HTTP 状态判断可用性。
func (c Code) HTTPStatus() int {
	if m, ok := meta[c]; ok {
		return m.httpStatus
	}
	return http.StatusInternalServerError
}

// Known 判断错误码是否已登记元数据。
func (c Code) Known() bool {
	_, ok := meta[c]
	return ok
}

// AllCodes 返回全部已登记的错误码,按数值升序。用于文档同步与测试。
func AllCodes() []Code {
	out := make([]Code, 0, len(meta))
	for c := range meta {
		out = append(out, c)
	}
	// 简单插入排序,避免为了排序引入排序依赖。
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// Error 是业务错误。
//
// 刻意实现成值类型而不是指针:业务错误在代码里按 == 比较很自然,
// 而内部错误(不是 apperr.Error)走另一条通道,不会被混进来。
type Error struct {
	Code Code
	// Message 覆盖默认消息。为空时用 Code.Message()。
	Message string
	// Detail 只进日志,**绝不**返回给调用方(见 docs/09-security.md 10)。
	Detail string
}

// New 用错误码构造业务错误。
func New(code Code, detail string) Error {
	return Error{Code: code, Detail: detail}
}

// Newf 用格式化详情构造业务错误。
func Newf(code Code, format string, args ...any) Error {
	return Error{Code: code, Detail: fmt.Sprintf(format, args...)}
}

// WithMessage 覆盖对外消息,用于「需要说清楚具体哪个字段不合法」的场景。
func (e Error) WithMessage(msg string) Error {
	e.Message = msg
	return e
}

// Error 实现 error 接口。
func (e Error) Error() string {
	if e.Detail == "" {
		return fmt.Sprintf("code=%d %s", e.Code, e.PublicMessage())
	}
	return fmt.Sprintf("code=%d %s: %s", e.Code, e.PublicMessage(), e.Detail)
}

// PublicMessage 返回可以安全返回给调用方的消息。
func (e Error) PublicMessage() string {
	if e.Message != "" {
		return e.Message
	}
	return e.Code.Message()
}

// HTTPStatus 返回该错误对应的 HTTP 状态码。
func (e Error) HTTPStatus() int { return e.Code.HTTPStatus() }

// From 从任意 error 提取业务错误。
//
// 非业务错误一律映射为 10003 服务器内部错误 —— 内部细节不外泄,
// 具体原因由调用方写进日志。
func From(err error) Error {
	if err == nil {
		return Error{Code: CodeOK}
	}
	var be Error
	if errors.As(err, &be) {
		return be
	}
	return Error{Code: CodeInternal, Detail: err.Error()}
}

// Is 判断错误是否为指定错误码。
func Is(err error, code Code) bool {
	var be Error
	if errors.As(err, &be) {
		return be.Code == code
	}
	return false
}

// Common 是几个最常用的构造。
var (
	// ErrInvalidArgument 泛化的参数错误。
	ErrInvalidArgument = New(CodeInvalidArgument, "")
	// ErrUnauthorized 未认证。
	ErrUnauthorized = New(CodeUnauthorized, "")
	// ErrSessionExpired 带了凭据,但登录态已经没了(登出、过期、被踢下线)。
	// 与 ErrUnauthorized 分开:用户该听到的是「请重新登录」,不是「未认证」。
	ErrSessionExpired = New(CodeSessionExpired, "")
	// ErrForbidden 权限不足。
	ErrForbidden = New(CodeForbidden, "")
	// ErrNotFound 资源不存在。
	ErrNotFound = New(CodeNotFound, "")
	// ErrRateLimited 触发限流。
	ErrRateLimited = New(CodeRateLimited, "")
	// ErrReadOnly 系统处于只读模式。
	ErrReadOnly = New(CodeUnavailable, "")
)
