package openai

import (
	"net/url"
	"strings"
)

// resolveProviderMediaResult 补全相对地址，并只对供应商同源地址启用鉴权。
func (c *Client) resolveProviderMediaResult(result MediaResult) (MediaResult, error) {
	if strings.TrimSpace(result.URL) == "" {
		return result, nil
	}
	resolved, authenticated, err := c.resolveProviderMediaURL(result.URL)
	if err != nil {
		return MediaResult{}, err
	}
	result.URL = resolved
	result.AuthenticatedURL = authenticated
	return result, nil
}

func (c *Client) resolveProviderMediaURL(value string) (string, bool, error) {
	if c.baseURL == nil {
		return "", false, &APIError{Code: "invalid_media_url", Message: "媒体地址缺少供应商 Base URL"}
	}
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return "", false, &APIError{Code: "invalid_media_url", Message: "供应商返回了无效媒体地址", Detail: err.Error()}
	}
	resolved := parsed
	if parsed.Scheme == "" {
		base := *c.baseURL
		base.Path = strings.TrimRight(base.Path, "/") + "/"
		base.RawPath = ""
		resolved = base.ResolveReference(parsed)
	}
	if (resolved.Scheme != "http" && resolved.Scheme != "https") || resolved.Host == "" {
		return "", false, &APIError{Code: "invalid_media_url", Message: "供应商媒体地址仅支持 http 或 https", Detail: value}
	}
	authenticated := sameProviderOrigin(c.baseURL, resolved)
	return resolved.String(), authenticated, nil
}

func sameProviderOrigin(left, right *url.URL) bool {
	if left == nil || right == nil {
		return false
	}
	return strings.EqualFold(left.Scheme, right.Scheme) &&
		strings.EqualFold(left.Hostname(), right.Hostname()) &&
		providerOriginPort(left) == providerOriginPort(right)
}

func providerOriginPort(value *url.URL) string {
	if port := value.Port(); port != "" {
		return port
	}
	switch strings.ToLower(value.Scheme) {
	case "http":
		return "80"
	case "https":
		return "443"
	default:
		return ""
	}
}
