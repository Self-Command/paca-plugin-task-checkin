package model

import "time"

type DeliveryAction struct {
	Connection string `json:"connection_id"`
	Record     string `json:"record_id"`
	Op         string `json:"op_id"`
	Action     string `json:"action"`
	Revision   int64  `json:"base_revision"`
	Path       string `json:"path,omitempty"`
	Created    string `json:"note_created,omitempty"`
}

func (a DeliveryAction) Valid() bool {
	if !UUID.MatchString(a.Connection) || !UUID.MatchString(a.Record) || len(a.Op) < 8 || len(a.Op) > 128 || a.Revision < 1 {
		return false
	}
	if a.Action != "retry" && a.Action != "ignore" && a.Action != "restore" && a.Action != "associate" {
		return false
	}
	return a.Action != "associate" || (a.Path != "" && len(a.Path) <= 1024 && a.Created != "" && len(a.Created) <= 128)
}

func DeliveryRetry(attempts int, code string, now time.Time) (string, time.Time) {
	if code != "transfer_unavailable" && code != "unavailable" && code != "photo" {
		return "needs_action", now
	}
	if attempts >= 10 {
		return "needs_action", now
	}
	if attempts < 1 {
		attempts = 1
	}
	delay := 30 * time.Second * time.Duration(1<<min(attempts-1, 5))
	if delay > 15*time.Minute {
		delay = 15 * time.Minute
	}
	return "retry_wait", now.Add(delay)
}
