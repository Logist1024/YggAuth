// Package httpx 提供统一响应包与通用 HTTP 中间件。
//
// 平台层能力,与业务无关:这里不认识账号、令牌、皮肤,
// 只认识「请求」「响应」「错误」。
//
// 响应包格式固定为 {"code":0,"message":"ok","data":{...}}(ADR-008)。
package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/yggauth/yggauth/internal/platform/apperr"
)

// Response 是统一响应包。
type Response struct {
	Code    apperr.Code `json:"code"`
	Message string      `json:"message"`
	Data    any         `json:"data"`
}

// OK 返回成功响应。
func OK(w http.ResponseWriter, data any) {
	writeJSON(w, http.StatusOK, Response{
		Code:    apperr.CodeOK,
		Message: apperr.CodeOK.Message(),
		Data:    data,
	})
}

// Created 返回 201 成功响应。
func Created(w http.ResponseWriter, data any) {
	writeJSON(w, http.StatusCreated, Response{
		Code:    apperr.CodeOK,
		Message: apperr.CodeOK.Message(),
		Data:    data,
	})
}

// NoContent 返回无响应体的成功响应(204)。
func NoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}

// Fail 按业务错误码返回错误响应。
//
// 内部错误只回泛化消息,细节进日志 —— 调用方拿不到任何栈信息
// (docs/09-security.md 第十节:错误响应不泄露内部细节)。
func Fail(w http.ResponseWriter, err error) {
	be := apperr.From(err)
	status := be.HTTPStatus()
	if status < 400 {
		status = http.StatusInternalServerError
	}

	// 非 OK 的 data 固定为 null,前端只需判断 code。
	writeJSON(w, status, Response{
		Code:    be.Code,
		Message: be.PublicMessage(),
		Data:    nil,
	})
}

// FailCode 用错误码 + 自定义消息返回错误。
func FailCode(w http.ResponseWriter, code apperr.Code, message string) {
	writeJSON(w, code.HTTPStatus(), Response{
		Code:    code,
		Message: message,
		Data:    nil,
	})
}

func writeJSON(w http.ResponseWriter, status int, body Response) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// 编码失败通常意味着连接已断开,已写出的状态码无法回退。
		// 这里只能放弃,不做二次写响应(那会造成响应体拼接)。
		return
	}
}

// ---------------------------------------------------------------- 请求绑定

// DecodeJSON 解析请求体并校验必填字段。
//
// 与 net/http 自带的 Decode 不同,这里限制请求体大小(默认 1 MiB),
// 防止超大 body 打满内存。
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	const maxBody = 1 << 20 // 1 MiB
	return DecodeJSONLimit(w, r, dst, maxBody)
}

// DecodeJSONLimit 以指定上限解析请求体。
func DecodeJSONLimit(w http.ResponseWriter, r *http.Request, dst any, maxBytes int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		return decodeError(err)
	}
	// 请求体里多出来的 JSON(比如连写两个对象)直接判错,避免「只读了一半」。
	if err := dec.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		return apperr.New(apperr.CodeInvalidArgument, "请求体只能包含一个 JSON 对象")
	}
	return nil
}

func decodeError(err error) error {
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError

	switch {
	case errors.As(err, &syntaxErr):
		return apperr.Newf(apperr.CodeInvalidArgument, "JSON 语法错误,位置 %d", syntaxErr.Offset)
	case errors.As(err, &typeErr):
		return apperr.Newf(apperr.CodeInvalidArgument, "字段 %q 类型错误", typeErr.Field)
	case errors.Is(err, io.EOF):
		return apperr.New(apperr.CodeInvalidArgument, "请求体为空")
	case errors.Is(err, io.ErrUnexpectedEOF):
		return apperr.New(apperr.CodeInvalidArgument, "请求体不完整")
	default:
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return apperr.Newf(apperr.CodePayloadTooLarge, "请求体超过上限 %d 字节", maxErr.Limit)
		}
		return apperr.Newf(apperr.CodeInvalidArgument, "请求体解析失败: %v", err)
	}
}
