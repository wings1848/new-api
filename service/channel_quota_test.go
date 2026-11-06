package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The fixtures are the de-identified upstream samples captured under
// investigation/20261106/_evidence; no channel key or account identifier is
// part of them.
const (
	commandCodeCreditsFixture = `{"credits":{"belowThreshold":false,"creditThreshold":0,"monthlyCredits":27.7158086384,"purchasedCredits":0,"freeCredits":0},"windowLimits":{"limited":true,"exceeded":null,"fiveHour":{"used":1.4916208944,"cap":14,"exceeded":false,"resetAt":1793953400437},"weekly":{"used":13.0017519324,"cap":35,"exceeded":false,"resetAt":1794398198129}},"sandboxAccess":true,"sandboxMinutes":{"usedMinutes":0,"limitMinutes":0,"overageUsd":0,"resetAt":1796083200000}}`

	commandCodeCreditsWithoutFiveHourFixture = `{"credits":{"monthlyCredits":27.7158086384,"purchasedCredits":0,"freeCredits":0},"windowLimits":{"limited":true,"weekly":{"used":13.0017519324,"cap":35,"resetAt":1794398198129}}}`

	commandCodeCreditsSecondsResetFixture = `{"credits":{"monthlyCredits":27.7158086384,"purchasedCredits":0,"freeCredits":0},"windowLimits":{"fiveHour":{"used":1.4916208944,"cap":14,"resetAt":1793953400},"weekly":{"used":13.0017519324,"cap":35,"resetAt":1794398198129}}}`

	commandCodeSubscriptionFixture = `{"success":true,"data":{"status":"active","planId":"individual-goat","currentPeriodStart":"2026-10-11T13:36:29.000Z","currentPeriodEnd":"2026-11-11T13:36:29.000Z"}}`

	commandCodeUsageSummaryFixture = `{"totalCount":18749,"totalCost":42.1798749695,"totalTokens":3732215776,"periodBasis":"billing-period","periodStart":"2026-10-11T13:36:29.000Z"}`

	openCodeGoUsageFixture = `{"usage":{"rolling":{"status":"ok","percent":0,"resetsAt":"2026-11-06T10:18:26.661Z"},"weekly":{"status":"ok","percent":0,"resetsAt":"2026-11-09T00:00:00.000Z"},"monthly":{"status":"ok","percent":10,"resetsAt":"2026-11-23T04:15:39.000Z"}}}`

	openCodeGoUsageWithoutMonthlyFixture = `{"usage":{"rolling":{"status":"ok","percent":0,"resetsAt":"2026-11-06T10:18:26.661Z"},"weekly":{"status":"ok","percent":0,"resetsAt":"2026-11-09T00:00:00.000Z"}}}`
)

type quotaRoundTripFunc func(*http.Request) (*http.Response, error)

func (f quotaRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func newQuotaTestClient(roundTrip quotaRoundTripFunc) *http.Client {
	return &http.Client{Transport: roundTrip}
}

func quotaTestResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

func TestBuildCommandCodeQuotaWindows(t *testing.T) {
	tests := []struct {
		name             string
		creditsJSON      string
		subscriptionJSON string
		summaryJSON      string
		wantKinds        []string
		wantPercent      map[string]float64
		wantUsed         map[string]float64
		wantCap          map[string]float64
		wantResetsAt     map[string]int64
		wantWarnings     int
		wantPlan         string
		wantExtras       map[string]float64
	}{
		{
			name:             "five hour, weekly and derived monthly windows",
			creditsJSON:      commandCodeCreditsFixture,
			subscriptionJSON: commandCodeSubscriptionFixture,
			summaryJSON:      commandCodeUsageSummaryFixture,
			wantKinds:        []string{"5h", "week", "month"},
			wantPercent: map[string]float64{
				"5h":    10.65443496,
				"week":  37.147862663999994,
				"month": 60.346895247666765,
			},
			wantUsed: map[string]float64{
				"5h":    1.4916208944,
				"week":  13.0017519324,
				"month": 42.1798749695,
			},
			wantCap: map[string]float64{
				"5h":    14,
				"week":  35,
				"month": 69.8956836079,
			},
			wantResetsAt: map[string]int64{
				"5h":    1793953400437,
				"week":  1794398198129,
				"month": 1794404189000,
			},
			wantPlan: "individual-goat",
			wantExtras: map[string]float64{
				"monthlyCredits": 27.7158086384,
				"monthlySpent":   42.1798749695,
			},
		},
		{
			name:             "missing five hour window keeps the other windows",
			creditsJSON:      commandCodeCreditsWithoutFiveHourFixture,
			subscriptionJSON: commandCodeSubscriptionFixture,
			summaryJSON:      commandCodeUsageSummaryFixture,
			wantKinds:        []string{"week", "month"},
			wantWarnings:     1,
			wantPlan:         "individual-goat",
		},
		{
			name:             "resetAt in epoch seconds is widened to milliseconds",
			creditsJSON:      commandCodeCreditsSecondsResetFixture,
			subscriptionJSON: commandCodeSubscriptionFixture,
			summaryJSON:      commandCodeUsageSummaryFixture,
			wantKinds:        []string{"5h", "week", "month"},
			wantResetsAt:     map[string]int64{"5h": 1793953400000},
			wantPlan:         "individual-goat",
		},
		{
			name:         "monthly data unavailable still returns the plan windows",
			creditsJSON:  commandCodeCreditsFixture,
			wantKinds:    []string{"5h", "week"},
			wantWarnings: 0,
			wantExtras: map[string]float64{
				"monthlyCredits": 27.7158086384,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			credits, err := parseCommandCodeCredits([]byte(tt.creditsJSON))
			require.NoError(t, err)

			var subscription *commandCodeSubscription
			if tt.subscriptionJSON != "" {
				subscription, err = parseCommandCodeSubscription([]byte(tt.subscriptionJSON))
				require.NoError(t, err)
			}
			var summary *commandCodeUsageSummary
			if tt.summaryJSON != "" {
				summary, err = parseCommandCodeUsageSummary([]byte(tt.summaryJSON))
				require.NoError(t, err)
			}

			quota := buildCommandCodeQuota(credits, subscription, summary)
			kinds := make([]string, 0, len(quota.Windows))
			for _, window := range quota.Windows {
				kinds = append(kinds, window.Kind)
				if want, ok := tt.wantPercent[window.Kind]; ok {
					assert.InDelta(t, want, window.Percent, 1e-9, "%s percent", window.Kind)
				}
				if want, ok := tt.wantUsed[window.Kind]; ok {
					require.NotNil(t, window.Used, "%s used", window.Kind)
					assert.InDelta(t, want, *window.Used, 1e-9, "%s used", window.Kind)
				}
				if want, ok := tt.wantCap[window.Kind]; ok {
					require.NotNil(t, window.Cap, "%s cap", window.Kind)
					assert.InDelta(t, want, *window.Cap, 1e-9, "%s cap", window.Kind)
				}
				if want, ok := tt.wantResetsAt[window.Kind]; ok {
					require.NotNil(t, window.ResetsAt, "%s reset", window.Kind)
					assert.Equal(t, want, *window.ResetsAt, "%s reset", window.Kind)
				}
			}
			assert.Equal(t, tt.wantKinds, kinds)
			assert.Len(t, quota.Warnings, tt.wantWarnings)
			assert.Equal(t, tt.wantPlan, quota.Plan)
			for name, want := range tt.wantExtras {
				assert.InDelta(t, want, quota.Extras[name], 1e-9, "extras %s", name)
			}
		})
	}
}

func TestParseCommandCodeCreditsRejectsInvalidPayload(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantCode string
	}{
		{name: "empty body", body: "", wantCode: ChannelQuotaCodeInvalidResponse},
		{name: "non JSON Cloudflare page", body: "<html><body>blocked</body></html>", wantCode: ChannelQuotaCodeInvalidResponse},
		{name: "empty object", body: "{}", wantCode: ChannelQuotaCodeInvalidResponse},
		{name: "credits object only", body: `{"credits":{}}`, wantCode: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseCommandCodeCredits([]byte(tt.body))
			if tt.wantCode == "" {
				require.NoError(t, err)
				return
			}
			var quotaErr *ChannelQuotaError
			require.ErrorAs(t, err, &quotaErr)
			assert.Equal(t, tt.wantCode, quotaErr.Code)
		})
	}
}

func TestParseOpenCodeGoUsage(t *testing.T) {
	tests := []struct {
		name         string
		body         string
		wantKinds    []string
		wantPercent  map[string]float64
		wantResetsAt map[string]int64
		wantWarning  string
		wantCode     string
	}{
		{
			name:        "three percent-only windows",
			body:        openCodeGoUsageFixture,
			wantKinds:   []string{"5h", "week", "month"},
			wantPercent: map[string]float64{"5h": 0, "week": 0, "month": 10},
			wantResetsAt: map[string]int64{
				"5h":    1793960306661,
				"week":  1794182400000,
				"month": 1795407339000,
			},
		},
		{
			name:        "missing monthly window keeps the other windows",
			body:        openCodeGoUsageWithoutMonthlyFixture,
			wantKinds:   []string{"5h", "week"},
			wantPercent: map[string]float64{"5h": 0, "week": 0},
			wantWarning: "Monthly window is missing",
		},
		{
			name:        "percent above 100 is clamped",
			body:        `{"usage":{"rolling":{"status":"ok","percent":150,"resetsAt":"2026-11-06T10:18:26.661Z"}}}`,
			wantKinds:   []string{"5h"},
			wantPercent: map[string]float64{"5h": 100},
			wantWarning: "Weekly window is missing",
		},
		{
			name:        "invalid reset timestamp keeps the window and warns",
			body:        `{"usage":{"rolling":{"status":"ok","percent":5,"resetsAt":"not-a-time"}}}`,
			wantKinds:   []string{"5h"},
			wantPercent: map[string]float64{"5h": 5},
			wantWarning: "reset time is not a valid timestamp",
		},
		{name: "non JSON body", body: "<html>1010</html>", wantCode: ChannelQuotaCodeInvalidResponse},
		{name: "usage object missing", body: `{}`, wantCode: ChannelQuotaCodeInvalidResponse},
		{name: "all windows missing", body: `{"usage":{}}`, wantCode: ChannelQuotaCodeInvalidResponse},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			quota, err := parseOpenCodeGoUsage([]byte(tt.body))
			if tt.wantCode != "" {
				var quotaErr *ChannelQuotaError
				require.ErrorAs(t, err, &quotaErr)
				assert.Equal(t, tt.wantCode, quotaErr.Code)
				return
			}
			require.NoError(t, err)
			kinds := make([]string, 0, len(quota.Windows))
			for _, window := range quota.Windows {
				kinds = append(kinds, window.Kind)
				assert.Nil(t, window.Used, "%s must not invent an absolute amount", window.Kind)
				assert.Nil(t, window.Cap, "%s must not invent an absolute cap", window.Kind)
				if want, ok := tt.wantPercent[window.Kind]; ok {
					assert.InDelta(t, want, window.Percent, 1e-9, "%s percent", window.Kind)
				}
				if want, ok := tt.wantResetsAt[window.Kind]; ok {
					require.NotNil(t, window.ResetsAt, "%s reset", window.Kind)
					assert.Equal(t, want, *window.ResetsAt, "%s reset", window.Kind)
				}
			}
			assert.Equal(t, tt.wantKinds, kinds)
			if tt.wantWarning != "" {
				assert.Contains(t, strings.Join(quota.Warnings, "\n"), tt.wantWarning)
			}
		})
	}
}

func TestResolveChannelQuotaProvider(t *testing.T) {
	tests := []struct {
		name     string
		baseURL  string
		wantID   string
		wantCode string
	}{
		{name: "command code base URL", baseURL: "https://api.commandcode.ai/provider", wantID: commandCodeProviderID},
		{name: "opencode go base URL", baseURL: "https://opencode.ai/inference/go/openai", wantID: openCodeGoProviderID},
		{name: "host is case insensitive", baseURL: "https://API.CommandCode.AI:443/provider", wantID: commandCodeProviderID},
		{name: "unsupported host", baseURL: "https://api.openai.com", wantCode: ChannelQuotaCodeProviderUnsupported},
		{name: "invalid URL", baseURL: "://missing-scheme", wantCode: ChannelQuotaCodeProviderUnsupported},
		{name: "no base URL", baseURL: "", wantCode: ChannelQuotaCodeProviderUnsupported},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			channel := &model.Channel{}
			if tt.baseURL != "" {
				baseURL := tt.baseURL
				channel.BaseURL = &baseURL
			}
			provider, err := resolveChannelQuotaProvider(channel)
			if tt.wantCode != "" {
				var quotaErr *ChannelQuotaError
				require.ErrorAs(t, err, &quotaErr)
				assert.Equal(t, tt.wantCode, quotaErr.Code)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantID, provider.id)
		})
	}
}

func TestFetchChannelQuotaRejectsUnsupportedChannels(t *testing.T) {
	commandCodeBase := "https://api.commandcode.ai/provider"
	openAIBase := "https://api.openai.com"
	tests := []struct {
		name     string
		channel  *model.Channel
		wantCode string
	}{
		{
			name:     "multi-key channel",
			channel:  &model.Channel{Id: 1, BaseURL: &commandCodeBase, Key: "test-key", ChannelInfo: model.ChannelInfo{IsMultiKey: true}},
			wantCode: ChannelQuotaCodeMultiKeyUnsupported,
		},
		{
			name:     "missing key",
			channel:  &model.Channel{Id: 2, BaseURL: &commandCodeBase, Key: "  "},
			wantCode: ChannelQuotaCodeKeyMissing,
		},
		{
			name:     "unsupported provider",
			channel:  &model.Channel{Id: 3, BaseURL: &openAIBase, Key: "test-key"},
			wantCode: ChannelQuotaCodeProviderUnsupported,
		},
		{
			name:     "nil channel",
			channel:  nil,
			wantCode: ChannelQuotaCodeProviderUnsupported,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := FetchChannelQuota(context.Background(), tt.channel)
			var quotaErr *ChannelQuotaError
			require.ErrorAs(t, err, &quotaErr)
			assert.Equal(t, tt.wantCode, quotaErr.Code)
		})
	}
}

func TestFetchCommandCodeQuotaRequestsAndParses(t *testing.T) {
	var requestedPaths []string
	client := newQuotaTestClient(func(request *http.Request) (*http.Response, error) {
		assert.Equal(t, http.MethodGet, request.Method)
		assert.Equal(t, "Bearer test-channel-key", request.Header.Get("Authorization"))
		assert.Equal(t, channelQuotaAcceptEncoding, request.Header.Get("Accept-Encoding"))
		assert.Contains(t, request.Header.Get("User-Agent"), "Mozilla")
		assert.Equal(t, "https", request.URL.Scheme)
		assert.Equal(t, "api.commandcode.ai", request.URL.Host)
		requestedPaths = append(requestedPaths, request.URL.Path)
		switch request.URL.Path {
		case "/alpha/billing/credits":
			return quotaTestResponse(http.StatusOK, commandCodeCreditsFixture), nil
		case "/alpha/billing/subscriptions":
			return quotaTestResponse(http.StatusOK, commandCodeSubscriptionFixture), nil
		case "/alpha/usage/summary":
			assert.Equal(t, "2026-10-11T13:36:29.000Z", request.URL.Query().Get("since"))
			return quotaTestResponse(http.StatusOK, commandCodeUsageSummaryFixture), nil
		default:
			t.Fatalf("unexpected upstream path %s", request.URL.Path)
			return nil, nil
		}
	})

	quota, err := fetchCommandCodeQuota(context.Background(), client, "test-channel-key")
	require.NoError(t, err)
	assert.Equal(t, []string{"/alpha/billing/credits", "/alpha/billing/subscriptions", "/alpha/usage/summary"}, requestedPaths)
	assert.Equal(t, "individual-goat", quota.Plan)
	require.Len(t, quota.Windows, 3)
	assert.Equal(t, []string{"5h", "week", "month"}, []string{quota.Windows[0].Kind, quota.Windows[1].Kind, quota.Windows[2].Kind})

	body, err := common.Marshal(quota)
	require.NoError(t, err)
	assert.NotContains(t, string(body), "test-channel-key")
}

func TestFetchCommandCodeQuotaDegradesWhenMonthlyLookupFails(t *testing.T) {
	client := newQuotaTestClient(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/alpha/billing/credits" {
			return quotaTestResponse(http.StatusOK, commandCodeCreditsFixture), nil
		}
		return quotaTestResponse(http.StatusInternalServerError, `{"error":"boom"}`), nil
	})

	quota, err := fetchCommandCodeQuota(context.Background(), client, "test-channel-key")
	require.NoError(t, err)
	require.Len(t, quota.Windows, 2)
	assert.Equal(t, []string{"5h", "week"}, []string{quota.Windows[0].Kind, quota.Windows[1].Kind})
	require.Len(t, quota.Warnings, 1)
	assert.Contains(t, quota.Warnings[0], "billing subscription request returned HTTP 500")
}

func TestFetchOpenCodeGoQuotaRequestsAndParses(t *testing.T) {
	client := newQuotaTestClient(func(request *http.Request) (*http.Response, error) {
		assert.Equal(t, http.MethodGet, request.Method)
		assert.Equal(t, "Bearer test-channel-key", request.Header.Get("Authorization"))
		assert.Contains(t, request.Header.Get("User-Agent"), "Mozilla")
		assert.Equal(t, "/zen/go/v1/usage", request.URL.Path)
		assert.Equal(t, "opencode.ai", request.URL.Host)
		return quotaTestResponse(http.StatusOK, openCodeGoUsageFixture), nil
	})

	quota, err := fetchOpenCodeGoQuota(context.Background(), client, "test-channel-key")
	require.NoError(t, err)
	require.Len(t, quota.Windows, 3)
	assert.InDelta(t, 10, quota.Windows[2].Percent, 1e-9)
}

func TestChannelQuotaUpstreamStatusErrors(t *testing.T) {
	tests := []struct {
		name     string
		provider *channelQuotaProvider
		status   int
		body     string
		wantCode string
	}{
		{name: "command code 401", provider: commandCodeQuotaProvider, status: http.StatusUnauthorized, body: `{"success":false,"error":{"code":"UNAUTHORIZED","status":401}}`, wantCode: ChannelQuotaCodeUnauthorized},
		{name: "command code 404", provider: commandCodeQuotaProvider, status: http.StatusNotFound, body: ``, wantCode: ChannelQuotaCodeNotFound},
		{name: "command code 503", provider: commandCodeQuotaProvider, status: http.StatusServiceUnavailable, body: ``, wantCode: ChannelQuotaCodeUnavailable},
		{name: "opencode 401", provider: openCodeGoQuotaProvider, status: http.StatusUnauthorized, body: `{"type":"error","error":{"type":"AuthError","message":"Unauthorized"}}`, wantCode: ChannelQuotaCodeUnauthorized},
		{name: "opencode 403 without subscription", provider: openCodeGoQuotaProvider, status: http.StatusForbidden, body: `{"type":"error","error":{"type":"EntitlementError","message":"No subscription"}}`, wantCode: ChannelQuotaCodeNoSubscription},
		{name: "opencode 403 forbidden", provider: openCodeGoQuotaProvider, status: http.StatusForbidden, body: `{"type":"error","error":{"type":"PermissionError"}}`, wantCode: ChannelQuotaCodeForbidden},
		{name: "opencode 404", provider: openCodeGoQuotaProvider, status: http.StatusNotFound, body: ``, wantCode: ChannelQuotaCodeNotFound},
		{
			name:     "command code 200 non JSON",
			provider: commandCodeQuotaProvider,
			status:   http.StatusOK,
			body:     "<html>cloudflare challenge</html>",
			wantCode: ChannelQuotaCodeInvalidResponse,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newQuotaTestClient(func(*http.Request) (*http.Response, error) {
				return quotaTestResponse(tt.status, tt.body), nil
			})
			fetch := fetchCommandCodeQuota
			if tt.provider.id == openCodeGoProviderID {
				fetch = fetchOpenCodeGoQuota
			}
			_, err := fetch(context.Background(), client, "test-channel-key")
			var quotaErr *ChannelQuotaError
			require.ErrorAs(t, err, &quotaErr)
			assert.Equal(t, tt.wantCode, quotaErr.Code)
			assert.NotContains(t, quotaErr.Message, "test-channel-key")
		})
	}
}

func TestChannelQuotaCacheRespectsTTL(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	store := &channelQuotaCacheStore{
		entries: make(map[int]channelQuotaCacheEntry),
		now:     func() time.Time { return now },
	}
	fetches := 0
	fetch := func() (*ChannelQuota, error) {
		fetches++
		return &ChannelQuota{Provider: commandCodeProviderID}, nil
	}

	first, err := store.load(7, fetch)
	require.NoError(t, err)
	require.Equal(t, 1, fetches)

	now = now.Add(59 * time.Second)
	cached, err := store.load(7, fetch)
	require.NoError(t, err)
	assert.Same(t, first, cached)
	assert.Equal(t, 1, fetches)

	now = now.Add(2 * time.Second)
	_, err = store.load(7, fetch)
	require.NoError(t, err)
	assert.Equal(t, 2, fetches)
}

func TestChannelQuotaCacheUsesShorterFailureTTL(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	store := &channelQuotaCacheStore{
		entries: make(map[int]channelQuotaCacheEntry),
		now:     func() time.Time { return now },
	}
	failure := &ChannelQuotaError{Code: ChannelQuotaCodeUnavailable, Message: "upstream unavailable"}
	fetches := 0
	fetch := func() (*ChannelQuota, error) {
		fetches++
		return nil, failure
	}

	_, err := store.load(9, fetch)
	require.ErrorIs(t, err, failure)
	require.Equal(t, 1, fetches)

	now = now.Add(14 * time.Second)
	_, err = store.load(9, fetch)
	require.ErrorIs(t, err, failure)
	assert.Equal(t, 1, fetches)

	now = now.Add(2 * time.Second)
	_, _ = store.load(9, fetch)
	assert.Equal(t, 2, fetches)
}

func TestFetchChannelQuotaFillsMetadataAndUsesCache(t *testing.T) {
	const channelID = 4242
	baseURL := "https://quota.test/provider"
	fetches := 0
	provider := &channelQuotaProvider{
		id:      "quota-test",
		name:    "Quota Test",
		hosts:   []string{"quota.test"},
		windows: []string{"5h"},
		fetch: func(_ context.Context, _ *http.Client, key string) (*ChannelQuota, error) {
			fetches++
			assert.Equal(t, "trimmed-key", key)
			return &ChannelQuota{Windows: []QuotaWindow{{Kind: "5h", Name: "5-Hour", Percent: 5}}}, nil
		},
	}
	originalProviders := channelQuotaProviders
	channelQuotaProviders = []*channelQuotaProvider{provider}
	defer func() { channelQuotaProviders = originalProviders }()

	channelQuotaCache.mu.Lock()
	delete(channelQuotaCache.entries, channelID)
	channelQuotaCache.mu.Unlock()

	channel := &model.Channel{Id: channelID, Name: "quota-test-channel", BaseURL: &baseURL, Key: "  trimmed-key  "}
	quota, err := FetchChannelQuota(context.Background(), channel)
	require.NoError(t, err)
	assert.Equal(t, "quota-test", quota.Provider)
	assert.Equal(t, "Quota Test", quota.ProviderName)
	assert.Equal(t, channelID, quota.ChannelID)
	assert.Equal(t, "quota-test-channel", quota.ChannelName)
	assert.NotZero(t, quota.FetchedAt)

	cached, err := FetchChannelQuota(context.Background(), channel)
	require.NoError(t, err)
	assert.Same(t, quota, cached)
	assert.Equal(t, 1, fetches)
}
