package model

import (
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// Privacy dry-run audit (issue #13): each privacy-filter hit is recorded as one
// event plus per-rule finding rows. Only rule ids, types, actions and counts are
// stored — never the original values.

// PrivacyAuditEvent is one masked request (or one dry-run hit).
type PrivacyAuditEvent struct {
	Id           int    `gorm:"primaryKey" json:"id"`
	UserId       int    `gorm:"index" json:"user_id"`
	Username     string `gorm:"type:varchar(64)" json:"username,omitempty"`
	RequestId    string `gorm:"type:varchar(64);index" json:"request_id,omitempty"`
	Path         string `gorm:"type:varchar(255)" json:"path"`
	Mode         string `gorm:"type:varchar(16);index" json:"mode"`
	FindingCount int    `json:"finding_count"`
	CreatedAt    int64  `gorm:"index" json:"created_at"`
}

func (PrivacyAuditEvent) TableName() string {
	return "privacy_audit_events"
}

// PrivacyAuditFinding is the aggregated hit count of one rule in one event.
type PrivacyAuditFinding struct {
	Id      int    `gorm:"primaryKey" json:"id"`
	EventId int    `gorm:"index" json:"event_id"`
	RuleId  string `gorm:"type:varchar(64);index" json:"rule_id"`
	Type    string `gorm:"type:varchar(64);index" json:"type"`
	Action  string `gorm:"type:varchar(16)" json:"action"`
	Count   int    `json:"count"`
}

func (PrivacyAuditFinding) TableName() string {
	return "privacy_audit_findings"
}

// RecordPrivacyAuditEvent stores one event and its findings; best effort, a
// failure must never affect the request.
func RecordPrivacyAuditEvent(event *PrivacyAuditEvent, findings []PrivacyAuditFinding) error {
	if DB == nil || event == nil {
		return nil
	}
	if event.CreatedAt == 0 {
		event.CreatedAt = common.GetTimestamp()
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(event).Error; err != nil {
			return err
		}
		if len(findings) == 0 {
			return nil
		}
		for index := range findings {
			findings[index].EventId = event.Id
		}
		return tx.Create(&findings).Error
	})
}

// PrivacyAuditCount is one grouped statistic.
type PrivacyAuditCount struct {
	Key   string `json:"key"`
	Count int64  `json:"count"`
}

// PrivacyAuditDaily is one day of the event trend.
type PrivacyAuditDaily struct {
	Date     string `json:"date"`
	Events   int64  `json:"events"`
	Findings int64  `json:"findings"`
}

// PrivacyAuditStats is the dashboard payload of the privacy audit page.
type PrivacyAuditStats struct {
	WindowDays    int                 `json:"window_days"`
	TotalEvents   int64               `json:"total_events"`
	TotalFindings int64               `json:"total_findings"`
	AffectedUsers int64               `json:"affected_users"`
	ByType        []PrivacyAuditCount `json:"by_type"`
	ByAction      []PrivacyAuditCount `json:"by_action"`
	ByRule        []PrivacyAuditCount `json:"by_rule"`
	ByMode        []PrivacyAuditCount `json:"by_mode"`
	Daily         []PrivacyAuditDaily `json:"daily"`
}

type privacyAuditGroupRow struct {
	Label string `gorm:"column:label"`
	Count int64  `gorm:"column:count"`
}

func privacyAuditGroup(query *gorm.DB, column string) ([]PrivacyAuditCount, error) {
	var rows []privacyAuditGroupRow
	if err := query.Select(fmt.Sprintf("%s AS label, COALESCE(SUM(count), 0) AS count", column)).
		Group(column).Order("count DESC").Scan(&rows).Error; err != nil {
		return nil, err
	}
	counts := make([]PrivacyAuditCount, 0, len(rows))
	for _, row := range rows {
		counts = append(counts, PrivacyAuditCount{Key: row.Label, Count: row.Count})
	}
	return counts, nil
}

// GetPrivacyAuditStats aggregates events and findings of the last days.
func GetPrivacyAuditStats(days int) (*PrivacyAuditStats, error) {
	if days <= 0 {
		days = 7
	}
	if days > 90 {
		days = 90
	}
	stats := &PrivacyAuditStats{WindowDays: days}
	now := time.Now()
	windowStart := now.AddDate(0, 0, -(days - 1))
	since := time.Date(windowStart.Year(), windowStart.Month(), windowStart.Day(), 0, 0, 0, 0, time.Local).Unix()
	eventIDs := DB.Model(&PrivacyAuditEvent{}).Select("id").Where("created_at >= ?", since)

	if err := DB.Model(&PrivacyAuditEvent{}).Where("created_at >= ?", since).Count(&stats.TotalEvents).Error; err != nil {
		return nil, err
	}
	if err := DB.Model(&PrivacyAuditEvent{}).Where("created_at >= ?", since).Distinct("user_id").Count(&stats.AffectedUsers).Error; err != nil {
		return nil, err
	}
	if err := DB.Model(&PrivacyAuditFinding{}).Where("event_id IN (?)", eventIDs).
		Select("COALESCE(SUM(count), 0)").Scan(&stats.TotalFindings).Error; err != nil {
		return nil, err
	}
	findingQuery := func() *gorm.DB {
		return DB.Model(&PrivacyAuditFinding{}).Where("event_id IN (?)", eventIDs)
	}
	var err error
	if stats.ByType, err = privacyAuditGroup(findingQuery(), "type"); err != nil {
		return nil, err
	}
	if stats.ByAction, err = privacyAuditGroup(findingQuery(), "action"); err != nil {
		return nil, err
	}
	if stats.ByRule, err = privacyAuditGroup(findingQuery(), "rule_id"); err != nil {
		return nil, err
	}

	var modes []privacyAuditGroupRow
	if err := DB.Model(&PrivacyAuditEvent{}).Where("created_at >= ?", since).
		Select("mode AS label, COUNT(*) AS count").Group("mode").Order("count DESC").Scan(&modes).Error; err != nil {
		return nil, err
	}
	for _, mode := range modes {
		stats.ByMode = append(stats.ByMode, PrivacyAuditCount{Key: mode.Label, Count: mode.Count})
	}

	stats.Daily = make([]PrivacyAuditDaily, 0, days)
	for offset := days - 1; offset >= 0; offset-- {
		day := now.AddDate(0, 0, -offset)
		start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.Local)
		end := start.AddDate(0, 0, 1)
		daily := PrivacyAuditDaily{Date: start.Format("2006-01-02")}
		if err := DB.Model(&PrivacyAuditEvent{}).Where("created_at >= ? AND created_at < ?", start.Unix(), end.Unix()).
			Count(&daily.Events).Error; err != nil {
			return nil, err
		}
		if err := DB.Model(&PrivacyAuditFinding{}).
			Where("event_id IN (?)", DB.Model(&PrivacyAuditEvent{}).Select("id").Where("created_at >= ? AND created_at < ?", start.Unix(), end.Unix())).
			Select("COALESCE(SUM(count), 0)").Scan(&daily.Findings).Error; err != nil {
			return nil, err
		}
		stats.Daily = append(stats.Daily, daily)
	}
	return stats, nil
}

// PrivacyAuditEventFilter narrows the paginated event list.
type PrivacyAuditEventFilter struct {
	Page     int
	PageSize int
	Days     int
	Type     string
	Mode     string
}

// PrivacyAuditEventView is one event with its aggregated findings.
type PrivacyAuditEventView struct {
	PrivacyAuditEvent
	Findings []PrivacyAuditFinding `json:"findings"`
}

// GetPrivacyAuditEvents returns one page of events, newest first.
func GetPrivacyAuditEvents(filter PrivacyAuditEventFilter) ([]PrivacyAuditEventView, int64, error) {
	if filter.Page <= 0 {
		filter.Page = 1
	}
	if filter.PageSize <= 0 {
		filter.PageSize = 20
	}
	if filter.PageSize > 100 {
		filter.PageSize = 100
	}
	if filter.Days <= 0 {
		filter.Days = 30
	}
	if filter.Days > 365 {
		filter.Days = 365
	}
	since := common.GetTimestamp() - int64(filter.Days)*86400
	query := DB.Model(&PrivacyAuditEvent{}).Where("created_at >= ?", since)
	if filter.Mode != "" {
		query = query.Where("mode = ?", filter.Mode)
	}
	if filter.Type != "" {
		query = query.Where("id IN (?)", DB.Model(&PrivacyAuditFinding{}).Select("event_id").Where("type = ?", filter.Type))
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var events []PrivacyAuditEvent
	if err := query.Order("created_at DESC, id DESC").
		Offset((filter.Page - 1) * filter.PageSize).Limit(filter.PageSize).
		Find(&events).Error; err != nil {
		return nil, 0, err
	}
	views := make([]PrivacyAuditEventView, 0, len(events))
	if len(events) == 0 {
		return views, total, nil
	}
	ids := make([]int, 0, len(events))
	for _, event := range events {
		ids = append(ids, event.Id)
	}
	var findings []PrivacyAuditFinding
	if err := DB.Where("event_id IN ?", ids).Order("id ASC").Find(&findings).Error; err != nil {
		return nil, 0, err
	}
	byEvent := make(map[int][]PrivacyAuditFinding, len(events))
	for _, finding := range findings {
		byEvent[finding.EventId] = append(byEvent[finding.EventId], finding)
	}
	for _, event := range events {
		views = append(views, PrivacyAuditEventView{PrivacyAuditEvent: event, Findings: byEvent[event.Id]})
	}
	return views, total, nil
}

// CleanupPrivacyAuditEvents deletes events older than the given days and their
// findings, in bounded batches.
func CleanupPrivacyAuditEvents(olderThanDays int) (int64, int64, error) {
	if olderThanDays < 1 {
		olderThanDays = 1
	}
	if olderThanDays > 365 {
		olderThanDays = 365
	}
	before := common.GetTimestamp() - int64(olderThanDays)*86400
	var deletedEvents, deletedFindings int64
	for {
		var ids []int
		if err := DB.Model(&PrivacyAuditEvent{}).Where("created_at < ?", before).
			Limit(1000).Pluck("id", &ids).Error; err != nil {
			return deletedEvents, deletedFindings, err
		}
		if len(ids) == 0 {
			break
		}
		result := DB.Where("event_id IN ?", ids).Delete(&PrivacyAuditFinding{})
		if result.Error != nil {
			return deletedEvents, deletedFindings, result.Error
		}
		deletedFindings += result.RowsAffected
		result = DB.Where("id IN ?", ids).Delete(&PrivacyAuditEvent{})
		if result.Error != nil {
			return deletedEvents, deletedFindings, result.Error
		}
		deletedEvents += result.RowsAffected
	}
	return deletedEvents, deletedFindings, nil
}
