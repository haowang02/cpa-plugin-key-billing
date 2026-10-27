package plugin

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"cpa-key-billing/internal/messages"
)

func (a *App) openAuthQuotaClient(callbackID string, file hostAuthFile) (*authQuotaClient, error) {
	credential, err := a.readHostAuthFile(file.AuthIndex)
	if err != nil {
		return nil, err
	}
	return newAuthQuotaClient(a.hostCaller, callbackID, credential)
}

type authQuotaClient struct {
	hostCaller HostCaller
	callbackID string
	credential map[string]any
	token      string
	client     *http.Client
	proxyURL   *url.URL
}

func newAuthQuotaClient(caller HostCaller, callbackID string, credential map[string]any) (*authQuotaClient, error) {
	if credentialUsesAPIKey(credential) {
		return nil, messages.Errorf("API key credentials do not support this quota query")
	}
	token := credentialToken(credential)
	if token == "" {
		return nil, messages.Errorf("Auth file has no usable credentials")
	}
	client := &authQuotaClient{hostCaller: caller, callbackID: callbackID, credential: credential, token: token}
	proxy := credentialString(credential, "proxy_url", "proxyUrl")
	if proxy == "" {
		return client, nil
	}
	transport := &http.Transport{
		DisableKeepAlives:   true, // Keep network work within the host call.
		TLSHandshakeTimeout: 10 * time.Second,
	}
	if !strings.EqualFold(proxy, "direct") && !strings.EqualFold(proxy, "none") {
		parsed, err := url.Parse(proxy)
		if err != nil || parsed.Hostname() == "" {
			return nil, messages.Errorf("Invalid auth file proxy")
		}
		if port := parsed.Port(); port != "" {
			number, errPort := strconv.Atoi(port)
			if errPort != nil || number < 1 || number > 65535 {
				return nil, messages.Errorf("Invalid auth file proxy")
			}
		}
		switch parsed.Scheme {
		case "http", "https", "socks5", "socks5h":
		default:
			return nil, messages.Errorf("Invalid auth file proxy")
		}
		client.proxyURL = parsed
		transport.Proxy = http.ProxyURL(parsed)
	}
	client.client = &http.Client{Transport: transport, Timeout: 30 * time.Second}
	return client, nil
}

func (c *authQuotaClient) close() {
	if c.client != nil {
		c.client.CloseIdleConnections()
	}
}

func (c *authQuotaClient) do(request hostHTTPRequest) (hostHTTPResponse, error) {
	if c.client == nil {
		response, errCall := callHostHTTP(c.hostCaller, request)
		if errCall != nil {
			return hostHTTPResponse{}, messages.Errorf("Quota request failed: %s", c.redact(errCall.Error()))
		}
		return response, nil
	}
	req, errRequest := http.NewRequest(request.Method, request.URL, bytes.NewReader(request.Body))
	if errRequest != nil {
		return hostHTTPResponse{}, messages.Errorf("Build quota request: %w", errRequest)
	}
	req.Header = request.Headers.Clone()
	response, errCall := c.client.Do(req)
	if errCall != nil {
		return hostHTTPResponse{}, messages.Errorf("Quota request failed: %s", c.redact(errCall.Error()))
	}
	defer response.Body.Close()
	body, errRead := io.ReadAll(response.Body)
	if errRead != nil {
		return hostHTTPResponse{}, messages.Errorf("Quota request failed: %s", c.redact(errRead.Error()))
	}
	return hostHTTPResponse{StatusCode: response.StatusCode, Body: body}, nil
}

func (c *authQuotaClient) redact(value string) string {
	value = redactSecret(value, c.token)
	if c.proxyURL != nil && c.proxyURL.User != nil {
		value = redactSecret(value, c.proxyURL.User.String())
		value = redactSecret(value, url.User(c.proxyURL.User.Username()).String())
		value = redactSecret(value, c.proxyURL.User.Username())
		password, _ := c.proxyURL.User.Password()
		value = redactSecret(value, strings.TrimPrefix(url.UserPassword("", password).String(), ":"))
		value = redactSecret(value, password)
	}
	return value
}

func (c *authQuotaClient) upstream(method, endpoint string, headers http.Header, body any) (map[string]any, error) {
	object, errCall := c.upstreamCall(method, endpoint, headers, body)
	if errCall != nil {
		return nil, errCall
	}
	if object == nil {
		return nil, messages.Errorf("Invalid upstream response")
	}
	return object, nil
}

// Reset responses may be empty even when successful.
func (c *authQuotaClient) upstreamCall(method, endpoint string, headers http.Header, body any) (map[string]any, error) {
	if headers == nil {
		headers = make(http.Header)
	}
	headers.Set("Authorization", "Bearer "+c.token)
	var rawBody []byte
	if body != nil {
		headers.Set("Content-Type", "application/json")
		var errMarshal error
		rawBody, errMarshal = json.Marshal(body)
		if errMarshal != nil {
			return nil, messages.Errorf("Build quota request: %w", errMarshal)
		}
	}
	response, errCall := c.do(hostHTTPRequest{
		HostCallbackID: c.callbackID, Method: method, URL: endpoint, Headers: headers, Body: rawBody,
	})
	if errCall != nil {
		return nil, errCall
	}
	var object map[string]any
	if len(response.Body) > 0 {
		_ = json.Unmarshal(response.Body, &object)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message := c.redact(upstreamErrorMessage(object))
		if response.StatusCode == http.StatusUnauthorized {
			if message != "" {
				return nil, messages.Errorf("Upstream returned HTTP %d: Credentials are invalid or expired: %s", response.StatusCode, message)
			}
			return nil, messages.Errorf("Upstream returned HTTP %d: Credentials are invalid or expired", response.StatusCode)
		}
		if message == "" {
			message = http.StatusText(response.StatusCode)
		}
		return nil, messages.Errorf("Upstream returned HTTP %d: %s", response.StatusCode, message)
	}
	return object, nil
}

func upstreamErrorMessage(object map[string]any) string {
	message := firstString(object, "message", "error_description")
	if nested := objectMap(object, "error"); nested != nil {
		message = firstNonEmptyString(firstString(nested, "message", "detail"), message)
	}
	message = strings.Join(strings.Fields(message), " ")
	if len(message) > 500 {
		message = message[:500] + "…"
	}
	return message
}

func credentialRecords(credential map[string]any) []map[string]any {
	var records []map[string]any
	var visit func(map[string]any, int)
	visit = func(record map[string]any, depth int) {
		if record == nil || depth > 2 {
			return
		}
		records = append(records, record)
		for _, key := range []string{"metadata", "attributes", "oauth", "raw", "credential", "auth", "token", "installed", "web", "user"} {
			if nested, ok := record[key].(map[string]any); ok {
				visit(nested, depth+1)
			}
		}
	}
	visit(credential, 0)
	return records
}

func credentialString(credential map[string]any, keys ...string) string {
	for _, record := range credentialRecords(credential) {
		if value := firstString(record, keys...); value != "" {
			return value
		}
	}
	return ""
}

func credentialToken(credential map[string]any) string {
	return credentialString(credential, "access_token", "accessToken", "token", "id_token", "idToken", "cookie")
}

func credentialUsesAPIKey(credential map[string]any) bool {
	if credentialString(credential, "api_key", "apiKey") != "" {
		return true
	}
	for _, record := range credentialRecords(credential) {
		if usingAPI, ok := boolValue(record, "using_api", "usingApi"); ok && usingAPI {
			return true
		}
	}
	return false
}

func redactSecret(value, secret string) string {
	if secret == "" {
		return value
	}
	return strings.ReplaceAll(value, secret, "[REDACTED]")
}
