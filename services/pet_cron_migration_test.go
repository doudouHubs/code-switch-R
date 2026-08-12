package services

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestMigrateOpenCoworkPetCronJobsImportsAtEveryAndCron(t *testing.T) {
	source := newPetCronMigrationSource(t)
	now := time.Date(2026, 8, 12, 8, 0, 0, 0, time.UTC)
	createdAt := now.Add(-time.Minute).UnixMilli()
	insertPetCronMigrationRow(t, source, petCronMigrationRow{
		id: "at-job", scheduleKind: "at", scheduleAt: now.Add(time.Hour).UnixMilli(), scheduleTZ: "UTC",
		payloadJSON: petCronMigrationPayload(t, "at-job", "reminder"), enabled: 1, createdAt: createdAt,
	})
	insertPetCronMigrationRow(t, source, petCronMigrationRow{
		id: "every-job", scheduleKind: "every", scheduleEvery: 60 * 1000, scheduleTZ: "UTC",
		payloadJSON: petCronMigrationPayload(t, "every-job", "action"), enabled: 1, createdAt: createdAt,
	})
	insertPetCronMigrationRow(t, source, petCronMigrationRow{
		id: "cron-job", scheduleKind: "cron", scheduleExpr: "0 9 * * *", scheduleTZ: "UTC",
		payloadJSON: petCronMigrationPayload(t, "cron-job", "reminder"), enabled: 1, createdAt: createdAt,
	})
	insertPetCronMigrationRow(t, source, petCronMigrationRow{
		id: "agent-job", scheduleKind: "at", scheduleAt: now.Add(time.Hour).UnixMilli(), scheduleTZ: "UTC",
		jobType: "agent", payloadJSON: petCronMigrationPayload(t, "agent-job", "action"), enabled: 1, createdAt: createdAt,
	})

	store := newPetCronMigrationTarget(t)
	report, err := MigrateOpenCoworkPetCronJobs(context.Background(), source, store, PetCronMigrationOptions{Now: now})
	if err != nil {
		t.Fatalf("migration error = %v", err)
	}
	if report.Scanned != 3 || report.Imported != 3 || report.Skipped != 0 || report.AlreadyApplied {
		t.Fatalf("report = %#v, want only three pet jobs imported", report)
	}

	jobs := loadPetCronMigrationTargetJobs(t, store)
	if len(jobs) != 3 {
		t.Fatalf("target jobs = %#v, want three jobs", jobs)
	}
	wantDue := map[string]time.Time{
		"at-job":    now.Add(time.Hour),
		"every-job": now.Add(time.Minute),
		"cron-job":  time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC),
	}
	wantKinds := map[string]PetPlanScheduleKind{
		"at-job":    PetPlanScheduleAt,
		"every-job": PetPlanScheduleEvery,
		"cron-job":  PetPlanScheduleCron,
	}
	for id, job := range jobs {
		if job.Schedule.Kind != wantKinds[id] {
			t.Errorf("job %q schedule kind = %q, want %q", id, job.Schedule.Kind, wantKinds[id])
		}
		if !job.DueAt.Equal(wantDue[id].UTC()) || !job.AvailableAt.Equal(wantDue[id].UTC()) {
			t.Errorf("job %q due=%s available=%s, want %s", id, job.DueAt, job.AvailableAt, wantDue[id])
		}
		if job.CreatedAt.UnixMilli() != createdAt || job.JobType != PetAutomationJobType {
			t.Errorf("job %q metadata = %#v, want source created_at and pet type", id, job)
		}
	}
	if jobs["at-job"].Payload.Text != "at-job reminder" || jobs["every-job"].Payload.Action != PetActionFeed {
		t.Fatalf("normalized payloads = %#v/%#v", jobs["at-job"].Payload, jobs["every-job"].Payload)
	}
}

func TestMigrateOpenCoworkPetCronJobsSkipsInvalidAndInactiveRecords(t *testing.T) {
	source := newPetCronMigrationSource(t)
	now := time.Date(2026, 8, 12, 8, 0, 0, 0, time.UTC)
	createdAt := now.Add(-time.Minute).UnixMilli()
	rows := []petCronMigrationRow{
		{id: "bad-payload", scheduleKind: "at", scheduleAt: now.Add(time.Hour).UnixMilli(), payloadJSON: "{\"version\":1}", enabled: 1, createdAt: createdAt},
		{id: "disabled", scheduleKind: "at", scheduleAt: now.Add(time.Hour).UnixMilli(), payloadJSON: petCronMigrationPayload(t, "disabled", "action"), enabled: 0, createdAt: createdAt},
		{id: "deleted", scheduleKind: "at", scheduleAt: now.Add(time.Hour).UnixMilli(), payloadJSON: petCronMigrationPayload(t, "deleted", "action"), enabled: 1, deletedAt: now.UnixMilli(), createdAt: createdAt},
		{id: "past-at", scheduleKind: "at", scheduleAt: now.Add(-time.Millisecond).UnixMilli(), payloadJSON: petCronMigrationPayload(t, "past-at", "action"), enabled: 1, createdAt: createdAt},
		{id: "small-every", scheduleKind: "every", scheduleEvery: PetPlanMinIntervalMS - 1, payloadJSON: petCronMigrationPayload(t, "small-every", "action"), enabled: 1, createdAt: createdAt},
		{id: "bad-cron", scheduleKind: "cron", scheduleExpr: "not a cron", payloadJSON: petCronMigrationPayload(t, "bad-cron", "action"), enabled: 1, createdAt: createdAt},
		{id: "bad-tz", scheduleKind: "cron", scheduleExpr: "0 9 * * *", scheduleTZ: "Mars/Nope", payloadJSON: petCronMigrationPayload(t, "bad-tz", "action"), enabled: 1, createdAt: createdAt},
		{id: "missing-at", scheduleKind: "at", payloadJSON: petCronMigrationPayload(t, "missing-at", "action"), enabled: 1, createdAt: createdAt},
	}
	for _, row := range rows {
		insertPetCronMigrationRow(t, source, row)
	}

	store := newPetCronMigrationTarget(t)
	report, err := MigrateOpenCoworkPetCronJobs(context.Background(), source, store, PetCronMigrationOptions{Now: now})
	if err != nil {
		t.Fatalf("migration error = %v", err)
	}
	if report.Scanned != len(rows) || report.Imported != 0 || report.Skipped != len(rows) || report.AlreadyApplied {
		t.Fatalf("report = %#v, want all records skipped and not already applied", report)
	}
	if len(loadPetCronMigrationTargetJobs(t, store)) != 0 {
		t.Fatal("invalid or inactive records wrote target jobs")
	}
	for _, row := range rows {
		found := false
		for _, diagnostic := range report.Diagnostics {
			if diagnostic.ID == row.id && diagnostic.Source == report.SourceDB && strings.TrimSpace(diagnostic.Reason) != "" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing diagnostic for source id %q: %#v", row.id, report.Diagnostics)
		}
	}
}

func TestMigrateOpenCoworkPetCronJobsHandlesMissingDBAndTable(t *testing.T) {
	store := newPetCronMigrationTarget(t)
	missingReport, err := MigrateOpenCoworkPetCronJobs(context.Background(), t.TempDir(), store, PetCronMigrationOptions{Now: time.Now().UTC()})
	if err != nil {
		t.Fatalf("missing DB migration error = %v", err)
	}
	if missingReport.Scanned != 0 || missingReport.Imported != 0 || len(missingReport.Diagnostics) != 1 {
		t.Fatalf("missing DB report = %#v", missingReport)
	}

	emptySource := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(emptySource, "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE unrelated (id TEXT PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	missingTableReport, err := MigrateOpenCoworkPetCronJobs(context.Background(), emptySource, store, PetCronMigrationOptions{Now: time.Now().UTC()})
	if err != nil {
		t.Fatalf("missing table migration error = %v", err)
	}
	if missingTableReport.Scanned != 0 || missingTableReport.Imported != 0 || len(missingTableReport.Diagnostics) != 1 || !strings.Contains(missingTableReport.Diagnostics[0].Reason, "cron_jobs") {
		t.Fatalf("missing table report = %#v", missingTableReport)
	}
}

func TestMigrateOpenCoworkPetCronJobsIsIdempotentAndPreservesTargetIDConflict(t *testing.T) {
	source := newPetCronMigrationSource(t)
	now := time.Date(2026, 8, 12, 8, 0, 0, 0, time.UTC)
	insertPetCronMigrationRow(t, source, petCronMigrationRow{
		id: "same-id", scheduleKind: "at", scheduleAt: now.Add(time.Hour).Format(time.RFC3339), scheduleTZ: "UTC",
		payloadJSON: petCronMigrationPayload(t, "same-id", "action"), enabled: 1, createdAt: now.UnixMilli(),
	})
	store := newPetCronMigrationTarget(t)

	first, err := MigrateOpenCoworkPetCronJobs(context.Background(), source, store, PetCronMigrationOptions{Now: now})
	if err != nil || first.Imported != 1 {
		t.Fatalf("first migration report=%#v err=%v", first, err)
	}
	second, err := MigrateOpenCoworkPetCronJobs(context.Background(), source, store, PetCronMigrationOptions{Now: now.Add(time.Minute)})
	if err != nil {
		t.Fatalf("second migration error = %v", err)
	}
	if second.Imported != 0 || second.Skipped != 1 || !second.AlreadyApplied {
		t.Fatalf("second migration report = %#v, want one existing ID and AlreadyApplied", second)
	}
	if len(loadPetCronMigrationTargetJobs(t, store)) != 1 {
		t.Fatal("rerun created a duplicate target job")
	}
}

type petCronMigrationRow struct {
	id            string
	scheduleKind  string
	scheduleAt    any
	scheduleEvery any
	scheduleExpr  string
	scheduleTZ    string
	jobType       string
	payloadJSON   string
	enabled       int
	deleteAfter   int
	deletedAt     any
	createdAt     int64
}

func newPetCronMigrationSource(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(root, "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`CREATE TABLE cron_jobs (
        id TEXT PRIMARY KEY,
        schedule_kind TEXT NOT NULL,
        schedule_at,
        schedule_every INTEGER,
        schedule_expr TEXT,
        schedule_tz TEXT DEFAULT 'UTC',
        job_type TEXT DEFAULT 'agent',
        payload_json TEXT,
        enabled INTEGER DEFAULT 1,
        delete_after_run INTEGER DEFAULT 0,
        deleted_at INTEGER,
        created_at INTEGER NOT NULL
    )`)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func insertPetCronMigrationRow(t *testing.T, root string, row petCronMigrationRow) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(root, "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if row.jobType == "" {
		row.jobType = "pet"
	}
	if _, err := db.Exec(`INSERT INTO cron_jobs
        (id, schedule_kind, schedule_at, schedule_every, schedule_expr, schedule_tz,
         job_type, payload_json, enabled, delete_after_run, deleted_at, created_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.id, row.scheduleKind, row.scheduleAt, row.scheduleEvery, row.scheduleExpr, row.scheduleTZ,
		row.jobType, row.payloadJSON, row.enabled, row.deleteAfter, row.deletedAt, row.createdAt); err != nil {
		t.Fatal(err)
	}
}

func petCronMigrationPayload(t *testing.T, id, kind string) string {
	t.Helper()
	payload := map[string]any{
		"version":   1,
		"planId":    "plan-" + id,
		"stepId":    "step-" + id,
		"kind":      kind,
		"createdAt": float64(time.Now().UnixMilli()),
	}
	if kind == "action" {
		payload["action"] = "feed"
	} else {
		payload["text"] = id + " reminder"
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func newPetCronMigrationTarget(t *testing.T) *PetSQLiteJobStore {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(t.TempDir(), "target.db")))
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewPetSQLiteJobStore(db)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return store
}

func loadPetCronMigrationTargetJobs(t *testing.T, store *PetSQLiteJobStore) map[string]PetScheduledJob {
	t.Helper()
	rows, err := store.db.Query(`SELECT id, job_type, plan_id, step_id, schedule_json, payload_json,
        created_at, due_at, available_at, expires_at, max_attempts, attempt, state,
        lease_token, lease_until, last_error FROM pet_scheduler_jobs`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	jobs := make(map[string]PetScheduledJob)
	for rows.Next() {
		record, err := scanPetSQLiteJobRecord(rows)
		if err != nil {
			t.Fatal(err)
		}
		jobs[record.job.ID] = record.job
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return jobs
}
