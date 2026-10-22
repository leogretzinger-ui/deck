package db

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixedNow is the constant timestamp stamped onto every created/invalidated
// column by the migration-020 helpers and inline statements. No test asserts
// on the value; it pins the suite to a fixed instant instead of the wall
// clock, which time.Now would read silently.
var fixedNow = time.Date(2026, time.August, 12, 12, 0, 0, 0, time.UTC)

// hex32 returns a canonical lowercase 32-hex identifier embedding n. It exists
// only to keep the migration-020 tests readable: every value it produces
// satisfies the identifier CHECK, while hand-written constants invite a typo
// that would make a negative test pass for the wrong reason.
func hex32(n int) string { return fmt.Sprintf("%032x", n) }

// hex64 returns a canonical lowercase 64-hex digest embedding n.
func hex64(n int) string { return fmt.Sprintf("%064x", n) }

// seedDomain inserts an active security domain and returns its id.
func seedDomain(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	now := FormatTime(fixedNow)
	if _, err := db.Exec(
		`INSERT INTO project_security_domains
		     (security_domain_id, display_label, created_at, created_by)
		 VALUES (?, ?, ?, ?)`,
		id, "domain-"+id, now, "operator"); err != nil {
		t.Fatalf("seed domain %s: %v", id, err)
	}
	return id
}

// seedRevision inserts an active policy revision and returns its id.
func seedRevision(t *testing.T, db *sql.DB, id, domainID string, no int) string {
	t.Helper()
	now := FormatTime(fixedNow)
	if _, err := db.Exec(
		`INSERT INTO project_policy_revisions
		     (revision_id, security_domain_id, revision_no, policy_digest,
		      canonical_json, created_at, created_by)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, domainID, no, hex64(no),
		fmt.Sprintf(`{"revision":%d}`, no), now, "operator"); err != nil {
		t.Fatalf("seed revision %s: %v", id, err)
	}
	return id
}

// seedAttestation inserts an active backing attestation and returns its id.
func seedAttestation(t *testing.T, db *sql.DB, id, revisionID, domainID string) string {
	t.Helper()
	now := FormatTime(fixedNow)
	if _, err := db.Exec(
		`INSERT INTO project_backing_attestations
		     (attestation_id, revision_id, security_domain_id, backing_digest,
		      canonical_json, attested_at, attested_by)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, revisionID, domainID, hex64(700), `{"backing":"v1"}`, now, "supervisor"); err != nil {
		t.Fatalf("seed attestation %s: %v", id, err)
	}
	return id
}

// seedApproval inserts an active approval bound to attestationID/revisionID and
// returns its id.
func seedApproval(t *testing.T, db *sql.DB, id, attestationID, revisionID, domainID string) string {
	t.Helper()
	now := FormatTime(fixedNow)
	if _, err := db.Exec(
		`INSERT INTO project_policy_approvals
		     (approval_id, attestation_id, revision_id, security_domain_id,
		      approved_at, approved_by)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		id, attestationID, revisionID, domainID, now, "operator"); err != nil {
		t.Fatalf("seed approval %s: %v", id, err)
	}
	return id
}

// invalidate sets the one-way invalidation tuple on the row selected by where.
func invalidate(t *testing.T, db *sql.DB, table, idColumn, id, reason string, evidence int) {
	t.Helper()
	now := FormatTime(fixedNow)
	if _, err := db.Exec(
		fmt.Sprintf(
			`UPDATE %s SET invalidated_reason = ?, invalidated_evidence = ?,
			        invalidated_at = ?, invalidated_by = ?
			  WHERE %s = ?`, table, idColumn),
		reason, hex64(evidence), now, "operator", id); err != nil {
		t.Fatalf("invalidate %s %s: %v", table, id, err)
	}
}

// seedChain builds one complete active authority chain and returns its ids.
func seedChain(t *testing.T, db *sql.DB) (domain, revision, attestation, approval string) {
	t.Helper()
	domain = seedDomain(t, db, hex32(1))
	revision = seedRevision(t, db, hex32(2), domain, 1)
	attestation = seedAttestation(t, db, hex32(3), revision, domain)
	approval = seedApproval(t, db, hex32(4), attestation, revision, domain)
	return
}

// TestMigrate020SchemaTablesIndexes applies the full migration set and proves
// the five protected tables, their indexes, and their triggers exist, and that
// STRICT rejects a wrong storage class on a new table.
func TestMigrate020SchemaTablesIndexes(t *testing.T) {
	db, _ := openMigrated(t)

	for _, table := range []string{
		"project_security_domains", "project_domain_memberships",
		"project_policy_revisions", "project_backing_attestations",
		"project_policy_approvals",
	} {
		var name string
		if err := db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table,
		).Scan(&name); err != nil {
			t.Errorf("table %q missing after migrate: %v", table, err)
		}
	}

	for _, idx := range []string{
		"idx_pba_active_per_revision", "idx_ppa_active_per_attestation",
		"idx_pdm_active_per_project", "idx_psd_evidence_unique",
		"idx_pdm_evidence_unique", "idx_ppr_evidence_unique",
		"idx_pba_evidence_unique", "idx_ppa_evidence_unique",
	} {
		var name string
		if err := db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='index' AND name=?`, idx,
		).Scan(&name); err != nil {
			t.Errorf("index %q missing after migrate: %v", idx, err)
		}
	}

	for _, trg := range []string{
		"trg_pba_binds_revision_domain",
		"trg_ppa_binds_active_attestation", "trg_pba_invalidation_order",
		"trg_psd_immutable", "trg_pdm_immutable", "trg_ppr_immutable",
		"trg_pba_immutable", "trg_ppa_immutable",
		"trg_psd_nodelete", "trg_pdm_nodelete", "trg_ppr_nodelete",
		"trg_pba_nodelete", "trg_ppa_nodelete",
	} {
		var name string
		if err := db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='trigger' AND name=?`, trg,
		).Scan(&name); err != nil {
			t.Errorf("trigger %q missing after migrate: %v", trg, err)
		}
	}

	// STRICT: a TEXT identifier column rejects an integer storage class.
	if _, err := db.Exec(
		`INSERT INTO project_security_domains
		     (security_domain_id, display_label, created_at, created_by)
		 VALUES (123, 'x', '2026-08-12T00:00:00.000Z', 'op')`); err == nil {
		t.Errorf("STRICT table accepted an integer identifier")
	}
}

// TestMigrate020UpgradesSchema019AndPreservesData builds a genuine schema-019
// database with a project, session, and audit row, applies 020, and proves the
// existing data survives, no memberships appear, and the new tables are empty.
func TestMigrate020UpgradesSchema019AndPreservesData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")
	db := open(t, path, 5000)
	ctx := context.Background()

	migrations, err := LoadMigrations()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, err := db.ExecContext(ctx, createSchemaMigrations); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}
	for _, m := range migrations {
		if m.Version >= 20 {
			break
		}
		if err := applyOne(ctx, db, m); err != nil {
			t.Fatalf("apply migration %d: %v", m.Version, err)
		}
	}

	p := seedProject(t, db, "p020")
	seedSession(t, db, "s020", p)

	// Apply 020.
	var m020 *Migration
	for i := range migrations {
		if migrations[i].Version == 20 {
			m020 = &migrations[i]
			break
		}
	}
	if m020 == nil {
		t.Fatal("migration 020 not embedded")
	}
	if err := applyOne(ctx, db, *m020); err != nil {
		t.Fatalf("apply migration 20: %v", err)
	}

	// A row inserted after the upgrade exercises the new tables end to end.
	seedDomain(t, db, hex32(1))

	// Existing data retained.
	var projects, sessions int
	if err := db.QueryRow(`SELECT count(*) FROM projects`).Scan(&projects); err != nil {
		t.Fatalf("count projects: %v", err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM sessions`).Scan(&sessions); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if projects != 1 || sessions != 1 {
		t.Errorf("after 020: projects=%d sessions=%d, want 1 and 1", projects, sessions)
	}
	var domains int
	if err := db.QueryRow(
		`SELECT count(*) FROM project_security_domains`).Scan(&domains); err != nil {
		t.Fatalf("count domains: %v", err)
	}
	if domains != 1 {
		t.Errorf("domains=%d, want 1 (the post-upgrade seed)", domains)
	}

	// No memberships: every project remains unconfined.
	var memberships int
	if err := db.QueryRow(
		`SELECT count(*) FROM project_domain_memberships`).Scan(&memberships); err != nil {
		t.Fatalf("count memberships: %v", err)
	}
	if memberships != 0 {
		t.Errorf("memberships=%d, want 0 (migration must not confine projects)", memberships)
	}
}

// TestMigrate020RecursiveTriggersReadsBackOne confirms the test DSN enables
// recursive triggers, which the immutability triggers rely on firing fully.
func TestMigrate020RecursiveTriggersReadsBackOne(t *testing.T) {
	db, _ := openMigrated(t)
	var got sql.NullString
	if err := db.QueryRow(`PRAGMA recursive_triggers`).Scan(&got); err != nil {
		t.Fatalf("read recursive_triggers: %v", err)
	}
	if !got.Valid || got.String != "1" {
		t.Fatalf("recursive_triggers = %v, want 1", got)
	}
}

// TestMigrate020IdentifierAndDigestChecks proves the exact lowercase-hex checks
// reject uppercase, wrong length, and non-hex values, and accept valid ones.
func TestMigrate020IdentifierAndDigestChecks(t *testing.T) {
	db, _ := openMigrated(t)
	now := FormatTime(fixedNow)
	domain := seedDomain(t, db, hex32(1))

	cases := []struct {
		name string
		id   string
	}{
		{"uppercase hex", "ABCDEF000000000000000000000000AB"},
		{"too short", hex32(1)[:31]},
		{"too long", hex32(1) + "0"},
		{"non-hex", strings.Repeat("z", 32)},
	}
	for _, c := range cases {
		if _, err := db.Exec(
			`INSERT INTO project_security_domains
			     (security_domain_id, display_label, created_at, created_by)
			 VALUES (?, ?, ?, ?)`, c.id, c.name, now, "op"); err == nil {
			t.Errorf("identifier %q was accepted; want CHECK rejection", c.id)
		}
	}

	badDigests := []struct {
		name   string
		digest string
	}{
		{"uppercase digest", strings.Repeat("A", 64)},
		{"short digest", strings.Repeat("a", 63)},
		{"long digest", strings.Repeat("a", 65)},
		{"non-hex digest", strings.Repeat("g", 64)},
	}
	for _, c := range badDigests {
		if _, err := db.Exec(
			`INSERT INTO project_policy_revisions
			     (revision_id, security_domain_id, revision_no, policy_digest,
			      canonical_json, created_at, created_by)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			hex32(90), domain, 1, c.digest, `{"x":1}`, now, "op"); err == nil {
			t.Errorf("digest %q was accepted; want CHECK rejection", c.name)
		}
	}
}

// TestMigrate020CanonicalJsonChecks proves only valid JSON objects are stored;
// arrays, scalars, and non-JSON are rejected.
func TestMigrate020CanonicalJsonChecks(t *testing.T) {
	db, _ := openMigrated(t)
	now := FormatTime(fixedNow)
	domain := seedDomain(t, db, hex32(1))

	bad := []string{`[1,2,3]`, `"scalar"`, `42`, `null`, `true`, `{not json`}
	for i, body := range bad {
		if _, err := db.Exec(
			`INSERT INTO project_policy_revisions
			     (revision_id, security_domain_id, revision_no, policy_digest,
			      canonical_json, created_at, created_by)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			hex32(100+i), domain, i+1, hex64(i), body, now, "op"); err == nil {
			t.Errorf("canonical_json %q was accepted; want rejection", body)
		}
	}
	// A plain object is accepted.
	if _, err := db.Exec(
		`INSERT INTO project_policy_revisions
		     (revision_id, security_domain_id, revision_no, policy_digest,
		      canonical_json, created_at, created_by)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		hex32(200), domain, 1, hex64(1), `{"profile":"v1"}`, now, "op"); err != nil {
		t.Errorf("valid object canonical_json rejected: %v", err)
	}
}

// TestMigrate020ForeignKeysRestr proves foreign keys are present and use ON
// DELETE RESTRICT: bad references are rejected on insert and deleting a
// referenced project is refused.
func TestMigrate020ForeignKeysRestrict(t *testing.T) {
	db, _ := openMigrated(t)
	now := FormatTime(fixedNow)

	// membership → nonexistent project and domain.
	if _, err := db.Exec(
		`INSERT INTO project_domain_memberships
		     (membership_id, project_id, security_domain_id, created_at, created_by)
		 VALUES (?, ?, ?, ?, ?)`,
		hex32(1), "no-such-project", hex32(1), now, "op"); err == nil {
		t.Error("membership with nonexistent project was accepted")
	}
	p := seedProject(t, db, "p")
	domain := seedDomain(t, db, hex32(1))
	if _, err := db.Exec(
		`INSERT INTO project_domain_memberships
		     (membership_id, project_id, security_domain_id, created_at, created_by)
		 VALUES (?, ?, ?, ?, ?)`,
		hex32(2), p, "deadbeefdeadbeefdeadbeefdeadbeef", now, "op"); err == nil {
		t.Error("membership with nonexistent domain was accepted")
	}

	// revision → nonexistent domain.
	if _, err := db.Exec(
		`INSERT INTO project_policy_revisions
		     (revision_id, security_domain_id, revision_no, policy_digest,
		      canonical_json, created_at, created_by)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		hex32(9), "deadbeefdeadbeefdeadbeefdeadbeef", 1, hex64(1), `{}`, now, "op"); err == nil {
		t.Error("revision with nonexistent domain was accepted")
	}

	// Deleting a project that an active membership references is refused.
	if _, err := db.Exec(
		`INSERT INTO project_domain_memberships
		     (membership_id, project_id, security_domain_id, created_at, created_by)
		 VALUES (?, ?, ?, ?, ?)`,
		hex32(3), p, domain, now, "op"); err != nil {
		t.Fatalf("seed membership: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM projects WHERE id = ?`, p); err == nil {
		t.Error("deleting a project with an active membership was accepted")
	}
}

// TestMigrate020UniqueDomainRevision proves (security_domain_id, revision_no)
// is unique, even across invalidated revisions.
func TestMigrate020UniqueDomainRevision(t *testing.T) {
	db, _ := openMigrated(t)
	domain := seedDomain(t, db, hex32(1))
	seedRevision(t, db, hex32(2), domain, 1)
	// Same domain + revision_no with a fresh id must collide.
	if _, err := db.Exec(
		`INSERT INTO project_policy_revisions
		     (revision_id, security_domain_id, revision_no, policy_digest,
		      canonical_json, created_at, created_by)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		hex32(3), domain, 1, hex64(2), `{}`, FormatTime(fixedNow), "op"); err == nil {
		t.Error("duplicate (domain, revision_no) was accepted")
	}
	// A different revision_no in the same domain is fine.
	seedRevision(t, db, hex32(4), domain, 2)
}

// TestMigrate020ActivePartialUniqueIndexes proves the active-only uniqueness:
// a second active attestation per revision, approval per attestation, and
// membership per project are rejected, but a second inactive one is allowed.
func TestMigrate020ActivePartialUniqueIndexes(t *testing.T) {
	db, _ := openMigrated(t)
	domain := seedDomain(t, db, hex32(1))
	revision := seedRevision(t, db, hex32(2), domain, 1)
	att := seedAttestation(t, db, hex32(3), revision, domain)
	seedApproval(t, db, hex32(4), att, revision, domain)

	// Second active attestation for the same revision.
	if _, err := db.Exec(
		`INSERT INTO project_backing_attestations
		     (attestation_id, revision_id, security_domain_id, backing_digest,
		      canonical_json, attested_at, attested_by)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		hex32(5), revision, domain, hex64(8), `{}`, FormatTime(fixedNow), "sup"); err == nil {
		t.Error("second active attestation per revision was accepted")
	}
	// Second active approval for the same attestation.
	if _, err := db.Exec(
		`INSERT INTO project_policy_approvals
		     (approval_id, attestation_id, revision_id, security_domain_id,
		      approved_at, approved_by)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		hex32(6), att, revision, domain, FormatTime(fixedNow), "op"); err == nil {
		t.Error("second active approval per attestation was accepted")
	}

	// Invalidation order requires the approval before its attestation. After the
	// active approval is gone the per-attestation slot frees, so a second active
	// approval is allowed; invalidating that one in turn lets the attestation be
	// invalidated, freeing the per-revision slot for a second active attestation.
	invalidate(t, db, "project_policy_approvals", "approval_id", hex32(4), "revoked", 11)
	seedApproval(t, db, hex32(7), att, revision, domain)
	invalidate(t, db, "project_policy_approvals", "approval_id", hex32(7), "revoked", 14)
	invalidate(t, db, "project_backing_attestations", "attestation_id", hex32(3), "revoked", 12)
	seedAttestation(t, db, hex32(8), revision, domain)

	// Two active memberships for one project are rejected; the inactive first
	// frees the project.
	p := seedProject(t, db, "puniq")
	if _, err := db.Exec(
		`INSERT INTO project_domain_memberships
		     (membership_id, project_id, security_domain_id, created_at, created_by)
		 VALUES (?, ?, ?, ?, ?)`,
		hex32(9), p, domain, FormatTime(fixedNow), "op"); err != nil {
		t.Fatalf("first membership: %v", err)
	}
	domain2 := seedDomain(t, db, hex32(20))
	if _, err := db.Exec(
		`INSERT INTO project_domain_memberships
		     (membership_id, project_id, security_domain_id, created_at, created_by)
		 VALUES (?, ?, ?, ?, ?)`,
		hex32(10), p, domain2, FormatTime(fixedNow), "op"); err == nil {
		t.Error("second active membership per project was accepted")
	}
	invalidate(t, db, "project_domain_memberships", "membership_id", hex32(9), "revoked", 13)
	if _, err := db.Exec(
		`INSERT INTO project_domain_memberships
		     (membership_id, project_id, security_domain_id, created_at, created_by)
		 VALUES (?, ?, ?, ?, ?)`,
		hex32(11), p, domain2, FormatTime(fixedNow), "op"); err != nil {
		t.Errorf("membership after invalidating the prior one rejected: %v", err)
	}
}

// TestMigrate020Immutability proves the only allowed UPDATE is the one-way
// invalidation transition: column mutation, a partial tuple, and a second
// invalidation are all rejected.
func TestMigrate020Immutability(t *testing.T) {
	db, _ := openMigrated(t)
	now := FormatTime(fixedNow)
	domain, _, _, _ := seedChain(t, db)

	// Mutating a non-invalidation column is rejected.
	if _, err := db.Exec(
		`UPDATE project_security_domains SET display_label = ? WHERE security_domain_id = ?`,
		"changed", domain); err == nil {
		t.Error("mutating a non-invalidation column was accepted")
	}

	// A partial invalidation tuple (reason set, evidence/at/by null) is rejected.
	if _, err := db.Exec(
		`UPDATE project_security_domains SET invalidated_reason = ?
		  WHERE security_domain_id = ?`, "revoked", domain); err == nil {
		t.Error("partial invalidation tuple was accepted")
	}

	// The full tuple is accepted.
	invalidate(t, db, "project_security_domains", "security_domain_id", domain, "restore", 21)

	// A second invalidation (or any further edit) is rejected.
	if _, err := db.Exec(
		`UPDATE project_security_domains SET invalidated_reason = ?, invalidated_evidence = ?,
		        invalidated_at = ?, invalidated_by = ?
		  WHERE security_domain_id = ?`,
		"backing_mismatch", hex64(22), now, "op", domain); err == nil {
		t.Error("second invalidation was accepted")
	}
}

// TestMigrate020NeverDeletable proves DELETE is refused on every protected
// table. A BEFORE DELETE trigger fires once per row, so each table is seeded
// first: an empty DELETE harms nothing, and the protection must hold against a
// delete that would actually erase a row.
func TestMigrate020NeverDeletable(t *testing.T) {
	db, _ := openMigrated(t)
	domain := seedDomain(t, db, hex32(1))
	revision := seedRevision(t, db, hex32(2), domain, 1)
	attestation := seedAttestation(t, db, hex32(3), revision, domain)
	seedApproval(t, db, hex32(4), attestation, revision, domain)
	project := seedProject(t, db, "pnd")
	if _, err := db.Exec(
		`INSERT INTO project_domain_memberships
		     (membership_id, project_id, security_domain_id, created_at, created_by)
		 VALUES (?, ?, ?, ?, ?)`,
		hex32(5), project, domain, FormatTime(fixedNow), "op"); err != nil {
		t.Fatalf("seed membership: %v", err)
	}

	// Deleting a whole table is refused row by row: the first existing row's
	// BEFORE DELETE trigger aborts the statement.
	for _, table := range []string{
		"project_security_domains", "project_domain_memberships",
		"project_policy_revisions", "project_backing_attestations",
		"project_policy_approvals",
	} {
		if _, err := db.Exec(fmt.Sprintf(`DELETE FROM %s`, table)); err == nil {
			t.Errorf("DELETE FROM %s was accepted", table)
		}
	}

	// A targeted delete of one row is refused for the same reason.
	if _, err := db.Exec(
		`DELETE FROM project_policy_revisions WHERE revision_id = ?`, revision); err == nil {
		t.Error("DELETE of a single revision was accepted")
	}
}

// TestMigrate020InsertOrReplaceResisted proves INSERT OR REPLACE cannot mutate
// an existing protected row: the conflict-resolution delete fires the no-delete
// trigger. A genuine new primary key still inserts normally.
func TestMigrate020InsertOrReplaceResisted(t *testing.T) {
	db, _ := openMigrated(t)
	now := FormatTime(fixedNow)
	domain := seedDomain(t, db, hex32(1))

	// Replacing the same primary key with altered content is refused.
	if _, err := db.Exec(
		`INSERT OR REPLACE INTO project_security_domains
		     (security_domain_id, display_label, created_at, created_by)
		 VALUES (?, ?, ?, ?)`,
		domain, "REPLACED", now, "other"); err == nil {
		t.Error("INSERT OR REPLACE on an existing domain was accepted")
	}
	// The original row is intact.
	var label string
	if err := db.QueryRow(
		`SELECT display_label FROM project_security_domains WHERE security_domain_id = ?`,
		domain).Scan(&label); err != nil {
		t.Fatalf("reread domain: %v", err)
	}
	if label == "REPLACED" {
		t.Error("INSERT OR REPLACE mutated the immutable row despite the error")
	}

	// INSERT OR REPLACE on a fresh primary key is an ordinary insert and works.
	if _, err := db.Exec(
		`INSERT OR REPLACE INTO project_security_domains
		     (security_domain_id, display_label, created_at, created_by)
		 VALUES (?, ?, ?, ?)`,
		hex32(2), "fresh", now, "op"); err != nil {
		t.Errorf("INSERT OR REPLACE on a fresh primary key rejected: %v", err)
	}
}

// TestMigrate020AttestationBindsRevisionDomain proves a backing attestation's
// revision_id and security_domain_id must name one row in
// project_policy_revisions: an attestation cannot attach to a revision that
// belongs to a different domain, and a matching revision binds cleanly so the
// approval chain seeded elsewhere stays valid.
func TestMigrate020AttestationBindsRevisionDomain(t *testing.T) {
	db, _ := openMigrated(t)
	now := FormatTime(fixedNow)

	domainA := seedDomain(t, db, hex32(1))
	revisionA := seedRevision(t, db, hex32(2), domainA, 1)

	// Positive: an attestation naming revisionA under its own domain binds.
	seedAttestation(t, db, hex32(3), revisionA, domainA)

	// Mismatched domain: the revision exists but belongs to another domain, so
	// an attestation that pins it to this domain is rejected by the trigger.
	domainB := seedDomain(t, db, hex32(4))
	revisionB := seedRevision(t, db, hex32(5), domainB, 1)
	if _, err := db.Exec(
		`INSERT INTO project_backing_attestations
		     (attestation_id, revision_id, security_domain_id, backing_digest,
		      canonical_json, attested_at, attested_by)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		hex32(6), revisionB, domainA, hex64(700), `{"backing":"v1"}`, now, "supervisor"); err == nil {
		t.Error("attestation binding a revision to a foreign domain was accepted")
	}

	// Symmetric mismatch: revisionA belongs to domainA, not domainB.
	if _, err := db.Exec(
		`INSERT INTO project_backing_attestations
		     (attestation_id, revision_id, security_domain_id, backing_digest,
		      canonical_json, attested_at, attested_by)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		hex32(7), revisionA, domainB, hex64(700), `{"backing":"v1"}`, now, "supervisor"); err == nil {
		t.Error("attestation binding a foreign-domain revision to this domain was accepted")
	}

	// Positive again: revisionB under its own domain binds, and the full chain
	// (attestation then approval) over it stays valid.
	att := seedAttestation(t, db, hex32(8), revisionB, domainB)
	seedApproval(t, db, hex32(9), att, revisionB, domainB)
}

// TestMigrate020ApprovalBindsActiveAttestation proves an active approval must
// bind an active attestation for the same policy: an inactive attestation, a
// cross-policy revision, or a foreign domain are rejected.
func TestMigrate020ApprovalBindsActiveAttestation(t *testing.T) {
	db, _ := openMigrated(t)
	now := FormatTime(fixedNow)
	domain := seedDomain(t, db, hex32(1))
	revision := seedRevision(t, db, hex32(2), domain, 1)
	att := seedAttestation(t, db, hex32(3), revision, domain)

	// Cross-policy: approval claims a different revision than the attestation.
	otherRevision := seedRevision(t, db, hex32(9), domain, 2)
	if _, err := db.Exec(
		`INSERT INTO project_policy_approvals
		     (approval_id, attestation_id, revision_id, security_domain_id,
		      approved_at, approved_by)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		hex32(4), att, otherRevision, domain, now, "op"); err == nil {
		t.Error("approval binding an attestation to a different revision was accepted")
	}

	// Foreign domain: approval claims a domain that differs from the attestation.
	foreignDomain := seedDomain(t, db, hex32(20))
	if _, err := db.Exec(
		`INSERT INTO project_policy_approvals
		     (approval_id, attestation_id, revision_id, security_domain_id,
		      approved_at, approved_by)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		hex32(5), att, revision, foreignDomain, now, "op"); err == nil {
		t.Error("approval binding an attestation to a foreign domain was accepted")
	}

	// Inactive attestation: invalidate the attestation (no approval bound), then
	// an approval for it must be rejected.
	invalidate(t, db, "project_backing_attestations", "attestation_id", att, "backing_mismatch", 30)
	if _, err := db.Exec(
		`INSERT INTO project_policy_approvals
		     (approval_id, attestation_id, revision_id, security_domain_id,
		      approved_at, approved_by)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		hex32(6), att, revision, domain, now, "op"); err == nil {
		t.Error("approval binding an inactive attestation was accepted")
	}

	// A fresh active attestation binds cleanly.
	att2 := seedAttestation(t, db, hex32(7), revision, domain)
	seedApproval(t, db, hex32(8), att2, revision, domain)
}

// TestMigrate020InvalidationOrderApprovalBeforeAttestation proves an
// attestation cannot be invalidated while an active approval is bound to it;
// once the approval is invalidated first, the attestation can be.
func TestMigrate020InvalidationOrderApprovalBeforeAttestation(t *testing.T) {
	db, _ := openMigrated(t)
	_, _, att, app := seedChain(t, db)

	// Attestation invalidation blocked while the approval is active.
	if _, err := db.Exec(
		`UPDATE project_backing_attestations SET invalidated_reason = ?, invalidated_evidence = ?,
		        invalidated_at = ?, invalidated_by = ?
		  WHERE attestation_id = ?`,
		"backing_mismatch", hex64(40), FormatTime(fixedNow), "op", att); err == nil {
		t.Error("attestation invalidated before its active approval")
	}

	// Invalidate the approval first, then the attestation succeeds.
	invalidate(t, db, "project_policy_approvals", "approval_id", app, "revoked", 41)
	invalidate(t, db, "project_backing_attestations", "attestation_id", att, "backing_mismatch", 42)

	// And the invalidated approval cannot be re-edited (one-way).
	if _, err := db.Exec(
		`UPDATE project_policy_approvals SET invalidated_reason = ?, invalidated_evidence = ?,
		        invalidated_at = ?, invalidated_by = ?
		  WHERE approval_id = ?`,
		"restore", hex64(43), FormatTime(fixedNow), "op", app); err == nil {
		t.Error("an invalidated approval was re-edited")
	}
}

// TestMigrate020EvidenceReuse proves restore/backing_mismatch evidence is
// single-use within a table while revoked evidence may repeat.
func TestMigrate020EvidenceReuse(t *testing.T) {
	db, _ := openMigrated(t)
	domain := seedDomain(t, db, hex32(1))

	// Two restore invalidations sharing evidence in the same table collide.
	r1 := seedRevision(t, db, hex32(2), domain, 1)
	r2 := seedRevision(t, db, hex32(3), domain, 2)
	const sharedEvidence = 50
	invalidate(t, db, "project_policy_revisions", "revision_id", r1, "restore", sharedEvidence)
	if _, err := db.Exec(
		`UPDATE project_policy_revisions SET invalidated_reason = ?, invalidated_evidence = ?,
		        invalidated_at = ?, invalidated_by = ?
		  WHERE revision_id = ?`,
		"restore", hex64(sharedEvidence), FormatTime(fixedNow), "op", r2); err == nil {
		t.Error("restore evidence was reused within the same table")
	}

	// backing_mismatch is likewise single-use; the blocked second row can still
	// be invalidated with a fresh evidence value.
	r3 := seedRevision(t, db, hex32(4), domain, 3)
	if _, err := db.Exec(
		`UPDATE project_policy_revisions SET invalidated_reason = ?, invalidated_evidence = ?,
		        invalidated_at = ?, invalidated_by = ?
		  WHERE revision_id = ?`,
		"backing_mismatch", hex64(sharedEvidence), FormatTime(fixedNow), "op", r3); err == nil {
		t.Error("backing_mismatch evidence was reused within the same table")
	}
	invalidate(t, db, "project_policy_revisions", "revision_id", r3, "backing_mismatch", 51)

	// revoked evidence may repeat freely.
	d1 := seedDomain(t, db, hex32(10))
	d2 := seedDomain(t, db, hex32(11))
	const revokedEvidence = 60
	invalidate(t, db, "project_security_domains", "security_domain_id", d1, "revoked", revokedEvidence)
	invalidate(t, db, "project_security_domains", "security_domain_id", d2, "revoked", revokedEvidence)
}

// TestMigrate020ReasonSetIsClosed proves an invalidation reason outside the
// closed set is rejected on a fresh active row.
func TestMigrate020ReasonSetIsClosed(t *testing.T) {
	db, _ := openMigrated(t)
	now := FormatTime(fixedNow)
	domain := seedDomain(t, db, hex32(1))
	if _, err := db.Exec(
		`UPDATE project_security_domains SET invalidated_reason = ?, invalidated_evidence = ?,
		        invalidated_at = ?, invalidated_by = ?
		  WHERE security_domain_id = ?`,
		"expired", hex64(70), now, "op", domain); err == nil {
		t.Error("an invalidation reason outside the closed set was accepted")
	}
}
