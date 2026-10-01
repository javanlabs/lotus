package web

import (
	"fmt"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/uyab/lotus/internal/httpx"
	"github.com/uyab/lotus/internal/repo"
	"github.com/uyab/lotus/internal/repo/global"
)

// PIC (person in charge) of a section or a case. Who can be one is who can
// be given run items (qaPeople): owners, admins, members, and guests listed
// on the project; viewers never. A case without its own PIC is in the
// charge of its nearest section's (repo.QAEffectivePIC).

// errQAPIC: the PIC sent is not someone who can be one here.
var errQAPIC = fmt.Errorf("%w: not someone who can be PIC here", repo.ErrInvalidInput)

const qaPICErrMessage = "That person can't be the PIC here."

// qaPICFrom reads a PIC picker: "" is nobody; anything else must be one of
// people — or keep, the current PIC, who may have left or become a viewer
// since and must not block saving the rest of the form.
func qaPICFrom(people []qaPerson, raw string, keep *uuid.UUID) (*uuid.UUID, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if keep != nil && raw == keep.String() {
		return keep, nil
	}
	for _, p := range people {
		if p.ID.String() == raw {
			id := p.ID
			return &id, nil
		}
	}
	return nil, errQAPIC
}

// qaPICByEmail is qaPICFrom for a CSV cell or an API field, which name
// the PIC by email (ignoring case).
func qaPICByEmail(people []qaPerson, email string) (*uuid.UUID, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return nil, nil
	}
	for _, p := range people {
		if strings.EqualFold(p.Email, email) {
			id := p.ID
			return &id, nil
		}
	}
	return nil, errQAPIC
}

// qaPICOptions is the PIC picker: nobody (labelled none), everyone in
// people, and current when it is no longer among them.
func qaPICOptions(people []qaPerson, none string, current *uuid.UUID, labels map[uuid.UUID]string) []httpx.SelectOption {
	out := []httpx.SelectOption{{Value: "", Label: none}}
	listed := false
	for _, p := range people {
		out = append(out, httpx.SelectOption{Value: p.ID.String(), Label: p.Label})
		listed = listed || (current != nil && p.ID == *current)
	}
	if current != nil && !listed {
		out = append(out, httpx.SelectOption{Value: current.String(), Label: personLabel(labels, current) + " (no longer assignable)"})
	}
	return out
}

func qaUserLabel(u global.User) string {
	switch {
	case strings.TrimSpace(u.Name) != "":
		return u.Name
	case u.Email != nil:
		return *u.Email
	}
	return "—"
}

// qaUserLabels names every user in ids that exists, member of the
// organization or not: a PIC or author may have left since.
func qaUserLabels(c *fiber.Ctx, d Deps, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	users, err := global.UsersByIDs(c.Context(), d.DB, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]string, len(users))
	for id, u := range users {
		out[id] = qaUserLabel(u)
	}
	return out, nil
}

// qaPICView is a PIC as shown: who, and the section it comes from when
// inherited ("" when the PIC is the row's own).
type qaPICView struct {
	Label string
	From  string
}

// qaPICIDs collects the PICs and authors to name for a page.
func qaPICIDs(sections []repo.QASection, ids ...*uuid.UUID) []uuid.UUID {
	seen := map[uuid.UUID]bool{}
	var out []uuid.UUID
	add := func(id *uuid.UUID) {
		if id != nil && !seen[*id] {
			seen[*id] = true
			out = append(out, *id)
		}
	}
	for _, s := range sections {
		add(s.PicID)
	}
	for _, id := range ids {
		add(id)
	}
	return out
}

// qaEffectivePICView is the PIC of a case filed in sectionID with its own
// PIC own, nil when nobody is in charge.
func qaEffectivePICView(sections []repo.QASection, sectionID uuid.UUID, own *uuid.UUID, labels map[uuid.UUID]string) *qaPICView {
	pic, from := repo.QAEffectivePIC(sections, sectionID, own)
	if pic == nil {
		return nil
	}
	v := &qaPICView{Label: personLabel(labels, pic)}
	if from != nil {
		v.From = from.Name
	}
	return v
}

// qaPICEmails is the email of each case's own PIC, for the CSV export
// (cases without one, or whose PIC has no email, are absent).
func qaPICEmails(c *fiber.Ctx, d Deps, cases []repo.QACase) (map[uuid.UUID]string, error) {
	var ids []*uuid.UUID
	for _, qc := range cases {
		ids = append(ids, qc.PicID)
	}
	users, err := global.UsersByIDs(c.Context(), d.DB, qaPICIDs(nil, ids...))
	if err != nil {
		return nil, err
	}
	out := map[uuid.UUID]string{}
	for _, qc := range cases {
		if qc.PicID == nil {
			continue
		}
		if u, ok := users[*qc.PicID]; ok && u.Email != nil {
			out[qc.ID] = *u.Email
		}
	}
	return out, nil
}
