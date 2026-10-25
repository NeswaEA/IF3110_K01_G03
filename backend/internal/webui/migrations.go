package webui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/connections"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/migrations"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/platform"
)

type MigrationSource interface {
	ListDrafts(context.Context, contracts.Principal, string, int, int) ([]migrations.MigrationDraft, bool, error)
	GetDraft(context.Context, contracts.Principal, string, string) (migrations.MigrationDraft, error)
	ListRuns(context.Context, contracts.Principal, string, int, int) ([]migrations.MigrationRun, bool, error)
	GetRun(context.Context, contracts.Principal, string, string) (migrations.MigrationRun, error)
	Head(context.Context, contracts.Principal, string) (migrations.MigrationHead, error)
}

func (p *Pages) WithMigrations(source MigrationSource) *Pages {
	p.migrationSource = source
	return p
}

const migrationPageSize = 50

type migrationList[T any] struct {
	Data []T           `json:"data"`
	Page platform.Page `json:"page"`
}

type migrationInitial struct {
	Database      connections.Database                      `json:"database"`
	Drafts        *migrationList[migrations.MigrationDraft] `json:"drafts"`
	History       migrationList[migrations.MigrationRun]    `json:"history"`
	Head          *migrations.MigrationHead                 `json:"head"`
	HeadError     string                                    `json:"head_error"`
	Draft         *migrations.MigrationDraft                `json:"draft"`
	Run           *migrations.MigrationRun                  `json:"run"`
	DraftOffset   int                                       `json:"draft_offset"`
	HistoryOffset int                                       `json:"history_offset"`
}

type migrationDraftRow struct {
	migrations.MigrationDraft
	URL string
}

type migrationRunRow struct {
	migrations.MigrationRun
	URL, Status, Started, Finished string
}

type migrationsView struct {
	ID                   string
	Database             databaseItem
	CanEdit              bool
	Drafts               []migrationDraftRow
	Runs                 []migrationRunRow
	Draft                *migrations.MigrationDraft
	Run                  *migrationRunRow
	Head                 *migrations.MigrationHead
	HeadProblem          string
	DraftPrev, DraftNext string
	RunPrev, RunNext     string
	NewDraftURL          string
	Initial              string
}

type migrationQuery struct {
	draft, run                 string
	draftOffset, historyOffset int
}

func parseMigrationQuery(values url.Values) (migrationQuery, error) {
	q := migrationQuery{draft: values.Get("draft"), run: values.Get("run")}
	for _, key := range []string{"draft", "run", "draft_offset", "history_offset"} {
		if len(values[key]) > 1 {
			return q, errors.New("duplicate migration parameter")
		}
	}
	if (q.draft != "" && !contracts.ValidID(q.draft)) || (q.run != "" && !contracts.ValidID(q.run)) {
		return q, errors.New("invalid migration id")
	}
	for key, dest := range map[string]*int{"draft_offset": &q.draftOffset, "history_offset": &q.historyOffset} {
		if value := values.Get(key); value != "" {
			n, err := strconv.Atoi(value)
			if err != nil || n < 0 || n > 100000 {
				return q, errors.New("invalid migration offset")
			}
			*dest = n
		}
	}
	return q, nil
}

func (q migrationQuery) url(databaseID string) string {
	values := url.Values{}
	for key, value := range map[string]string{"draft": q.draft, "run": q.run} {
		if value != "" {
			values.Set(key, value)
		}
	}
	for key, value := range map[string]int{"draft_offset": q.draftOffset, "history_offset": q.historyOffset} {
		if value > 0 {
			values.Set(key, strconv.Itoa(value))
		}
	}
	path := "/databases/" + databaseID + "/migrations"
	if len(values) > 0 {
		path += "?" + values.Encode()
	}
	return path
}

func (p *Pages) migrations(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	user, ok := p.authorize(w, r, contracts.ViewDatabase, id)
	if !ok {
		return
	}
	q, err := parseMigrationQuery(r.URL.Query())
	if err != nil {
		p.render.Status(w, http.StatusBadRequest, user)
		return
	}
	principal, _ := p.principal(r.Context())
	db, err := p.databases.Database(r.Context(), p.access, principal.UserID, id)
	if err != nil {
		p.targetFailure(w, r, user, err)
		return
	}
	canEdit := db.EffectiveAccess == contracts.AccessEdit
	if q.draft != "" && !canEdit {
		p.render.Status(w, http.StatusForbidden, user)
		return
	}
	if p.migrationSource == nil {
		p.render.Status(w, http.StatusInternalServerError, user)
		return
	}
	initial := migrationInitial{Database: db, DraftOffset: q.draftOffset, HistoryOffset: q.historyOffset}
	view := migrationsView{ID: id, Database: databaseItem{db, accessLabels[db.EffectiveAccess]}, CanEdit: canEdit}
	if canEdit {
		drafts, more, err := p.migrationSource.ListDrafts(r.Context(), principal, id, migrationPageSize, q.draftOffset)
		if err != nil {
			p.fail(w, r, user, err)
			return
		}
		if drafts == nil {
			drafts = []migrations.MigrationDraft{}
		}
		initial.Drafts = &migrationList[migrations.MigrationDraft]{drafts, platform.Page{Limit: migrationPageSize, Offset: q.draftOffset, HasMore: more}}
		for _, draft := range drafts {
			link := q
			link.draft = draft.ID
			view.Drafts = append(view.Drafts, migrationDraftRow{draft, link.url(id) + "#migration-draft"})
		}
		if q.draftOffset > 0 {
			prev := q
			prev.draftOffset = max(0, q.draftOffset-migrationPageSize)
			view.DraftPrev = prev.url(id)
		}
		if more && q.draftOffset+migrationPageSize <= 100000 {
			next := q
			next.draftOffset += migrationPageSize
			view.DraftNext = next.url(id)
		}
		if q.draft != "" {
			draft, err := p.migrationSource.GetDraft(r.Context(), principal, id, q.draft)
			if err != nil {
				p.fail(w, r, user, err)
				return
			}
			initial.Draft, view.Draft = &draft, &draft
		}
	}
	runs, more, err := p.migrationSource.ListRuns(r.Context(), principal, id, migrationPageSize, q.historyOffset)
	if err != nil {
		p.fail(w, r, user, err)
		return
	}
	if runs == nil {
		runs = []migrations.MigrationRun{}
	}
	initial.History = migrationList[migrations.MigrationRun]{runs, platform.Page{Limit: migrationPageSize, Offset: q.historyOffset, HasMore: more}}
	for _, run := range runs {
		link := q
		link.run = run.ID
		view.Runs = append(view.Runs, runRow(run, link.url(id)+"#migration-run"))
	}
	if q.historyOffset > 0 {
		prev := q
		prev.historyOffset = max(0, q.historyOffset-migrationPageSize)
		view.RunPrev = prev.url(id)
	}
	if more && q.historyOffset+migrationPageSize <= 100000 {
		next := q
		next.historyOffset += migrationPageSize
		view.RunNext = next.url(id)
	}
	if q.run != "" {
		run, err := p.migrationSource.GetRun(r.Context(), principal, id, q.run)
		if err != nil {
			p.fail(w, r, user, err)
			return
		}
		row := runRow(run, "")
		initial.Run, view.Run = &run, &row
	}
	head, err := p.migrationSource.Head(r.Context(), principal, id)
	if err == nil {
		initial.Head, view.Head = &head, &head
	} else {
		var domain *contracts.DomainError
		if errors.As(err, &domain) && (domain.Code == "UNAUTHENTICATED" || domain.Code == "FORBIDDEN" || domain.Code == "NOT_FOUND") {
			p.fail(w, r, user, err)
			return
		}
		view.HeadProblem = "Versi committed belum dapat dibaca dari target. Riwayat tersimpan tetap tersedia."
		initial.HeadError = view.HeadProblem
	}
	newDraft := q
	newDraft.draft = ""
	view.NewDraftURL = newDraft.url(id) + "#migration-draft"
	encoded, err := json.Marshal(initial)
	if err != nil {
		p.render.Status(w, http.StatusInternalServerError, user)
		return
	}
	view.Initial = string(encoded)
	p.render.Render(w, http.StatusOK, "migrations/index", Page{Title: "Migrasi", User: user, Active: "databases", Entry: "migrations", Data: view})
}

func runRow(run migrations.MigrationRun, link string) migrationRunRow {
	status := map[string]string{migrations.Running: "Berjalan", migrations.Succeeded: "Berhasil", migrations.Failed: "Gagal", migrations.OutcomeUnknown: "Hasil belum pasti"}[run.Status]
	row := migrationRunRow{MigrationRun: run, URL: link, Status: status, Started: run.StartedAt.UTC().Format("02 Jan 2006 15:04 UTC"), Finished: "belum tersedia"}
	if run.FinishedAt != nil {
		row.Finished = run.FinishedAt.UTC().Format("02 Jan 2006 15:04 UTC")
	}
	return row
}
