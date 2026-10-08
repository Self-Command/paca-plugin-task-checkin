package model

import (
	"encoding/json"
	"errors"
	"time"
)

type TimeValue struct {
	Precision string `json:"precision"`
	Value     string `json:"value"`
}
type TaskTimes struct {
	Start        TimeValue `json:"start"`
	Due          TimeValue `json:"due"`
	Timezone     string    `json:"timezone"`
	StartMinutes int       `json:"start_minutes"`
	DueMinutes   int       `json:"due_minutes"`
}

func normalizeTime(value TimeValue, zone string) (any, map[string]any, *time.Time, error) {
	fields := map[string]any{"precision": value.Precision, "source": value.Value, "instant": nil, "core_date": ""}
	switch value.Precision {
	case "none":
		if value.Value != "" {
			return nil, nil, nil, errors.New("未设置的时间不能包含日期。")
		}
		return nil, fields, nil, nil
	case "day":
		day, err := time.Parse("2006-01-02", value.Value)
		if err != nil || day.Format("2006-01-02") != value.Value {
			return nil, nil, nil, errors.New("日期格式无效。")
		}
		fields["core_date"] = value.Value
		return value.Value + "T00:00:00Z", fields, nil, nil
	case "instant":
		parsed, err := ParseExactTime(value.Value, zone)
		if err != nil || parsed == nil {
			return nil, nil, nil, errors.New("请填写精确到分钟的有效时间。")
		}
		loc, err := time.LoadLocation(zone)
		if err != nil {
			return nil, nil, nil, err
		}
		day := parsed.In(loc).Format("2006-01-02")
		fields["instant"] = parsed.Format(time.RFC3339Nano)
		fields["source"] = parsed.In(loc).Format(time.RFC3339Nano)
		fields["core_date"] = day
		return day + "T00:00:00Z", fields, parsed, nil
	default:
		return nil, nil, nil, errors.New("请选择未设置、仅日期或具体时间。")
	}
}

func TimePatch(t Task, input TaskTimes) (map[string]any, error) {
	if input.Timezone == "" {
		return nil, errors.New("请选择时区。")
	}
	if _, err := time.LoadLocation(input.Timezone); err != nil {
		return nil, errors.New("时区无效。")
	}
	if input.StartMinutes < 0 || input.StartMinutes > 1440 || input.DueMinutes < 0 || input.DueMinutes > 1440 {
		return nil, errors.New("提前分钟必须在 0 至 1440 之间。")
	}
	start, sm, si, err := normalizeTime(input.Start, input.Timezone)
	if err != nil {
		return nil, err
	}
	due, dm, di, err := normalizeTime(input.Due, input.Timezone)
	if err != nil {
		return nil, err
	}
	if si != nil && di != nil && di.Before(*si) {
		return nil, errors.New("结束时间不能早于开始时间。")
	}
	if start != nil && due != nil && start.(string)[:10] > due.(string)[:10] {
		return nil, errors.New("结束日期不能早于开始日期。")
	}
	custom := map[string]any{}
	raw, _ := json.Marshal(t.Custom)
	_ = json.Unmarshal(raw, &custom)
	if custom == nil {
		custom = map[string]any{}
	}
	meta, _ := custom["_integration_state_v1"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
	}
	meta["version"] = 2
	meta["timezone"] = input.Timezone
	if meta["source"] == nil {
		meta["source"] = "paca"
	}
	for key, value := range sm {
		meta["start_"+key] = value
	}
	for key, value := range dm {
		meta["due_"+key] = value
	}
	meta["reminder_start_minutes"] = input.StartMinutes
	meta["reminder_due_minutes"] = input.DueMinutes
	custom["_integration_state_v1"] = meta
	return map[string]any{"start_date": start, "due_date": due, "custom_fields": custom}, nil
}

func TimesOf(t Task) TaskTimes {
	m, _ := t.Custom["_integration_state_v1"].(map[string]any)
	zone, _ := m["timezone"].(string)
	if zone == "" {
		zone = "Asia/Shanghai"
	}
	result := TaskTimes{Timezone: zone, StartMinutes: 10, DueMinutes: 10}
	for _, item := range []struct {
		key  string
		core *time.Time
		out  *TimeValue
	}{{"start", t.StartDate, &result.Start}, {"due", t.DueDate, &result.Due}} {
		*item.out = TimeValue{Precision: "none"}
		if item.core != nil {
			*item.out = TimeValue{Precision: "day", Value: item.core.UTC().Format("2006-01-02")}
			if precise := Precise(t, item.key); precise != nil {
				*item.out = TimeValue{Precision: "instant", Value: precise.Format(time.RFC3339Nano)}
			}
		}
	}
	if v, ok := m["reminder_start_minutes"].(float64); ok {
		result.StartMinutes = int(v)
	}
	if v, ok := m["reminder_due_minutes"].(float64); ok {
		result.DueMinutes = int(v)
	}
	return result
}

// Includes the full custom map to detect concurrent unrelated custom-field edits
// before issuing the official whole-map PATCH. It is not a cross-system CAS.
func TimeVersion(t Task) string { return Hash(t) }

func ParseExactTime(s, zone string) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		utc := t.UTC()
		return &utc, nil
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return nil, err
	}
	for _, layout := range []string{"2006-01-02T15:04:05.999999999", "2006-01-02T15:04"} {
		wall, err := time.Parse(layout, s)
		if err != nil {
			continue
		}
		t, err := time.ParseInLocation(layout, s, loc)
		if err != nil {
			continue
		}
		normalized := wall.Format("2006-01-02T15:04:05.999999999")
		if t.In(loc).Format("2006-01-02T15:04:05.999999999") != normalized {
			continue
		}
		for _, delta := range []time.Duration{-2 * time.Hour, -time.Hour, time.Hour, 2 * time.Hour} {
			if t.Add(delta).In(loc).Format("2006-01-02T15:04:05.999999999") == normalized {
				return nil, errors.New("ambiguous local time; include UTC offset")
			}
		}
		utc := t.UTC()
		return &utc, nil
	}
	return nil, errors.New("precise time required; invalid or nonexistent local time")
}
