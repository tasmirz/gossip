package coord_test

import (
	"context"
	"encoding/json"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/tasmirz/gossip/pkg/coord"
	"strings"
	"testing"
	"time"
)

func TestReportPagesAndCompactSubmit(t *testing.T) {
	s, _, p, owner, worker := setup(t)
	ctx := context.Background()
	task := create(t, s, p, owner, nil)
	attempt, err := s.ClaimTask(ctx, p.ID, worker.Token, task.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat("অ🧪x", 1800)
	tool := coord.MCPServer(s, p.ID, worker.Token).GetTool("gossip_submit")
	res, err := tool.Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "gossip_submit", Arguments: map[string]any{"task_id": task.ID, "attempt": attempt.Attempt, "baseline": "commit:abc", "body": body}}})
	if err != nil || res.IsError {
		t.Fatalf("submit: %+v %v", res, err)
	}
	text := res.Content[0].(mcp.TextContent).Text
	if strings.Contains(text, "🧪") {
		t.Fatal("submission echoed report body")
	}
	var report coord.Report
	if err = json.Unmarshal([]byte(text), &report); err != nil {
		t.Fatal(err)
	}
	read := coord.MCPServer(s, p.ID, owner.Token).GetTool("gossip_read")
	res, err = read.Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "gossip_read", Arguments: map[string]any{"report_id": report.ID, "max_chars": 6}}})
	if err != nil || res.IsError {
		t.Fatalf("read page: %+v %v", res, err)
	}
	var first coord.ReportPage
	if err = json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &first); err != nil {
		t.Fatal(err)
	}
	if first.Excerpt != "অ🧪xঅ🧪x" || first.NextOffset == nil || *first.NextOffset != 6 {
		t.Fatalf("MCP page: %+v", first)
	}
	offset := 0
	var assembled strings.Builder
	for {
		page, err := s.GetReportPage(ctx, p.ID, owner.Token, report.ID, offset, 701)
		if err != nil {
			t.Fatal(err)
		}
		assembled.WriteString(page.Excerpt)
		if page.NextOffset == nil {
			break
		}
		offset = *page.NextOffset
	}
	if assembled.String() != body {
		t.Fatal("paged Unicode report differs from submitted report")
	}
	if _, err = s.GetReportPage(ctx, p.ID, owner.Token, report.ID, 0, 16385); err == nil {
		t.Fatal("unbounded page accepted")
	}
	other, credential, err := s.CreateProject(ctx, "other", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetReportPage(ctx, other.ID, credential.Token, report.ID, 0, 10); err == nil {
		t.Fatal("cross-project report read")
	}
}
