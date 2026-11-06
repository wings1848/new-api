package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

const (
	// ChannelQuotaCacheTTL is how long a successful upstream quota reading is
	// reused before the provider is queried again.
	ChannelQuotaCacheTTL = 60 * time.Second
	// channelQuotaFailureTTL keeps a failing upstream from being hit by every
	// admin click while still recovering quickly.
	channelQuotaFailureTTL = 15 * time.Second
	channelQuotaTimeout    = 15 * time.Second
	channelQuotaMaxBody    = 1 << 20

	// Quota endpoints sit behind CDNs that have returned a compressed body
	// without a matching Content-Encoding header. Asking for identity keeps
	// the JSON readable; the payloads are only a few hundred bytes.
	channelQuotaAcceptEncoding = "identity"
	channelQuotaUserAgent      = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36"
)

// Channel quota error codes exposed to the web console, which maps them to
// translated messages. Messages are never built from channel keys or upstream
// bodies.
const (
	ChannelQuotaCodeProviderUnsupported = "provider_unsupported"
	ChannelQuotaCodeMultiKeyUnsupported = "multi_key_unsupported"
	ChannelQuotaCodeKeyMissing          = "channel_key_missing"
	ChannelQuotaCodeUnauthorized        = "upstream_unauthorized"
	ChannelQuotaCodeForbidden           = "upstream_forbidden"
	ChannelQuotaCodeNoSubscription      = "no_subscription"
	ChannelQuotaCodeNotFound            = "upstream_not_found"
	ChannelQuotaCodeUnavailable         = "upstream_unavailable"
	ChannelQuotaCodeInvalidResponse     = "invalid_response"
)

// QuotaWindow is one usage window of a channel's upstream quota. ResetsAt is an
// absolute UTC instant in epoch milliseconds; Percent is a 0-100 ratio and Used
// / Cap are in the provider's own unit (credits for Command Code).
type QuotaWindow struct {
	Kind     string   `json:"kind"`
	Name     string   `json:"name"`
	Percent  float64  `json:"percent"`
	Used     *float64 `json:"used,omitempty"`
	Cap      *float64 `json:"cap,omitempty"`
	ResetsAt *int64   `json:"resetsAt,omitempty"`
}

// ChannelQuota is the provider-independent quota snapshot behind
// GET /api/channel/:id/quota.
type ChannelQuota struct {
	Provider     string         `json:"provider"`
	ProviderName string         `json:"providerName"`
	ChannelID    int            `json:"channelId"`
	ChannelName  string         `json:"channelName"`
	Plan         string         `json:"plan,omitempty"`
	Windows      []QuotaWindow  `json:"windows"`
	Extras       map[string]any `json:"extras,omitempty"`
	Warnings     []string       `json:"warnings,omitempty"`
	FetchedAt    int64          `json:"fetchedAt"`
}

// ChannelQuotaError carries a stable code for the web console and a readable
// English message. It never contains the channel key or an upstream body.
type ChannelQuotaError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *ChannelQuotaError) Error() string { return e.Message }

func newChannelQuotaError(code string, format string, args ...any) *ChannelQuotaError {
	return &ChannelQuotaError{Code: code, Message: fmt.Sprintf(format, args...)}
}

type channelQuotaProvider struct {
	id      string
	name    string
	hosts   []string
	windows []string
	fetch   func(ctx context.Context, client *http.Client, key string) (*ChannelQuota, error)
}

var channelQuotaProviders = []*channelQuotaProvider{
	commandCodeQuotaProvider,
	openCodeGoQuotaProvider,
}

// ChannelQuotaProviderInfo describes a provider that supports quota monitoring.
type ChannelQuotaProviderInfo struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Hosts   []string `json:"hosts"`
	Windows []string `json:"windows"`
}

// ListChannelQuotaProviders returns the providers the console may offer as
// quota-monitoring targets.
func ListChannelQuotaProviders() []ChannelQuotaProviderInfo {
	providers := make([]ChannelQuotaProviderInfo, 0, len(channelQuotaProviders))
	for _, provider := range channelQuotaProviders {
		providers = append(providers, ChannelQuotaProviderInfo{
			ID:      provider.id,
			Name:    provider.name,
			Hosts:   slices.Clone(provider.hosts),
			Windows: slices.Clone(provider.windows),
		})
	}
	return providers
}

// resolveChannelQuotaProvider picks the adapter from the channel's base URL
// host. The upstream request itself never uses that base URL: each adapter
// talks only to its own hard-coded official origin, so a channel key can never
// be sent to an arbitrary host.
func resolveChannelQuotaProvider(channel *model.Channel) (*channelQuotaProvider, error) {
	parsed, err := url.Parse(strings.TrimSpace(channel.GetBaseURL()))
	if err != nil || parsed.Hostname() == "" {
		return nil, newChannelQuotaError(ChannelQuotaCodeProviderUnsupported, "channel has no valid base URL for quota monitoring")
	}
	host := strings.ToLower(parsed.Hostname())
	for _, provider := range channelQuotaProviders {
		if slices.Contains(provider.hosts, host) {
			return provider, nil
		}
	}
	var supported []string
	for _, provider := range channelQuotaProviders {
		supported = append(supported, provider.hosts...)
	}
	return nil, newChannelQuotaError(
		ChannelQuotaCodeProviderUnsupported,
		"channel provider is not supported for quota monitoring; supported upstream hosts: %s",
		strings.Join(supported, ", "),
	)
}

// channelQuotaUpstreamStatusError maps a non-success upstream status to a
// readable error. It is a function rather than a provider method so the
// provider registries can reference their fetch functions without an
// initialization cycle.
func channelQuotaUpstreamStatusError(providerID string, providerName string, status int, body []byte) error {
	switch status {
	case http.StatusUnauthorized:
		return newChannelQuotaError(ChannelQuotaCodeUnauthorized, "%s rejected the channel key (HTTP 401); check the channel key", providerName)
	case http.StatusForbidden:
		if providerID == openCodeGoProviderID {
			var envelope openCodeErrorEnvelope
			if err := common.Unmarshal(body, &envelope); err == nil &&
				(envelope.Type == "EntitlementError" || envelope.Error.Type == "EntitlementError") {
				return newChannelQuotaError(ChannelQuotaCodeNoSubscription, "%s reports that this key has no Go subscription (HTTP 403 EntitlementError)", providerName)
			}
		}
		return newChannelQuotaError(ChannelQuotaCodeForbidden, "%s refused the quota request (HTTP 403)", providerName)
	case http.StatusNotFound:
		return newChannelQuotaError(ChannelQuotaCodeNotFound, "%s quota endpoint was not found (HTTP 404); the upstream API may have changed", providerName)
	default:
		return newChannelQuotaError(ChannelQuotaCodeUnavailable, "%s returned HTTP %d for the quota request", providerName, status)
	}
}

type channelQuotaCacheEntry struct {
	quota     *ChannelQuota
	err       error
	fetchedAt time.Time
}

type channelQuotaCacheStore struct {
	mu      sync.Mutex
	entries map[int]channelQuotaCacheEntry
	now     func() time.Time
}

var channelQuotaCache = &channelQuotaCacheStore{
	entries: make(map[int]channelQuotaCacheEntry),
	now:     time.Now,
}

func (s *channelQuotaCacheStore) load(channelID int, fetch func() (*ChannelQuota, error)) (*ChannelQuota, error) {
	s.mu.Lock()
	entry, ok := s.entries[channelID]
	s.mu.Unlock()

	if ok {
		ttl := ChannelQuotaCacheTTL
		if entry.err != nil {
			ttl = channelQuotaFailureTTL
		}
		if s.now().Sub(entry.fetchedAt) < ttl {
			return entry.quota, entry.err
		}
	}

	quota, err := fetch()

	s.mu.Lock()
	s.entries[channelID] = channelQuotaCacheEntry{quota: quota, err: err, fetchedAt: s.now()}
	s.mu.Unlock()
	return quota, err
}

// FetchChannelQuota returns the upstream quota snapshot for a single-key
// channel, reusing a cached reading for ChannelQuotaCacheTTL.
func FetchChannelQuota(ctx context.Context, channel *model.Channel) (*ChannelQuota, error) {
	if channel == nil {
		return nil, newChannelQuotaError(ChannelQuotaCodeProviderUnsupported, "channel not found")
	}
	if channel.ChannelInfo.IsMultiKey {
		return nil, newChannelQuotaError(ChannelQuotaCodeMultiKeyUnsupported, "multi-key channels are not supported for quota monitoring yet")
	}
	provider, err := resolveChannelQuotaProvider(channel)
	if err != nil {
		return nil, err
	}
	key := strings.TrimSpace(channel.Key)
	if key == "" {
		return nil, newChannelQuotaError(ChannelQuotaCodeKeyMissing, "channel key is empty")
	}

	return channelQuotaCache.load(channel.Id, func() (*ChannelQuota, error) {
		setting := channel.GetSetting()
		client, err := GetHttpClientWithProxySettings(setting.Proxy, setting)
		if err != nil {
			return nil, newChannelQuotaError(ChannelQuotaCodeUnavailable, "failed to build an HTTP client for the channel proxy")
		}
		fetchCtx, cancel := context.WithTimeout(ctx, channelQuotaTimeout)
		defer cancel()

		quota, err := provider.fetch(fetchCtx, client, key)
		if err != nil {
			return nil, err
		}
		quota.Provider = provider.id
		quota.ProviderName = provider.name
		quota.ChannelID = channel.Id
		quota.ChannelName = channel.Name
		quota.FetchedAt = time.Now().UnixMilli()
		return quota, nil
	})
}

// getChannelQuotaUpstream performs the only kind of request a quota adapter may
// send: an authenticated GET. The caller supplies a fully qualified official
// URL; adapters never append channel keys or arbitrary paths to a base URL.
func getChannelQuotaUpstream(ctx context.Context, client *http.Client, rawURL string, key string) (int, []byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Accept-Encoding", channelQuotaAcceptEncoding)
	request.Header.Set("User-Agent", channelQuotaUserAgent)

	response, err := client.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, channelQuotaMaxBody))
	if err != nil {
		return response.StatusCode, nil, err
	}
	return response.StatusCode, body, nil
}

func clampQuotaPercent(percent float64) float64 {
	return min(max(percent, 0), 100)
}

func quotaPercent(used float64, limit float64) float64 {
	if limit <= 0 {
		return 0
	}
	return clampQuotaPercent(used / limit * 100)
}

// normalizeEpochMillis widens an epoch-seconds reset timestamp to milliseconds.
// Upstream sends milliseconds today; the guard keeps a future unit change from
// rendering the reset date decades in the past.
func normalizeEpochMillis(value int64) int64 {
	if value > 0 && value < 1_000_000_000_000 {
		return value * 1000
	}
	return value
}
