package worker

import (
	"context"
	"encoding/json"
	"github.com/Self-Command/paca-plugin-task-checkin/internal/model"
	"strconv"
	"strings"
)

type TaskCard struct {
	Title    string   `json:"title"`
	Number   string   `json:"number"`
	Created  any      `json:"created"`
	Content  string   `json:"content"`
	Start    any      `json:"start"`
	Due      any      `json:"due"`
	Priority string   `json:"priority"`
	Status   string   `json:"status"`
	Tags     []string `json:"tags"`
	Source   string   `json:"source"`
	Timezone string   `json:"timezone"`
}

func plainBlocks(value any) string {
	var lines []string
	var walk func(any)
	walk = func(v any) {
		switch item := v.(type) {
		case []any:
			for _, child := range item {
				walk(child)
			}
		case map[string]any:
			if text, ok := item["text"].(string); ok {
				lines = append(lines, text)
			}
			if c, ok := item["content"]; ok {
				before := len(lines)
				walk(c)
				if len(lines) > before && item["type"] == "paragraph" {
					lines = append(lines, "\n")
				}
			}
			if c, ok := item["children"]; ok {
				walk(c)
			}
		case string:
			lines = append(lines, item)
		}
	}
	walk(value)
	return strings.TrimSpace(strings.Join(lines, ""))
}
func taskNumber(value int64) string {
	if value < 1 {
		return "未设置"
	}
	return "#" + strconv.FormatInt(value, 10)
}
func priorityLabel(value int) string {
	switch {
	case value >= 100:
		return "紧急"
	case value >= 50:
		return "高"
	case value >= 20:
		return "中"
	case value > 0:
		return "低"
	}
	return "未设置"
}
func statusLabel(name, category string) string {
	defaults := map[string]string{"Backlog": "待安排", "Todo": "待完成", "To Do": "待完成", "In Progress": "进行中", "Done": "已完成", "Archive": "已归档"}
	if value, ok := defaults[name]; ok {
		return value
	}
	if name != "" {
		return name
	}
	labels := map[string]string{"backlog": "待安排", "todo": "待完成", "inprogress": "进行中", "done": "已完成"}
	if value, ok := labels[category]; ok {
		return value
	}
	return "未设置"
}
func (w *Worker) taskCard(ctx context.Context, i model.Instance, cfg model.Config) (TaskCard, error) {
	var task model.Task
	if err := w.call(ctx, "GET", "/projects/"+i.Project+"/tasks/"+i.Task, nil, &task); err != nil {
		return TaskCard{}, err
	}
	var statuses struct {
		Items []struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Category string `json:"category"`
		} `json:"items"`
	}
	if err := w.call(ctx, "GET", "/projects/"+i.Project+"/task-statuses", nil, &statuses); err != nil {
		return TaskCard{}, err
	}
	card := TaskCard{Number: taskNumber(task.TaskNumber), Created: task.CreatedAt, Title: task.Title, Content: plainBlocks(task.Description), Start: i.Start, Due: i.Due, Priority: priorityLabel(task.Importance), Status: "未设置", Tags: task.Tags, Source: "任务中心", Timezone: cfg.Timezone}
	for _, state := range statuses.Items {
		if state.ID == task.Status {
			card.Status = statusLabel(state.Name, state.Category)
			break
		}
	}
	if task.Status == cfg.ArchiveStatus {
		card.Status = "已归档"
	}
	var source struct {
		Snapshot struct {
			Details *string `json:"details"`
		} `json:"snapshot"`
	}
	if json.Unmarshal(i.Source, &source) == nil && source.Snapshot.Details != nil {
		card.Content = *source.Snapshot.Details
		card.Source = "Obsidian 任务"
	}
	if card.Tags == nil {
		card.Tags = []string{}
	}
	return card, nil
}
