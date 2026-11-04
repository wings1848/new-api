package service

import (
	"sort"
	"time"

	"github.com/QuantumNous/new-api/model"
)

// Window-dimension usage report (issue #31): consume logs are bucketed into the
// same fixed 5h/7d windows used for quota limits, so operators can see spend per
// window instead of per calendar day.

// UsageWindowRow is one (window, key) aggregate.
type UsageWindowRow struct {
	Key              string `json:"key"`
	WindowStart      int64  `json:"window_start"`
	WindowEnd        int64  `json:"window_end"`
	Quota            int64  `json:"quota"`
	Requests         int64  `json:"requests"`
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
}

// UsageWindowReport is the admin report payload.
type UsageWindowReport struct {
	Window        string           `json:"window"`
	GroupBy       string           `json:"group_by"`
	Days          int              `json:"days"`
	TotalQuota    int64            `json:"total_quota"`
	TotalRequests int64            `json:"total_requests"`
	Rows          []UsageWindowRow `json:"rows"`
	GeneratedAt   int64            `json:"generated_at"`
}

// BuildUsageWindowReport aggregates consume logs of the last days into fixed
// windows, grouped by the group or model name.
func BuildUsageWindowReport(windowKind string, days int, groupBy string) (*UsageWindowReport, error) {
	if days <= 0 {
		days = 7
	}
	if days > 90 {
		days = 90
	}
	report := &UsageWindowReport{
		Window:      windowKind,
		GroupBy:     groupBy,
		Days:        days,
		Rows:        make([]UsageWindowRow, 0),
		GeneratedAt: time.Now().Unix(),
	}
	since := time.Now().AddDate(0, 0, -days).Unix()
	rows, err := model.LOG_DB.Model(&model.Log{}).
		Where("type = ? AND created_at >= ?", model.LogTypeConsume, since).
		Select("*").Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type rowKey struct {
		start int64
		key   string
	}
	aggregates := make(map[rowKey]*UsageWindowRow)
	var logRow model.Log
	for rows.Next() {
		if err := model.LOG_DB.ScanRows(rows, &logRow); err != nil {
			return nil, err
		}
		start := QuotaWindowStart(windowKind, time.Unix(logRow.CreatedAt, 0))
		if start <= 0 {
			continue
		}
		key := logRow.Group
		if groupBy == "model" {
			key = logRow.ModelName
		}
		if key == "" {
			key = "(unknown)"
		}
		aggregateKey := rowKey{start: start, key: key}
		aggregate, ok := aggregates[aggregateKey]
		if !ok {
			aggregate = &UsageWindowRow{
				Key:         key,
				WindowStart: start,
				WindowEnd:   QuotaWindowResetAt(windowKind, time.Unix(logRow.CreatedAt, 0)),
			}
			aggregates[aggregateKey] = aggregate
		}
		aggregate.Quota += int64(logRow.Quota)
		aggregate.Requests++
		aggregate.PromptTokens += int64(logRow.PromptTokens)
		aggregate.CompletionTokens += int64(logRow.CompletionTokens)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, aggregate := range aggregates {
		report.Rows = append(report.Rows, *aggregate)
		report.TotalQuota += aggregate.Quota
		report.TotalRequests += aggregate.Requests
	}
	sort.Slice(report.Rows, func(i, j int) bool {
		if report.Rows[i].WindowStart != report.Rows[j].WindowStart {
			return report.Rows[i].WindowStart > report.Rows[j].WindowStart
		}
		return report.Rows[i].Quota > report.Rows[j].Quota
	})
	return report, nil
}

// EstimateUserWindowExhaustion returns the projected seconds until the remaining
// window quota is used up, based on the user's last 30 minutes of spend. 0 means
// unknown or already exhausted.
func EstimateUserWindowExhaustion(userId int, remaining int64, now time.Time) int64 {
	if userId <= 0 || remaining <= 0 {
		return 0
	}
	const rateWindowSeconds int64 = 30 * 60
	since := now.Unix() - rateWindowSeconds
	var recentQuota int64
	if err := model.LOG_DB.Model(&model.Log{}).
		Where("type = ? AND user_id = ? AND created_at >= ?", model.LogTypeConsume, userId, since).
		Select("COALESCE(SUM(quota), 0)").Scan(&recentQuota).Error; err != nil {
		return 0
	}
	if recentQuota <= 0 {
		return 0
	}
	ratePerSecond := float64(recentQuota) / float64(rateWindowSeconds)
	if ratePerSecond <= 0 {
		return 0
	}
	return int64(float64(remaining) / ratePerSecond)
}
