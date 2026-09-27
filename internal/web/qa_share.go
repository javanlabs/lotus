package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/authz"
	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/tenant"
)

// The plan report: what a client signs off against. Signed in, anyone who
// reads QA opens it at …/plans/:pid/report; qa.share hands out a link
// (/s/qa/:token) that opens the same report with no login, read-only,
// until it expires (30 days) or is revoked. The report holds no forms, no
// attachments and no links into the app.

type qaReportScenario struct {
	Name       string
	PhaseLabel string
	Progress   repo.QAProgress
}

type qaReportRun struct {
	Name     string
	Cycle    int
	Closed   bool
	Progress repo.QAProgress
}

type qaReportIssue struct {
	Key          int
	Title        string
	Severity     string
	SeverityTone string
	Status       string
}

type qaReportData struct {
	ProjectName string
	Plan        repo.QAPlan
	PhaseLabel  string
	Window      string
	Progress    repo.QAProgress
	Scenarios   []qaReportScenario
	Runs        []qaReportRun
	Issues      []qaReportIssue
	Signoffs    []qaSignoffRow
	Shared      bool // opened through a share link
	Generated   string
	ExpiresOn   string // the share link's last day
}

// buildQAReport gathers the report; shared is true for a share link, whose
// holder is nobody in particular (see qaPlanSignoffs).
func buildQAReport(c *fiber.Ctx, d Deps, s tenant.Scope, project *repo.Project, plan *repo.QAPlan, shared bool) (qaReportData, error) {
	data := qaReportData{ProjectName: project.Name, Plan: *plan, Shared: shared, PhaseLabel: qaPhaseLabels[plan.Phase],
		Window: qaWindow(plan.StartsOn, plan.EndsOn), Generated: time.Now().Format("02 Jan 2006 15:04")}
	progress, err := repo.QAPlanProgress(c.Context(), s, []uuid.UUID{plan.ID})
	if err != nil {
		return data, err
	}
	data.Progress = progress[plan.ID]

	ids, err := repo.ListQAPlanScenarioIDs(c.Context(), s, plan.ID)
	if err != nil {
		return data, err
	}
	inPlan := map[uuid.UUID]bool{}
	for _, id := range ids {
		inPlan[id] = true
	}
	all, err := repo.ListQAScenarios(c.Context(), s, project.ID, true)
	if err != nil {
		return data, err
	}
	perScenario, err := repo.QAPlanScenarioProgress(c.Context(), s, plan.ID)
	if err != nil {
		return data, err
	}
	var rows []qaScenarioRow
	for _, sc := range all {
		if inPlan[sc.ID] {
			rows = append(rows, qaScenarioRow{QAScenario: sc})
			data.Scenarios = append(data.Scenarios, qaReportScenario{Name: sc.Name, PhaseLabel: qaPhaseLabels[sc.Phase], Progress: perScenario[sc.ID]})
		}
	}
	if data.Signoffs, err = qaPlanSignoffs(c, d, s, plan.ID, rows, shared); err != nil {
		return data, err
	}

	runs, err := repo.ListQARuns(c.Context(), s, plan.ID)
	if err != nil {
		return data, err
	}
	runIDs := make([]uuid.UUID, len(runs))
	for i, r := range runs {
		runIDs[i] = r.ID
	}
	runProgress, err := repo.QARunProgressFor(c.Context(), s, runIDs)
	if err != nil {
		return data, err
	}
	for _, r := range runs {
		data.Runs = append(data.Runs, qaReportRun{Name: r.Name, Cycle: r.Cycle, Closed: r.ClosedAt != nil, Progress: runProgress[r.ID]})
	}

	issues, err := repo.ListQAIssues(c.Context(), s, project.ID, repo.QAIssueFilter{Statuses: repo.QAOpenIssueStatuses, PlanID: &plan.ID})
	if err != nil {
		return data, err
	}
	for _, is := range issues {
		data.Issues = append(data.Issues, qaReportIssue{Key: is.Key, Title: is.Title, Severity: is.Severity,
			SeverityTone: qaSeverityTone(is.Severity), Status: is.Status})
	}
	return data, nil
}

func renderQAReport(c *fiber.Ctx, d Deps, data qaReportData) error {
	c.Type("html")
	return renderPage(c, d, "pages/qa_report.html", httpx.ViewData{
		Title: data.Plan.Name + " · " + data.ProjectName, Data: data,
	})
}

// qaReportPage is GET …/plans/:pid/report (qa.read).
func qaReportPage(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, err := loadQAProject(c)
		if err != nil {
			return err
		}
		plan, err := loadQAPlan(c, s, project)
		if err != nil {
			return err
		}
		data, err := buildQAReport(c, d, s, project, plan, false)
		if err != nil {
			return writeRepoErr(c, err)
		}
		return renderQAReport(c, d, data)
	}
}

var qaShareTokenPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func qaShareTokenHash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// qaSharedReport is GET /s/qa/:token: no session, the token is the whole
// authorization, and it answers for one plan's report only. Anything wrong
// with it (unknown, revoked, expired, another organization's) is 404.
func qaSharedReport(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		// A link is a secret: not cached, not indexed, not leaked onward.
		c.Set("Cache-Control", "no-store")
		c.Set("X-Robots-Tag", "noindex, nofollow")
		c.Set("Referrer-Policy", "no-referrer")
		token := c.Params("token")
		if !qaShareTokenPattern.MatchString(token) {
			return fiber.ErrNotFound
		}
		s, err := mustScope(c)
		if err != nil {
			return err
		}
		link, err := repo.QAShareLinkByHash(c.Context(), s, qaShareTokenHash(token))
		if errors.Is(err, repo.ErrNotFound) {
			return fiber.ErrNotFound
		}
		if err != nil {
			return writeRepoErr(c, err)
		}
		plan, err := repo.GetQAPlan(c.Context(), s, link.PlanID)
		if err != nil {
			return writeRepoErr(c, err)
		}
		project, err := repo.ProjectByID(c.Context(), s, plan.ProjectID)
		if err != nil {
			return writeRepoErr(c, err)
		}
		data, err := buildQAReport(c, d, s, project, plan, true)
		if err != nil {
			return writeRepoErr(c, err)
		}
		data.ExpiresOn = link.ExpiresAt.Format("02 Jan 2006")
		return renderQAReport(c, d, data)
	}
}

type qaShareRow struct {
	ID        uuid.UUID
	CreatedBy string
	CreatedAt string
	ExpiresOn string
}

// qaPlanShares lists planID's live links for the plan page.
func qaPlanShares(c *fiber.Ctx, d Deps, s tenant.Scope, planID uuid.UUID) ([]qaShareRow, error) {
	links, err := repo.ListQAShareLinks(c.Context(), s, planID)
	if err != nil || len(links) == 0 {
		return nil, err
	}
	labels, err := qaPeopleLabels(c, d, s)
	if err != nil {
		return nil, err
	}
	out := make([]qaShareRow, len(links))
	for i, l := range links {
		out[i] = qaShareRow{ID: l.ID, CreatedBy: personLabel(labels, l.CreatedBy),
			CreatedAt: l.CreatedAt.Format("02 Jan 2006"), ExpiresOn: l.ExpiresAt.Format("02 Jan 2006")}
	}
	return out, nil
}

// createQAShareUI makes a link and shows it once: only its hash is kept.
func createQAShareUI(d Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, err := loadQAProject(c)
		if err != nil {
			return err
		}
		plan, err := loadQAPlan(c, s, project)
		if err != nil {
			return err
		}
		raw, hash, err := newInvitationToken()
		if err != nil {
			return err
		}
		link, err := repo.CreateQAShareLink(c.Context(), s, plan.ID, hash, qaActor(c))
		if err != nil {
			return writeRepoErr(c, err)
		}
		audit, _ := json.Marshal(fiber.Map{"plan_id": plan.ID, "link_id": link.ID, "expires_at": link.ExpiresAt})
		if err := repo.RecordAuditEvent(c.Context(), s, qaActor(c), "qa.share.create", "qa_plan", &plan.ID, audit); err != nil {
			return writeRepoErr(c, err)
		}
		org, ok := tenant.OrgFromContext(c)
		if !ok {
			return fiber.ErrNotFound
		}
		c.Set("Cache-Control", "no-store")
		c.Type("html")
		return d.Renderer.RenderNamed(c, "qa_share_created", fiber.Map{
			"Link": d.Cfg.TenantURL(org.Subdomain) + "/s/qa/" + raw, "ExpiresOn": link.ExpiresAt.Format("02 Jan 2006"),
			"Back": planURL(project, plan),
		})
	}
}

func revokeQAShareUI() fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, project, err := loadQAProject(c)
		if err != nil {
			return err
		}
		plan, err := loadQAPlan(c, s, project)
		if err != nil {
			return err
		}
		id, err := uuid.Parse(c.Params("lid"))
		if err != nil {
			return fiber.ErrNotFound
		}
		if err := repo.RevokeQAShareLink(c.Context(), s, plan.ID, id); err != nil {
			if errors.Is(err, repo.ErrNotFound) {
				return fiber.ErrNotFound
			}
			return writeRepoErr(c, err)
		}
		audit, _ := json.Marshal(fiber.Map{"plan_id": plan.ID, "link_id": id})
		if err := repo.RecordAuditEvent(c.Context(), s, qaActor(c), "qa.share.revoke", "qa_plan", &plan.ID, audit); err != nil {
			return writeRepoErr(c, err)
		}
		setFlash(c, "qa.share.revoked")
		return redirectAfterMutation(c, planURL(project, plan))
	}
}

func registerQAShareRoutes(app *fiber.App, d Deps, requireSession, loadPrincipal fiber.Handler) {
	app.Get("/s/qa/:token", tenant.RequireTenant(),
		authz.Public("QA share link: the token is checked by the handler and opens one plan report only"),
		qaSharedReport(d))
	app.Get("/projects/:id/qa/plans/:pid/report", qaRoute(requireSession, loadPrincipal, "qa.read", qaReportPage(d))...)
	app.Post("/projects/:id/qa/plans/:pid/share", qaRoute(requireSession, loadPrincipal, "qa.share", createQAShareUI(d))...)
	app.Post("/projects/:id/qa/plans/:pid/share/:lid/revoke", qaRoute(requireSession, loadPrincipal, "qa.share", revokeQAShareUI())...)
}
