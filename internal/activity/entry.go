// Package activity holds the activity-log domain types.
package activity

import (
	"time"

	"github.com/google/uuid"
)

const (
	ActorAdmin        = "admin"
	ActorMerchantUser = "merchant_user"
	ActorAnonymous    = "anonymous"
)

// Entry is one recorded action.
type Entry struct {
	ID         uuid.UUID              `json:"id"`
	CreatedAt  time.Time              `json:"created_at"`
	ActorType  string                 `json:"actor_type"`
	ActorID    *uuid.UUID             `json:"actor_id,omitempty"`
	ActorEmail string                 `json:"actor_email"`
	MerchantID *uuid.UUID             `json:"merchant_id,omitempty"`
	Action     string                 `json:"action"`
	Method     string                 `json:"method"`
	Route      string                 `json:"route"`
	TargetID   string                 `json:"target_id"`
	StatusCode int                    `json:"status_code"`
	IP         string                 `json:"ip"`
	UserAgent  string                 `json:"user_agent"`
	DurationMS int                    `json:"duration_ms"`
	Details    map[string]interface{} `json:"details"`
}

// Filter narrows List/Count.
type Filter struct {
	Search     string // ILIKE on actor_email, action, target_id
	ActorType  string // "" = all
	MerchantID *uuid.UUID
	Result     string     // "", "success" (<400), "failed" (>=400)
	DateFrom   *time.Time // inclusive
	DateTo     *time.Time // exclusive
}

// DailyPoint is one day of the stats chart.
type DailyPoint struct {
	Date     string `json:"date"`  // 2006-01-02
	Label    string `json:"label"` // e.g. "Nov 13"
	Admin    int64  `json:"admin"`
	Merchant int64  `json:"merchant"` // merchant_user + anonymous
}

// ActionCount is one breakdown row.
type ActionCount struct {
	Action string `json:"action"`
	Count  int64  `json:"count"`
}

// Stats is the summary behind the admin activity page.
type Stats struct {
	Days         int           `json:"days"`
	Total        int64         `json:"total"`
	FailedLogins int64         `json:"failed_logins"` // actor_type=anonymous
	ActiveActors int64         `json:"active_actors"` // distinct non-null actor_id
	Daily        []DailyPoint  `json:"daily"`
	Breakdown    []ActionCount `json:"breakdown"` // top 6 actions + "Other"
}
