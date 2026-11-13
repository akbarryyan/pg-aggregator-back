package handler

import (
	"github.com/akbarryyan/pg-aggregator-back/internal/domain/merchant"
)

// apiKeyDetails builds the activity-log details of an API key create/rotate.
// It carries only the environment and key id, never the key value.
func apiKeyDetails(res *merchant.UpsertAPIKeyResponse) map[string]interface{} {
	d := map[string]interface{}{"environment": res.Environment}
	if res.Key != nil {
		d["key_id"] = res.Key.ID.String()
	}
	return d
}

// updatedMerchantFields lists the names of the fields an update request sets.
func updatedMerchantFields(req *merchant.UpdateMerchantRequest) []string {
	fields := []string{}
	if req.Name != nil {
		fields = append(fields, "name")
	}
	if req.Phone != nil {
		fields = append(fields, "phone")
	}
	if req.BusinessName != nil {
		fields = append(fields, "business_name")
	}
	if req.WebhookURL != nil {
		fields = append(fields, "webhook_url")
	}
	return fields
}

// last4 returns the last four digits of an account number, for the activity
// log (full account numbers are never recorded).
func last4(account string) string {
	digits := make([]rune, 0, len(account))
	for _, c := range account {
		if c >= '0' && c <= '9' {
			digits = append(digits, c)
		}
	}
	if len(digits) > 4 {
		digits = digits[len(digits)-4:]
	}
	return string(digits)
}
