package minecraft

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/yggauth/yggauth/internal/platform/httpx"
)

// Handler 是 Yggdrasil 协议的 HTTP 处理器。
//
// 协议端点**不走**统一响应包:MC 客户端与 authlib-injector 期望的是
// Yggdrasil 自己的 JSON 结构。混用会让它们解析失败,表现为「点了登录之后
// 客户端毫无反应」—— 这种问题排查起来极其痛苦,因为服务端一切正常。
type Handler struct {
	svc *Service
	// issuer 是 metadata 里 signaturePublickey 之外的标识信息
	issuer string
	// skinDomain 是客户端加载材质用的域
	skinDomain string
	// publicKeyPEM 是 Yggdrasil 签名公钥的 PEM 文本
	publicKeyPEM func() string
	// trustProxy 决定取 IP 时是否信任转发头
	trustProxy bool
}

// HandlerDeps 是 Handler 的装配参数。
type HandlerDeps struct {
	Service      *Service
	Issuer       string
	SkinDomain   string
	PublicKeyPEM func() string
	TrustProxy   bool
}

// NewHandler 创建协议处理器。
func NewHandler(d HandlerDeps) *Handler {
	return &Handler{
		svc:          d.Service,
		issuer:       d.Issuer,
		skinDomain:   d.SkinDomain,
		publicKeyPEM: d.PublicKeyPEM,
		trustProxy:   d.TrustProxy,
	}
}

// Mount 把 MC 协议路由挂到 /mc。
func (h *Handler) Mount(r chi.Router) {
	r.Get("/", h.Metadata)
	r.Post("/authenticate", h.Authenticate)
	r.Post("/refresh", h.Refresh)
	r.Post("/validate", h.Validate)
	r.Post("/invalidate", h.Invalidate)
	r.Post("/signout", h.Signout)
	r.Post("/join", h.Join)
	r.Get("/hasJoined", h.HasJoined)
	r.Get("/profile/{uuid}", h.ProfileByUUID)
	r.Post("/profiles/minecraft", h.ProfilesByName)
}

// Metadata 是 authlib-injector 启动时读取的服务端元数据。
func (h *Handler) Metadata(w http.ResponseWriter, _ *http.Request) {
	payload := map[string]any{
		"meta": map[string]any{
			"serverName":            "YggAuth",
			"implementationName":    "yggauth",
			"implementationVersion": "1.0",
		},
		"skinDomains": []string{h.skinDomain},
	}
	// signaturePublickey 缺失时 authlib-injector 会拒绝启动。
	// 密钥不存在是配置问题,返回空串好过整个端点 500 ——
	// 后者会让运维以为是服务没起来。
	if h.publicKeyPEM != nil {
		payload["signaturePublickey"] = h.publicKeyPEM()
	}
	writeYggdrasil(w, http.StatusOK, payload)
}

// authenticateRequest 是 /mc/authenticate 的请求体。
type authenticateRequest struct {
	Agent struct {
		Name    string `json:"name"`
		Version int    `json:"version"`
	} `json:"agent"`
	Username    string `json:"username"`
	Password    string `json:"password"`
	ClientToken string `json:"clientToken"`
	RequestUser bool   `json:"requestUser"`
}

// Authenticate 处理首次登录。
func (h *Handler) Authenticate(w http.ResponseWriter, r *http.Request) {
	var req authenticateRequest
	if err := decodeYggdrasil(w, r, &req); err != nil {
		failYggdrasil(w, Errorf(ErrCodeInvalidRequest, "请求体无法解析"))
		return
	}

	result, err := h.svc.Authenticate(r.Context(), AuthenticateInput{
		Username:    req.Username,
		Password:    req.Password,
		ClientToken: req.ClientToken,
		RequestUser: req.RequestUser,
	})
	if err != nil {
		failYggdrasil(w, From(err))
		return
	}

	writeYggdrasil(w, http.StatusOK, map[string]any{
		"accessToken":       result.AccessToken,
		"clientToken":       result.ClientToken,
		"selectedProfile":   result.SelectedProfile,
		"availableProfiles": []ProfileView{{ID: result.SelectedProfile.ID, Name: result.SelectedProfile.Name}},
	})
}

// refreshRequest 是 /mc/refresh 的请求体。
type refreshRequest struct {
	AccessToken string `json:"accessToken"`
	ClientToken string `json:"clientToken"`
}

// Refresh 换新令牌。
func (h *Handler) Refresh(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if err := decodeYggdrasil(w, r, &req); err != nil {
		failYggdrasil(w, Errorf(ErrCodeInvalidToken, "请求体无法解析"))
		return
	}

	result, err := h.svc.Refresh(r.Context(), req.AccessToken, req.ClientToken)
	if err != nil {
		failYggdrasil(w, From(err))
		return
	}

	writeYggdrasil(w, http.StatusOK, map[string]any{
		"accessToken":     result.AccessToken,
		"clientToken":     result.ClientToken,
		"selectedProfile": result.SelectedProfile,
	})
}

// tokenRequest 是 validate / invalidate 共用的请求体。
type tokenRequest struct {
	AccessToken string `json:"accessToken"`
}

// Validate 只回一个状态码。
//
// MC 客户端只关心「有效 / 无效」,任何响应体都会被它忽略。
// 无效时返回 **403**:这是协议规定的,200 会被当成「有效」。
func (h *Handler) Validate(w http.ResponseWriter, r *http.Request) {
	var req tokenRequest
	if err := decodeYggdrasil(w, r, &req); err != nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}

	if err := h.svc.Validate(r.Context(), req.AccessToken); err != nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Invalidate 吊销令牌。
func (h *Handler) Invalidate(w http.ResponseWriter, r *http.Request) {
	var req tokenRequest
	if err := decodeYggdrasil(w, r, &req); err != nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}

	if err := h.svc.Invalidate(r.Context(), req.AccessToken); err != nil {
		failYggdrasil(w, From(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// signoutRequest 是 /mc/signout 的请求体。
type signoutRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// Signout 用用户名密码登出全部设备。
func (h *Handler) Signout(w http.ResponseWriter, r *http.Request) {
	var req signoutRequest
	if err := decodeYggdrasil(w, r, &req); err != nil {
		failYggdrasil(w, Errorf(ErrCodeInvalidCredentials, "请求体无法解析"))
		return
	}

	accountID, err := h.svc.accounts.AuthenticateForMC(r.Context(), req.Username, req.Password)
	if err != nil {
		failYggdrasil(w, From(err))
		return
	}

	profiles, err := h.svc.ProfilesByAccount(r.Context(), accountID)
	if err != nil {
		failYggdrasil(w, From(err))
		return
	}
	for _, profile := range profiles {
		if err := h.svc.Signout(r.Context(), profile.ID); err != nil {
			failYggdrasil(w, From(err))
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// joinRequest 是 /mc/join 的请求体。
type joinRequest struct {
	ServerID    string `json:"serverId"`
	AccessToken string `json:"accessToken"`
	ClientToken string `json:"clientToken"`
}

// Join 登记进服会话。
func (h *Handler) Join(w http.ResponseWriter, r *http.Request) {
	var req joinRequest
	if err := decodeYggdrasil(w, r, &req); err != nil {
		failYggdrasil(w, Errorf(ErrCodeInvalidRequest, "请求体无法解析"))
		return
	}

	// 令牌也可以走 Authorization 头 —— MC 客户端两种都试。
	token := req.AccessToken
	if token == "" {
		token = httpx.BearerToken(r)
	}

	clientToken, profile, err := h.svc.Join(r.Context(), token, req.ServerID, httpx.ClientIP(r, h.trustProxy))
	if err != nil {
		failYggdrasil(w, From(err))
		return
	}

	writeYggdrasil(w, http.StatusOK, map[string]any{
		"clientToken":     clientToken,
		"selectedProfile": profile.View(),
	})
}

// HasJoined 是进服校验。**安全核心**,详见 Service.HasJoined。
//
// 通过返回档案(200);不通过返回 **204 No Content** ——
// 这是协议规定的失败信号,authlib-injector 据此拒绝放行。
func (h *Handler) HasJoined(w http.ResponseWriter, r *http.Request) {
	view, err := h.svc.HasJoined(
		r.Context(),
		r.URL.Query().Get("username"),
		r.URL.Query().Get("serverId"),
		r.URL.Query().Get("ip"),
	)
	if err != nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	writeYggdrasil(w, http.StatusOK, map[string]any{
		"id":         view.ID,
		"name":       view.Name,
		"properties": map[string]any{},
	})
}

// ProfileByUUID 按 UUID 查档案。
func (h *Handler) ProfileByUUID(w http.ResponseWriter, r *http.Request) {
	raw := chi.URLParam(r, "uuid")

	// 协议要求令牌放请求头而不是查询串 —— 查询串会进浏览器历史、
	// 进反向代理日志、进 referer。
	profile, err := h.svc.AuthenticatedProfile(r.Context(), httpx.BearerToken(r))
	if err != nil {
		failYggdrasil(w, From(err))
		return
	}

	id, err := ParseUUID(raw)
	if err != nil {
		failYggdrasil(w, From(err))
		return
	}

	// 只能查自己:MC 协议不需要「凭一个令牌查别人」的能力。
	if profile.UUID != id {
		failYggdrasil(w, Errorf(ErrCodeInvalidRequest, "只能查询自己的档案"))
		return
	}

	writeYggdrasil(w, http.StatusOK, profile.View())
}

// profilesRequest 是 /mc/profiles/minecraft 的请求体。
type profilesRequest struct {
	Names []string `json:"names"`
}

// ProfilesByName 按名字批量查 UUID。
//
// 存在的返回视图,不存在的静默跳过 —— MYSQLMOJANG 的行为是返回能找到的
// 那部分,而不是整体报错。
func (h *Handler) ProfilesByName(w http.ResponseWriter, r *http.Request) {
	var req profilesRequest
	if err := decodeYggdrasil(w, r, &req); err != nil {
		failYggdrasil(w, Errorf(ErrCodeInvalidRequest, "请求体无法解析"))
		return
	}
	if _, err := h.svc.AuthenticatedProfile(r.Context(), httpx.BearerToken(r)); err != nil {
		failYggdrasil(w, From(err))
		return
	}

	views, err := h.svc.ProfilesByNames(r.Context(), req.Names)
	if err != nil {
		failYggdrasil(w, From(err))
		return
	}

	// MC 协议的这个字段是数组还是对象随版本而异,
	// 两个都给上才能兼容不同版本的客户端。
	writeYggdrasil(w, http.StatusOK, map[string]any{
		"profiles":  views,
		"userCount": len(views),
	})
}

// writeYggdrasil 输出 Yggdrasil 格式的响应。
func writeYggdrasil(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if status == http.StatusNoContent {
		return
	}
	_ = json.NewEncoder(w).Encode(payload)
}

// failYggdrasil 输出 Yggdrasil 格式的错误。
func failYggdrasil(w http.ResponseWriter, mcErr *MCError) {
	if mcErr == nil {
		mcErr = Errorf(ErrCodeInvalidRequest, "请求无法处理")
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(w).Encode(mcErr)
}

// decodeYggdrasil 解析请求体。
func decodeYggdrasil(w http.ResponseWriter, r *http.Request, dst any) error {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		return err
	}
	return nil
}
