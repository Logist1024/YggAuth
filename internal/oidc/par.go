package oidc

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/google/uuid"
	"github.com/ory/fosite"

	"github.com/yggauth/yggauth/internal/platform/apperr"
	"github.com/yggauth/yggauth/internal/platform/db"
	"github.com/yggauth/yggauth/internal/platform/db/query"
)

// Pushed Authorization Request(RFC 9126)的存储实现。
//
// 客户端先把授权请求推到授权服务器,拿到一个短生命周期的 request_uri,
// 再用它去 /oauth/authorize。收益有两个:授权参数不再出现在浏览器地址栏、
// 也不会进入 referer 泄漏面;客户端无法在授权端点偷偷夹带参数。

// CreatePARSession 保存推送的授权请求。
func (s *Storage) CreatePARSession(ctx context.Context, requestURI string, request fosite.AuthorizeRequester) error {
	// 用显式 DTO,不用 json.Marshal(request):fosite.Request 里有接口字段,
	// 反序列化时必然失败。
	requestJSON, err := marshalStoredRequest(toStoredRequest(request))
	if err != nil {
		return err
	}

	// 未登录时 PAR 允许不关联账号 —— 用户随后才去登录。
	accountID, err := accountUUID(request.GetSession())
	if err != nil {
		accountID = uuid.Nil
	}

	ttl := 90 * time.Second
	_, err = s.queries.CreatePAR(ctx, query.CreatePARParams{
		RequestUriHash: hashCode(requestURI),
		ClientID:       request.GetClient().GetID(),
		AccountID:      pgUUID(accountID),
		Request:        requestJSON,
		ExpiresAt:      s.clock().Add(ttl),
	})
	return err
}

// GetPARSession 取出推送的授权请求。
func (s *Storage) GetPARSession(ctx context.Context, requestURI string) (fosite.AuthorizeRequester, error) {
	row, err := s.queries.GetPAR(ctx, hashCode(requestURI))
	if err != nil {
		if db.IsNoRows(err) {
			return nil, fosite.ErrInvalidRequest.WithHint("The PAR request_uri is unknown or expired.")
		}
		return nil, err
	}

	stored, err := unmarshalStoredRequest(row.Request)
	if err != nil {
		return nil, err
	}

	// 客户端必须按 ID 重新查库:fosite.Client 是接口,存不进 JSON
	client, err := s.loadClient(ctx, stored.ClientID)
	if err != nil {
		return nil, fosite.ErrInvalidRequest.WithHint("The PAR session refers to an unknown client.")
	}

	// 还原成一个真实的 GET 授权请求,让 fosite 重新校验一遍。
	// 在这里手工拼请求等于自己维护一份校验逻辑,迟早会漏项。
	form := url.Values{}
	for k, vs := range stored.Form {
		for _, v := range vs {
			form.Add(k, v)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "/oauth/authorize?"+form.Encode(), nil)
	if err != nil {
		return nil, apperr.Newf(apperr.CodeInternal, "构造 PAR 请求失败: %v", err)
	}

	return s.loadClient2(ctx, req, client)
}

// loadClient2 用给定客户端解析一个授权请求。
func (s *Storage) loadClient2(ctx context.Context, req *http.Request, client fosite.Client) (fosite.AuthorizeRequester, error) {
	ar, err := s.provider.NewAuthorizeRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	// 用库里查到的客户端覆盖 fosite 自己加载的结果,保证与授权码流程一致
	if setter, ok := ar.(interface{ SetClient(fosite.Client) }); ok {
		setter.SetClient(client)
	}
	return ar, nil
}

// DeletePARSession 作废推送的授权请求。
//
// PAR 的 request_uri 是一次性的:用掉即作废,否则同一个 URI 能在
// 有效期内反复发起授权。
func (s *Storage) DeletePARSession(ctx context.Context, requestURI string) error {
	_, err := s.queries.ConsumePAR(ctx, hashCode(requestURI))
	return err
}
