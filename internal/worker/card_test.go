package worker

import (
	"testing"
)

func TestProductionTaskCardLabelsAndContent(t *testing.T) {
	if priorityLabel(75) != "高" || priorityLabel(100) != "紧急" || statusLabel("In Progress", "inprogress") != "进行中" {
		t.Fatal("English labels leaked")
	}
	input := []any{map[string]any{"type": "paragraph", "content": []any{map[string]any{"type": "text", "text": "任务正文"}}}}
	if plainBlocks(input) != "任务正文" {
		t.Fatal("task content missing")
	}
}
