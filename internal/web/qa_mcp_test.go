package web

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"
)

type mcpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type mcpToolResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StructuredContent map[string]any `json:"structuredContent"`
	IsError           bool           `json:"isError"`
}

// mcp sends one JSON-RPC request to /mcp/qa.
func (e qaAPIEnv) mcp(t *testing.T, token, method string, params any) (int, mcpResponse) {
	t.Helper()
	status, body := e.api(t, http.MethodPost, token, "/mcp/qa", map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	var out mcpResponse
	if body != "" {
		_ = json.Unmarshal([]byte(body), &out)
	}
	return status, out
}

func (e qaAPIEnv) callTool(t *testing.T, token, name string, args map[string]any) mcpToolResult {
	t.Helper()
	status, resp := e.mcp(t, token, "tools/call", map[string]any{"name": name, "arguments": args})
	if status != http.StatusOK || resp.Error != nil {
		t.Fatalf("tools/call %s = %d %+v", name, status, resp.Error)
	}
	return decodeJSON[mcpToolResult](t, string(resp.Result))
}

func TestQAMCP_HandshakeAndToolList(t *testing.T) {
	t.Parallel()
	e := newQAAPIEnv(t, "acme-mcp")

	// The legacy endpoint answered anyone who sent X-Public; this one
	// answers no one without a token.
	if status, _ := e.api(t, http.MethodPost, "", "/mcp/qa", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}); status != http.StatusUnauthorized {
		t.Fatalf("no token = %d; want 401", status)
	}
	status, resp := e.mcp(t, e.memberToken, "initialize", map[string]any{
		"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test", "version": "1"},
	})
	init := decodeJSON[struct {
		ProtocolVersion string         `json:"protocolVersion"`
		Capabilities    map[string]any `json:"capabilities"`
		ServerInfo      struct{ Name string }
	}](t, string(resp.Result))
	if status != http.StatusOK || init.ProtocolVersion != "2025-06-18" || init.Capabilities["tools"] == nil || init.ServerInfo.Name == "" {
		t.Fatalf("initialize = %d %s", status, resp.Result)
	}
	if status, body := e.api(t, http.MethodPost, e.memberToken, "/mcp/qa", map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}); status != http.StatusAccepted || body != "" {
		t.Errorf("notification = %d %q; want 202, no body", status, body)
	}

	names := func(token string) string {
		_, resp := e.mcp(t, token, "tools/list", nil)
		list := decodeJSON[struct {
			Tools []struct {
				Name        string         `json:"name"`
				Description string         `json:"description"`
				InputSchema map[string]any `json:"inputSchema"`
			} `json:"tools"`
		}](t, string(resp.Result))
		var out []string
		for _, tool := range list.Tools {
			if tool.Description == "" || tool.InputSchema["type"] != "object" {
				t.Errorf("tool %s has no description or object schema", tool.Name)
			}
			out = append(out, tool.Name)
		}
		sort.Strings(out)
		return strings.Join(out, ",")
	}
	if got := names(e.memberToken); got != "create_cases,create_issue,get_run,list_cases,list_plans,list_projects,list_runs,list_sections,record_result" {
		t.Errorf("member tools = %s", got)
	}
	// A token only sees the tools it may call.
	if got := names(e.readToken); got != "get_run,list_cases,list_plans,list_projects,list_runs,list_sections" {
		t.Errorf("read-only tools = %s", got)
	}

	if _, resp := e.mcp(t, e.memberToken, "resources/list", nil); resp.Error == nil || resp.Error.Code != -32601 {
		t.Errorf("unknown method = %+v; want -32601", resp.Error)
	}
	if _, resp := e.mcp(t, e.memberToken, "tools/call", map[string]any{"name": "drop_database"}); resp.Error == nil || resp.Error.Code != -32602 {
		t.Errorf("unknown tool = %+v; want -32602", resp.Error)
	}
	if status, _ := e.api(t, http.MethodGet, e.memberToken, "/mcp/qa", nil); status != http.StatusMethodNotAllowed {
		t.Errorf("GET = %d; want 405 (no server stream)", status)
	}
}

func TestQAMCP_ToolsCallTheAPIWithItsChecks(t *testing.T) {
	t.Parallel()
	e := newQAAPIEnv(t, "acme-mcp-tools")
	project := e.projectID.String()

	res := e.callTool(t, e.memberToken, "list_projects", nil)
	if res.IsError || len(res.StructuredContent["projects"].([]any)) != 2 || res.Content[0].Type != "text" || !strings.Contains(res.Content[0].Text, "Portal") {
		t.Fatalf("list_projects = %+v", res)
	}
	res = e.callTool(t, e.memberToken, "create_cases", map[string]any{"project_id": project, "cases": apiCaseRows(2, "AI", "Generated")})
	if res.IsError || res.StructuredContent["created"].(float64) != 2 {
		t.Fatalf("create_cases = %+v", res)
	}
	res = e.callTool(t, e.memberToken, "list_cases", map[string]any{"project_id": project, "query": "generated"})
	if res.IsError || res.StructuredContent["total"].(float64) != 2 {
		t.Fatalf("list_cases = %+v", res)
	}
	plans := e.callTool(t, e.memberToken, "list_plans", map[string]any{"project_id": project}).StructuredContent["plans"].([]any)
	plan := plans[0].(map[string]any)["id"].(string)
	runs := e.callTool(t, e.memberToken, "list_runs", map[string]any{"project_id": project, "plan_id": plan}).StructuredContent["runs"].([]any)
	run := runs[0].(map[string]any)["id"].(string)
	items := e.callTool(t, e.memberToken, "get_run", map[string]any{"project_id": project, "run_id": run}).StructuredContent["items"].([]any)
	item := items[0].(map[string]any)["id"].(string)
	if res = e.callTool(t, e.memberToken, "record_result", map[string]any{"project_id": project, "item_id": item, "status": "failed"}); res.IsError {
		t.Fatalf("record_result = %+v", res)
	}
	if res = e.callTool(t, e.memberToken, "create_issue", map[string]any{"project_id": project, "item_id": item, "title": "Broken", "severity": "major"}); res.IsError || res.StructuredContent["ref"] == nil {
		t.Fatalf("create_issue = %+v", res)
	}

	// The same checks as the API: the token's scopes, the project it may
	// reach, and the operation's own validation — each an error result the
	// assistant can read.
	for name, tc := range map[string]struct {
		token, tool string
		args        map[string]any
		want        string
	}{
		"scope":      {e.readToken, "create_cases", map[string]any{"project_id": project, "cases": apiCaseRows(1, "AI")}, "permission"},
		"guest":      {e.guestToken, "list_cases", map[string]any{"project_id": e.other.String()}, "not found"},
		"pinned":     {e.pinnedToken, "list_plans", map[string]any{"project_id": e.other.String()}, "not found"},
		"no project": {e.memberToken, "list_sections", map[string]any{}, "not found"},
		"bad row":    {e.memberToken, "create_cases", map[string]any{"project_id": project, "cases": []map[string]any{{"title": "x"}}}, "Row 1: section_path is required."},
		"bad args":   {e.memberToken, "list_cases", map[string]any{"project_id": project, "limit": "many"}, "arguments"},
	} {
		res := e.callTool(t, tc.token, tc.tool, tc.args)
		if !res.IsError || !strings.Contains(res.Content[0].Text, tc.want) {
			t.Errorf("%s: %+v; want an error mentioning %q", name, res, tc.want)
		}
	}
}

// Settings → Tokens says where the QA API and the MCP endpoint are and
// how an assistant sends the token.
func TestQAMCP_TokensPageShowsTheEndpoints(t *testing.T) {
	t.Parallel()
	e := newQAAPIEnv(t, "acme-mcp-settings")
	status, body := e.get(t, e.member, "/settings/tokens")
	if status != http.StatusOK || !strings.Contains(body, "/mcp/qa") || !strings.Contains(body, "/api/qa/projects") || !strings.Contains(body, "Authorization") {
		t.Fatalf("tokens page = %d; no QA API / MCP instructions", status)
	}
}
