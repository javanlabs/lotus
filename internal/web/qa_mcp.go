package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/uyab/lotus/internal/authn"
)

// POST /mcp/qa is an MCP server (streamable HTTP transport) for the QA
// module, so an AI assistant can read the library and runs, write cases in
// bulk, record results and report issues. It is stateless: each POST
// carries one JSON-RPC message and gets one JSON answer (no event stream,
// so GET is 405). Authentication is the QA API's — a Lotus API token,
// never anonymous — and every tool runs the same operation, with the same
// permission and project checks, as its /api/qa route.

// mcpProtocolVersions are the MCP revisions this server speaks, newest
// first; initialize answers with the client's if it is one of them.
var mcpProtocolVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

const mcpInstructions = "Lotus QA: test cases (refs TC-n) filed in nested sections, scenarios, test plans, " +
	"runs of a plan with one item per case, and issues (refs BUG-n). Start with list_projects; every other tool " +
	"takes a project_id from it. create_cases writes up to 500 cases at once, all or none, creating sections " +
	"along each section_path. record_result and create_issue take item ids from get_run."

// mcpTool is one tool: its permission (the same as its API route), its
// description and input schema, and how it runs.
type mcpTool struct {
	Name        string
	Perm        string
	Description string
	Schema      map[string]any
	Run         func(c *fiber.Ctx, args json.RawMessage) (any, error)
}

func mcpObject(required []string, props map[string]any) map[string]any {
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

var (
	mcpString  = map[string]any{"type": "string"}
	mcpProject = map[string]any{"type": "string", "description": "Project id, from list_projects."}
	mcpSteps   = map[string]any{"type": "array", "items": mcpObject([]string{"action"}, map[string]any{
		"action": mcpString, "expected": mcpString,
	})}
)

// mcpArgs decodes a tool's arguments strictly: an unknown field is an
// error the assistant sees, not a silently ignored typo.
func mcpArgs(raw json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(orEmpty(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return apiErr(fiber.StatusBadRequest, "invalid arguments: %v", err)
	}
	return nil
}

type mcpProjectArg struct {
	ProjectID string `json:"project_id"`
}

// mcpProjectTool is a tool whose arguments are A plus project_id.
func mcpProjectTool[A any](op func(c *fiber.Ctx, call qaCall, a A) (any, error)) func(c *fiber.Ctx, raw json.RawMessage) (any, error) {
	return func(c *fiber.Ctx, raw json.RawMessage) (any, error) {
		// project_id and the tool's own fields arrive side by side.
		var p mcpProjectArg
		if err := json.Unmarshal(orEmpty(raw), &p); err != nil {
			return nil, apiErr(fiber.StatusBadRequest, "invalid arguments: %v", err)
		}
		fields := map[string]json.RawMessage{}
		if err := json.Unmarshal(orEmpty(raw), &fields); err != nil {
			return nil, apiErr(fiber.StatusBadRequest, "invalid arguments: %v", err)
		}
		delete(fields, "project_id")
		rest, _ := json.Marshal(fields)
		var args A
		if err := mcpArgs(rest, &args); err != nil {
			return nil, err
		}
		call, err := newQACall(c, p.ProjectID, true)
		if err != nil {
			return nil, err
		}
		return op(c, call, args)
	}
}

func orEmpty(raw json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return json.RawMessage("{}")
	}
	return raw
}

type mcpNoArgs struct{}

type mcpPlanArgs struct {
	PlanID string `json:"plan_id"`
}

var mcpTools = []mcpTool{
	{
		Name: "list_projects", Perm: "qa.read",
		Description: "List the projects this token can reach, with their ids.",
		Schema:      mcpObject(nil, map[string]any{}),
		Run: func(c *fiber.Ctx, raw json.RawMessage) (any, error) {
			var a mcpNoArgs
			if err := mcpArgs(raw, &a); err != nil {
				return nil, err
			}
			call, err := newQACall(c, "", false)
			if err != nil {
				return nil, err
			}
			return qaOpListProjects(c, call)
		},
	},
	{
		Name: "list_sections", Perm: "qa.read",
		Description: "List a project's case sections (a tree: each has its parent_id and full path).",
		Schema:      mcpObject([]string{"project_id"}, map[string]any{"project_id": mcpProject}),
		Run: mcpProjectTool(func(c *fiber.Ctx, call qaCall, _ mcpNoArgs) (any, error) {
			return qaOpListSections(c, call)
		}),
	},
	{
		Name: "list_cases", Perm: "qa.read",
		Description: "List a project's test cases, newest last, paged (default 100, at most 500). " +
			"Optionally only a section and its subsections, or titles containing query.",
		Schema: mcpObject([]string{"project_id"}, map[string]any{
			"project_id": mcpProject, "section_id": mcpString, "query": mcpString,
			"include_archived": map[string]any{"type": "boolean"},
			"limit":            map[string]any{"type": "integer", "minimum": 1, "maximum": qaAPIMaxLimit},
			"offset":           map[string]any{"type": "integer", "minimum": 0},
		}),
		Run: mcpProjectTool(qaOpListCases),
	},
	{
		Name: "create_cases", Perm: "qa.case.write",
		Description: "Create up to 500 test cases in one project, all or none. Each case names its section by path " +
			"(outermost first); missing sections are created. Returns the new refs (TC-n) in order.",
		Schema: mcpObject([]string{"project_id", "cases"}, map[string]any{
			"project_id": mcpProject,
			"cases": map[string]any{"type": "array", "minItems": 1, "maxItems": qaAPIMaxLimit, "items": mcpObject(
				[]string{"section_path", "title"}, map[string]any{
					"section_path": map[string]any{"type": "array", "items": mcpString, "minItems": 1},
					"title":        mcpString, "precondition": mcpString, "steps": mcpSteps,
					"priority": map[string]any{"type": "string", "enum": []string{"high", "medium", "low"}},
					"negative": map[string]any{"type": "boolean"}, "notes": mcpString,
				})},
		}),
		Run: mcpProjectTool(qaOpCreateCases),
	},
	{
		Name: "list_plans", Perm: "qa.read",
		Description: "List a project's test plans with their progress.",
		Schema:      mcpObject([]string{"project_id"}, map[string]any{"project_id": mcpProject}),
		Run: mcpProjectTool(func(c *fiber.Ctx, call qaCall, _ mcpNoArgs) (any, error) {
			return qaOpListPlans(c, call)
		}),
	},
	{
		Name: "list_runs", Perm: "qa.read",
		Description: "List the runs (test cycles) of a plan with their progress.",
		Schema:      mcpObject([]string{"project_id", "plan_id"}, map[string]any{"project_id": mcpProject, "plan_id": mcpString}),
		Run: mcpProjectTool(func(c *fiber.Ctx, call qaCall, a mcpPlanArgs) (any, error) {
			return qaOpListRuns(c, call, a.PlanID)
		}),
	},
	{
		Name: "get_run", Perm: "qa.read",
		Description: "Get a run's progress, its assignee_id (the lead tester) and a page of its items (one per case: " +
			"steps, status, assignee, and last_comment: the latest result's comment with who and when, null when there is none; " +
			"100 by default, at most 500; total says how many match), optionally only items in one status.",
		Schema: mcpObject([]string{"project_id", "run_id"}, map[string]any{
			"project_id": mcpProject, "run_id": mcpString,
			"status": map[string]any{"type": "string", "enum": qaItemStatuses},
			"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": qaAPIMaxLimit},
			"offset": map[string]any{"type": "integer", "minimum": 0},
		}),
		Run: mcpProjectTool(qaOpGetRun),
	},
	{
		Name: "record_result", Perm: "qa.execute",
		Description: "Record the result of one run item: passed, failed, blocked or skipped, with an optional comment " +
			"(at most 2500 characters). Returns the item with its new status and last_comment.",
		Schema: mcpObject([]string{"project_id", "item_id", "status"}, map[string]any{
			"project_id": mcpProject, "item_id": mcpString, "comment": mcpString,
			"status": map[string]any{"type": "string", "enum": []string{"passed", "failed", "blocked", "skipped"}},
		}),
		Run: mcpProjectTool(qaOpRecordResult),
	},
	{
		Name: "create_issue", Perm: "qa.issue.report",
		Description: "Report an issue (BUG-n). Give item_id to link it to the run item it was found in (its plan is then filled in).",
		Schema: mcpObject([]string{"project_id", "title", "severity"}, map[string]any{
			"project_id": mcpProject, "title": mcpString, "description": mcpString, "steps_to_reproduce": mcpString,
			"environment": mcpString, "plan_id": mcpString, "item_id": mcpString,
			"severity": map[string]any{"type": "string", "enum": []string{"blocker", "critical", "major", "minor", "trivial"}},
		}),
		Run: mcpProjectTool(qaOpCreateIssue),
	},
}

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type mcpRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func mcpReply(c *fiber.Ctx, id json.RawMessage, result any, rpcErr *mcpRPCError) error {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	out := fiber.Map{"jsonrpc": "2.0", "id": id}
	if rpcErr != nil {
		out["error"] = rpcErr
	} else {
		out["result"] = result
	}
	return c.JSON(out)
}

// mcpToolError is a tool's failure as a result the assistant reads.
func mcpToolError(c *fiber.Ctx, err error) fiber.Map {
	var rows *apiRowErrors
	msg := ""
	switch {
	case errors.As(err, &rows):
		msg = "some cases were refused; nothing was created:\n" + strings.Join(rows.rows, "\n")
	default:
		status, m := apiErrStatus(err)
		msg = m
		if status >= fiber.StatusInternalServerError {
			qaAPILogf("qa mcp: %s %s: %v", c.Method(), c.Path(), err)
		}
		if status == fiber.StatusForbidden {
			msg = "this token lacks the permission for this tool (" + m + ")"
		}
	}
	return fiber.Map{"content": []fiber.Map{{"type": "text", "text": msg}}, "isError": true}
}

func qaMCP() fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req mcpRequest
		if err := json.Unmarshal(c.Body(), &req); err != nil {
			c.Status(fiber.StatusBadRequest)
			return mcpReply(c, nil, nil, &mcpRPCError{Code: -32700, Message: "parse error: send one JSON-RPC message"})
		}
		if req.Method == "" || len(req.ID) == 0 {
			// A notification or a response: accepted, nothing to answer.
			c.Status(fiber.StatusAccepted)
			return nil
		}
		p := mustPrincipal(c)
		switch req.Method {
		case "initialize":
			var params struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			_ = json.Unmarshal(orEmpty(req.Params), &params)
			version := mcpProtocolVersions[0]
			if slices.Contains(mcpProtocolVersions, params.ProtocolVersion) {
				version = params.ProtocolVersion
			}
			return mcpReply(c, req.ID, fiber.Map{
				"protocolVersion": version,
				"capabilities":    fiber.Map{"tools": fiber.Map{"listChanged": false}},
				"serverInfo":      fiber.Map{"name": "lotus-qa", "title": "Lotus QA", "version": "1"},
				"instructions":    mcpInstructions,
			}, nil)
		case "ping":
			return mcpReply(c, req.ID, fiber.Map{}, nil)
		case "tools/list":
			tools := []fiber.Map{}
			for _, t := range mcpTools {
				if p.Can(t.Perm) {
					tools = append(tools, fiber.Map{"name": t.Name, "description": t.Description, "inputSchema": t.Schema})
				}
			}
			return mcpReply(c, req.ID, fiber.Map{"tools": tools}, nil)
		case "tools/call":
			var params struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			if err := json.Unmarshal(orEmpty(req.Params), &params); err != nil {
				return mcpReply(c, req.ID, nil, &mcpRPCError{Code: -32602, Message: "invalid params"})
			}
			i := slices.IndexFunc(mcpTools, func(t mcpTool) bool { return t.Name == params.Name })
			if i < 0 {
				return mcpReply(c, req.ID, nil, &mcpRPCError{Code: -32602, Message: "unknown tool: " + params.Name})
			}
			tool := mcpTools[i]
			if !p.Can(tool.Perm) {
				return mcpReply(c, req.ID, mcpToolError(c, apiErr(fiber.StatusForbidden, "missing permission: %s", tool.Perm)), nil)
			}
			out, err := tool.Run(c, params.Arguments)
			if err != nil {
				return mcpReply(c, req.ID, mcpToolError(c, err), nil)
			}
			text, _ := json.Marshal(out)
			return mcpReply(c, req.ID, fiber.Map{
				"content":           []fiber.Map{{"type": "text", "text": string(text)}},
				"structuredContent": out,
			}, nil)
		default:
			return mcpReply(c, req.ID, nil, &mcpRPCError{Code: -32601, Message: "method not found: " + req.Method})
		}
	}
}

func registerQAMCPRoutes(app *fiber.App, authDeps authn.Deps) {
	app.Post("/mcp/qa", append(qaAPIChain(authDeps, "qa.read"), qaMCP())...)
}
