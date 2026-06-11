package feishuproject

import (
	"net/http"

	sdk "github.com/larksuite/project-oapi-sdk-golang"
	sdkcore "github.com/larksuite/project-oapi-sdk-golang/core"
)

// NewClient constructs the v2 SDK client used by Service. The base URL is
// fully driven by config so the same binary can target project.feishu.cn,
// project.larksuite.com, or a self-hosted proxy without code changes.
func NewClient(cfg Config) *sdk.ClientV2 {
	return sdk.NewClientV2(
		cfg.PluginID,
		cfg.PluginSecret,
		sdk.WithOpenBaseUrl(cfg.BaseURL),
		sdk.WithAccessTokenType(sdkcore.AccessTokenTypePlugin),
		sdk.WithLogReqAtDebug(false),
	)
}

// requestOptions returns the per-call options carrying the user key and the
// optional auth mode header. Centralizing this prevents drift across the
// several API calls Service makes.
func requestOptions(cfg Config) []sdkcore.RequestOptionFunc {
	options := []sdkcore.RequestOptionFunc{sdkcore.WithUserKey(cfg.UserKey)}

	if cfg.AuthMode != "" {
		headers := make(http.Header)
		headers.Set("X-Auth-Mode", cfg.AuthMode)
		options = append(options, sdkcore.WithHeaders(headers))
	}

	return options
}
