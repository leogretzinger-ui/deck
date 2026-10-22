package project

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/noisefloor-oss/deck/internal/db"
	"github.com/noisefloor-oss/deck/internal/projectpolicy"
)

// resolvedPolicy builds a resolved noisedeck.project-policy.v1 value with no
// domain binding: the repository generates and binds that identity. Each
// test varies exactly the dimension it exercises through the modifiers.
func resolvedPolicy(modifiers ...func(*projectpolicy.ProjectPolicyV1)) projectpolicy.ProjectPolicyV1 {
	policy := projectpolicy.ProjectPolicyV1{
		Schema:                   projectpolicy.ProjectPolicySchemaV1,
		BackingDigest:            strings.Repeat("1", 64),
		ConfinementProfileDigest: strings.Repeat("2", 64),
		ProviderProfileDigest:    strings.Repeat("3", 64),
		MemoryScopeDigest:        strings.Repeat("4", 64),
		MappedRemote: projectpolicy.MappedRemoteV1{
			Repository:      "github.com/first/repo",
			PermissionClass: projectpolicy.RemotePermissionContribute,
		},
		Resources: projectpolicy.ResourceLimitsV1{
			Processes:     512,
			MemoryBytes:   8 << 30,
			OutputBytes:   1 << 30,
			ProjectBytes:  20 << 30,
			ScratchBytes:  4 << 30,
			ProjectInodes: 1 << 20,
			ScratchInodes: 1 << 18,
			ExchangeBytes: 1 << 30,
			CPU:           projectpolicy.CPULimitV1{QuotaPerSecUsec: 800000, PeriodUsec: 100000},
			IO: projectpolicy.IOLimitV1{
				ReadBytesPerSec:  100 << 20,
				WriteBytesPerSec: 50 << 20,
				ReadOpsPerSec:    10000,
				WriteOpsPerSec:   5000,
			},
		},
	}
	for _, modify := range modifiers {
		modify(&policy)
	}
	return policy
}

func withBacking(digest string) func(*projectpolicy.ProjectPolicyV1) {
	return func(p *projectpolicy.ProjectPolicyV1) { p.BackingDigest = digest }
}

func withMemoryScope(digest string) func(*projectpolicy.ProjectPolicyV1) {
	return func(p *projectpolicy.ProjectPolicyV1) { p.MemoryScopeDigest = digest }
}

func withRemote(repository string, class projectpolicy.RemotePermissionClass) func(*projectpolicy.ProjectPolicyV1) {
	return func(p *projectpolicy.ProjectPolicyV1) {
		p.MappedRemote = projectpolicy.MappedRemoteV1{Repository: repository, PermissionClass: class}
	}
}

func withProcesses(n uint64) func(*projectpolicy.ProjectPolicyV1) {
	return func(p *projectpolicy.ProjectPolicyV1) { p.Resources.Processes = n }
}

// Digest alphabets distinct per dimension so one varying field never matches
// another dimension by accident.
var (
	backingA = strings.Repeat("7", 64)
	backingB = strings.Repeat("9", 64)
	memoryA  = strings.Repeat("8", 64)
	memoryB  = strings.Repeat("a", 64)
	repoA    = "github.com/first/repo"
	repoB    = "github.com/second/repo"
	hexID    = func(n int) string { return fmt.Sprintf("%032x", n) }
)

// registerPolicyProject registers one real project under root, the only path
// a membership may reference.
func registerPolicyProject(t *testing.T, repo *Repository, root, name, dir string) string {
	t.Helper()
	p, _, err := repo.Register(context.Background(), name, dir, []string{root},
		GitIdentity{Name: "Test Author", Email: "test@example.invalid"})
	if err != nil {
		t.Fatalf("register %s: %v", name, err)
	}
	return p.ID
}

func createPolicyDomain(t *testing.T, repo *Repository, projectID string, policy projectpolicy.ProjectPolicyV1) *StoredPolicyRevision {
	t.Helper()
	stored, err := repo.CreatePolicyDomain(context.Background(), projectID, policy, "operator")
	if err != nil {
		t.Fatalf("CreatePolicyDomain: %v", err)
	}
	return stored
}

// policyStateSnapshot counts every protected row and policy audit event, so a
// failure path can prove it left nothing behind.
func policyStateSnapshot(t *testing.T, d *db.DB) map[string]int {
	t.Helper()
	state := make(map[string]int)
	for _, table := range []string{
		"project_security_domains", "project_domain_memberships", "project_policy_revisions",
	} {
		state[table] = countRows(t, d, table)
	}
	for _, typ := range []string{AuditSecurityDomainCreated, AuditPolicyRevisionAppended} {
		var n int
		if err := d.Reader().QueryRowContext(context.Background(),
			`SELECT count(*) FROM audit_events WHERE event_type = ?`, typ).Scan(&n); err != nil {
			t.Fatalf("count audit %s: %v", typ, err)
		}
		state["audit:"+typ] = n
	}
	return state
}

func countRows(t *testing.T, d *db.DB, table string) int {
	t.Helper()
	var n int
	if err := d.Reader().QueryRowContext(context.Background(),
		fmt.Sprintf(`SELECT count(*) FROM %s`, table)).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func requireStateUnchanged(t *testing.T, d *db.DB, before map[string]int) {
	t.Helper()
	after := policyStateSnapshot(t, d)
	for table, n := range before {
		if after[table] != n {
			t.Errorf("%s: %d rows before failure, %d after; want no partial rows or audit events",
				table, n, after[table])
		}
	}
}

// rawExec runs one write outside the repository, the shape a hostile or
// administrative writer with database access would use.
func rawExec(t *testing.T, d *db.DB, query string, args ...any) {
	t.Helper()
	if err := d.WithTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, query, args...)
		return err
	}); err != nil {
		t.Fatalf("raw exec: %v", err)
	}
}

// invalidateRow applies the one-way invalidation transition the schema allows.
func invalidateRow(t *testing.T, d *db.DB, table, idColumn, id, reason string, evidence int) {
	t.Helper()
	rawExec(t, d, fmt.Sprintf(
		`UPDATE %s SET invalidated_reason = ?, invalidated_evidence = ?,
		        invalidated_at = ?, invalidated_by = ?
		  WHERE %s = ?`, table, idColumn),
		reason, fmt.Sprintf("%064x", evidence), "2026-08-14T12:00:00.000Z", "test-operator", id)
}

func TestCreatePolicyDomainPersistsAndAudits(t *testing.T) {
	repo, d := newTestRepo(t)
	ctx := context.Background()
	root := mkdir(t, filepath.Join(t.TempDir(), "root"))
	dir := mkdir(t, filepath.Join(root, "proj"))
	projectID := registerPolicyProject(t, repo, root, "demo", dir)

	stored := createPolicyDomain(t, repo, projectID, resolvedPolicy())

	// Server-generated identities, never caller input.
	for name, id := range map[string]string{
		"domain id": stored.DomainID, "revision id": stored.RevisionID,
	} {
		if len(id) != 32 || strings.ToLower(id) != id || strings.Trim(id, "0123456789abcdef") != "" {
			t.Errorf("%s = %q, want server-generated lowercase 32-hex", name, id)
		}
	}
	if stored.Revision != 1 {
		t.Errorf("revision = %d, want 1", stored.Revision)
	}
	policy := resolvedPolicy()
	policy.SecurityDomainID = stored.DomainID
	want, err := projectpolicy.CanonicalizeProjectPolicy(policy)
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	if stored.Digest != want.Digest || stored.CanonicalJSON != want.CanonicalJSON {
		t.Error("stored policy identity differs from the canonical form")
	}
	if stored.Policy.SecurityDomainID != stored.DomainID {
		t.Errorf("policy names domain %s, want %s", stored.Policy.SecurityDomainID, stored.DomainID)
	}
	if stored.Invalidated != nil {
		t.Error("fresh revision reported an invalidation tuple")
	}

	var label, createdBy string
	var invalid sql.NullString
	if err := d.Reader().QueryRowContext(ctx,
		`SELECT display_label, created_by, invalidated_reason
		   FROM project_security_domains WHERE security_domain_id = ?`,
		stored.DomainID).Scan(&label, &createdBy, &invalid); err != nil {
		t.Fatalf("read domain: %v", err)
	}
	if label != "demo" || createdBy != "operator" || invalid.Valid {
		t.Errorf("domain row = (%q, %q, invalidated=%v), want (demo, operator, active)",
			label, createdBy, invalid.Valid)
	}
	var membershipDomain string
	if err := d.Reader().QueryRowContext(ctx,
		`SELECT security_domain_id FROM project_domain_memberships
		  WHERE project_id = ? AND invalidated_reason IS NULL`, projectID,
	).Scan(&membershipDomain); err != nil {
		t.Fatalf("read membership: %v", err)
	}
	if membershipDomain != stored.DomainID {
		t.Errorf("membership domain = %s, want %s", membershipDomain, stored.DomainID)
	}
	if n := countRows(t, d, "project_policy_revisions"); n != 1 {
		t.Fatalf("revision rows = %d, want 1", n)
	}
	var detail string
	if err := d.Reader().QueryRowContext(ctx,
		`SELECT details FROM audit_events WHERE event_type = ?`, AuditSecurityDomainCreated,
	).Scan(&detail); err != nil {
		t.Fatalf("read audit event: %v", err)
	}
	for _, want := range []string{
		"project_id=" + projectID, "domain_id=" + stored.DomainID,
		"revision_id=" + stored.RevisionID, "digest=" + stored.Digest,
	} {
		if !strings.Contains(detail, want) {
			t.Errorf("audit detail %q missing %q", detail, want)
		}
	}
}

func TestCreatePolicyDomainRejectsCallerChosenAuthority(t *testing.T) {
	repo, d := newTestRepo(t)
	root := mkdir(t, filepath.Join(t.TempDir(), "root"))
	dir := mkdir(t, filepath.Join(root, "proj"))
	projectID := registerPolicyProject(t, repo, root, "proj", dir)
	before := policyStateSnapshot(t, d)

	chosen := resolvedPolicy()
	chosen.SecurityDomainID = "00112233445566778899aabbccddeeff"
	if _, err := repo.CreatePolicyDomain(context.Background(), projectID, chosen, "operator"); !errors.Is(err, ErrPolicyAuthorityInput) {
		t.Errorf("caller-chosen domain: err = %v, want ErrPolicyAuthorityInput", err)
	}
	for _, operator := range []string{"", "   "} {
		if _, err := repo.CreatePolicyDomain(context.Background(), projectID, resolvedPolicy(), operator); !errors.Is(err, ErrPolicyAuthorityInput) {
			t.Errorf("creator %q: err = %v, want ErrPolicyAuthorityInput", operator, err)
		}
	}
	if _, err := repo.CreatePolicyDomain(context.Background(), projectID, projectpolicy.ProjectPolicyV1{}, "operator"); err == nil {
		t.Error("empty policy accepted")
	}
	requireStateUnchanged(t, d, before)
}

func TestCreatePolicyDomainRequiresResolvableActiveProject(t *testing.T) {
	repo, d := newTestRepo(t)
	ctx := context.Background()
	root := mkdir(t, filepath.Join(t.TempDir(), "root"))
	projectID := registerPolicyProject(t, repo, root, "proj", mkdir(t, filepath.Join(root, "proj")))
	before := policyStateSnapshot(t, d)

	if _, err := repo.CreatePolicyDomain(ctx, "nosuchproject", resolvedPolicy(), "operator"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown project: err = %v, want ErrNotFound", err)
	}
	if err := repo.Remove(ctx, projectID); err != nil {
		t.Fatalf("remove project: %v", err)
	}
	if _, err := repo.CreatePolicyDomain(ctx, projectID, resolvedPolicy(), "operator"); !errors.Is(err, ErrNotFound) {
		t.Errorf("removed project: err = %v, want ErrNotFound", err)
	}

	// A vanished directory refuses the write rather than attesting whatever
	// the path spelling now denotes.
	goneDir := mkdir(t, filepath.Join(root, "gone"))
	gone := registerPolicyProject(t, repo, root, "gone", goneDir)
	if err := os.RemoveAll(goneDir); err != nil {
		t.Fatalf("remove dir: %v", err)
	}
	if _, err := repo.CreatePolicyDomain(ctx, gone, resolvedPolicy(), "operator"); !errors.Is(err, ErrPolicyBackingChanged) {
		t.Errorf("vanished backing: err = %v, want ErrPolicyBackingChanged", err)
	}
	requireStateUnchanged(t, d, before)
}

func TestCreatePolicyDomainRejectsSecondActiveDomain(t *testing.T) {
	repo, d := newTestRepo(t)
	ctx := context.Background()
	root := mkdir(t, filepath.Join(t.TempDir(), "root"))
	projectID := registerPolicyProject(t, repo, root, "proj", mkdir(t, filepath.Join(root, "proj")))
	createPolicyDomain(t, repo, projectID, resolvedPolicy())
	before := policyStateSnapshot(t, d)

	if _, err := repo.CreatePolicyDomain(ctx, projectID, resolvedPolicy(withProcesses(600)), "operator"); !errors.Is(err, ErrProjectAlreadyConfined) {
		t.Errorf("second domain: err = %v, want ErrProjectAlreadyConfined", err)
	}
	requireStateUnchanged(t, d, before)
}

func TestAppendPolicyRevisionCAS(t *testing.T) {
	repo, d := newTestRepo(t)
	ctx := context.Background()
	root := mkdir(t, filepath.Join(t.TempDir(), "root"))
	projectID := registerPolicyProject(t, repo, root, "proj", mkdir(t, filepath.Join(root, "proj")))
	first := createPolicyDomain(t, repo, projectID, resolvedPolicy())
	before := policyStateSnapshot(t, d)

	appendTx := func(policy projectpolicy.ProjectPolicyV1, revID, digest string) (*StoredPolicyRevision, error) {
		return repo.AppendPolicyRevision(ctx, projectID, policy, revID, digest, "operator")
	}

	second, err := appendTx(resolvedPolicy(withProcesses(600)), first.RevisionID, first.Digest)
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if second.Revision != 2 || second.DomainID != first.DomainID || second.RevisionID == first.RevisionID {
		t.Fatalf("append result = revision %d in domain %s, want revision 2 in domain %s with a fresh id",
			second.Revision, second.DomainID, first.DomainID)
	}
	// The old revision is untouched: same identity, bytes, and no invalidation.
	old, err := repo.GetPolicyRevision(ctx, projectID, first.RevisionID)
	if err != nil {
		t.Fatalf("reread first: %v", err)
	}
	if old.Digest != first.Digest || old.CanonicalJSON != first.CanonicalJSON || old.Invalidated != nil {
		t.Error("append mutated the old revision")
	}

	// An exact replay of the successful append conflicts: history moved.
	if _, err := appendTx(resolvedPolicy(withProcesses(600)), first.RevisionID, first.Digest); !errors.Is(err, ErrPolicyRevisionConflict) {
		t.Errorf("replay: err = %v, want ErrPolicyRevisionConflict", err)
	}
	// A digest that never named the current revision is stale.
	if _, err := appendTx(resolvedPolicy(withProcesses(700)), second.RevisionID, first.Digest); !errors.Is(err, ErrPolicyRevisionConflict) {
		t.Errorf("wrong digest: err = %v, want ErrPolicyRevisionConflict", err)
	}
	if _, err := appendTx(resolvedPolicy(withProcesses(700)), "", ""); !errors.Is(err, ErrPolicyRevisionConflict) {
		t.Errorf("empty expectation: err = %v, want ErrPolicyRevisionConflict", err)
	}
	// A no-op edit is not a new revision.
	if _, err := appendTx(resolvedPolicy(withProcesses(600)), second.RevisionID, second.Digest); !errors.Is(err, ErrPolicyRevisionUnchanged) {
		t.Errorf("unchanged digest: err = %v, want ErrPolicyRevisionUnchanged", err)
	}
	// Caller authority and unconfined projects are rejected.
	chosen := resolvedPolicy(withProcesses(800))
	chosen.SecurityDomainID = first.DomainID
	if _, err := repo.AppendPolicyRevision(ctx, projectID, chosen, second.RevisionID, second.Digest, "operator"); !errors.Is(err, ErrPolicyAuthorityInput) {
		t.Errorf("caller-chosen domain: err = %v, want ErrPolicyAuthorityInput", err)
	}
	if _, err := repo.AppendPolicyRevision(ctx, projectID, resolvedPolicy(withProcesses(800)), second.RevisionID, second.Digest, ""); !errors.Is(err, ErrPolicyAuthorityInput) {
		t.Errorf("missing creator: err = %v, want ErrPolicyAuthorityInput", err)
	}
	other := registerPolicyProject(t, repo, root, "other", mkdir(t, filepath.Join(root, "other")))
	if _, err := repo.AppendPolicyRevision(ctx, other, resolvedPolicy(withProcesses(800)), second.RevisionID, second.Digest, "operator"); !errors.Is(err, ErrProjectNotConfined) {
		t.Errorf("unconfined project: err = %v, want ErrProjectNotConfined", err)
	}

	// Exactly one append survived; ordinals never fork, and every failure
	// above left neither rows nor audit events.
	after := policyStateSnapshot(t, d)
	for table, n := range before {
		want := n
		if table == "audit:"+AuditPolicyRevisionAppended {
			want++
		}
		if table == "project_policy_revisions" {
			want++
		}
		if after[table] != want {
			t.Errorf("%s = %d after, want %d", table, after[table], want)
		}
	}
}

func TestAppendPolicyRevisionSerializedUnderConcurrency(t *testing.T) {
	repo, d := newTestRepo(t)
	ctx := context.Background()
	root := mkdir(t, filepath.Join(t.TempDir(), "root"))
	projectID := registerPolicyProject(t, repo, root, "proj", mkdir(t, filepath.Join(root, "proj")))
	first := createPolicyDomain(t, repo, projectID, resolvedPolicy())

	const racers = 8
	start := make(chan struct{})
	results := make([]*StoredPolicyRevision, racers)
	errs := make([]error, racers)
	var wg sync.WaitGroup
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results[i], errs[i] = repo.AppendPolicyRevision(
				ctx, projectID, resolvedPolicy(withProcesses(uint64(600+i))),
				first.RevisionID, first.Digest, "operator")
		}()
	}
	close(start)
	wg.Wait()

	succeeded := -1
	for i := range racers {
		switch {
		case errs[i] == nil:
			if succeeded >= 0 {
				t.Fatal("two racers appended the same revision")
			}
			succeeded = i
		case errors.Is(errs[i], ErrPolicyRevisionConflict):
		default:
			t.Errorf("racer %d: err = %v, want success or ErrPolicyRevisionConflict", i, errs[i])
		}
	}
	if succeeded < 0 {
		t.Fatal("no racer succeeded")
	}
	var revisions int
	if err := d.Reader().QueryRowContext(ctx,
		`SELECT count(*) FROM project_policy_revisions WHERE security_domain_id = ?`,
		first.DomainID).Scan(&revisions); err != nil {
		t.Fatalf("count revisions: %v", err)
	}
	if revisions != 2 {
		t.Errorf("revisions = %d, want 2: a stale serialized append must conflict, not fork", revisions)
	}

	// A chain of retries against the fresh current revision keeps working.
	current := results[succeeded]
	for revision := 3; revision <= 5; revision++ {
		next, err := repo.AppendPolicyRevision(ctx, projectID,
			resolvedPolicy(withProcesses(uint64(700+revision))), current.RevisionID, current.Digest, "operator")
		if err != nil {
			t.Fatalf("append revision %d: %v", revision, err)
		}
		if next.Revision != int64(revision) {
			t.Fatalf("append revision = %d, want %d", next.Revision, revision)
		}
		current = next
	}
}

func TestGetPolicyRevisionScopedExactReadAndReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "control.db")
	open := func() (*Repository, *db.DB) {
		d, err := db.Open(ctx, db.Config{Path: path})
		if err != nil {
			t.Fatalf("db.Open: %v", err)
		}
		return NewRepository(d), d
	}
	repo, d := open()
	root := mkdir(t, filepath.Join(t.TempDir(), "root"))
	projectID := registerPolicyProject(t, repo, root, "proj", mkdir(t, filepath.Join(root, "proj")))
	first := createPolicyDomain(t, repo, projectID, resolvedPolicy())
	second, err := repo.AppendPolicyRevision(ctx, projectID, resolvedPolicy(withProcesses(600)),
		first.RevisionID, first.Digest, "operator")
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	got, err := repo.GetPolicyRevision(ctx, projectID, second.RevisionID)
	if err != nil {
		t.Fatalf("read second: %v", err)
	}
	if got.Digest != second.Digest || got.CanonicalJSON != second.CanonicalJSON ||
		got.Revision != 2 || got.DomainID != second.DomainID || got.Policy.Resources.Processes != 600 {
		t.Errorf("exact read = %+v, want the appended revision", got)
	}

	// Scope: an unknown revision, and a revision of a domain this project
	// never belonged to, are not found.
	if _, err := repo.GetPolicyRevision(ctx, projectID, "00112233445566778899aabbccddeeff"); !errors.Is(err, ErrPolicyRevisionNotFound) {
		t.Errorf("unknown revision: err = %v, want ErrPolicyRevisionNotFound", err)
	}
	otherProject := registerPolicyProject(t, repo, root, "other", mkdir(t, filepath.Join(root, "other")))
	otherDomain := createPolicyDomain(t, repo, otherProject,
		resolvedPolicy(withBacking(backingB), withMemoryScope(memoryB), withRemote(repoB, projectpolicy.RemotePermissionContribute)))
	if _, err := repo.GetPolicyRevision(ctx, otherProject, first.RevisionID); !errors.Is(err, ErrPolicyRevisionNotFound) {
		t.Errorf("foreign project: err = %v, want ErrPolicyRevisionNotFound", err)
	}
	if _, err := repo.GetPolicyRevision(ctx, projectID, otherDomain.RevisionID); !errors.Is(err, ErrPolicyRevisionNotFound) {
		t.Errorf("foreign revision: err = %v, want ErrPolicyRevisionNotFound", err)
	}

	// Reopen: retained bytes verify against the same schema, not memory.
	if err := d.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	repo2, d2 := open()
	t.Cleanup(func() { _ = d2.Close() })
	again, err := repo2.GetPolicyRevision(ctx, projectID, first.RevisionID)
	if err != nil {
		t.Fatalf("reread after reopen: %v", err)
	}
	if again.Digest != first.Digest || again.CanonicalJSON != first.CanonicalJSON {
		t.Error("revision identity changed across reopen")
	}
}

func TestPolicyRevisionVerificationFencesCorruptAuthority(t *testing.T) {
	repo, d := newTestRepo(t)
	ctx := context.Background()
	root := mkdir(t, filepath.Join(t.TempDir(), "root"))
	projectID := registerPolicyProject(t, repo, root, "proj", mkdir(t, filepath.Join(root, "proj")))
	stored := createPolicyDomain(t, repo, projectID, resolvedPolicy())

	insertRawRevision := func(id string, no int, digest, canonicalJSON string) {
		rawExec(t, d, `INSERT INTO project_policy_revisions
		     (revision_id, security_domain_id, revision_no, policy_digest,
		      canonical_json, created_at, created_by)
		 VALUES (?, ?, ?, ?, ?, '2026-08-14T12:00:00.000Z', 'hostile')`,
			id, stored.DomainID, no, digest, canonicalJSON)
	}

	// Digest mismatch: valid canonical bytes, wrong recorded digest.
	mismatchID := hexID(0xb1)
	insertRawRevision(mismatchID, 11, strings.Repeat("f", 64), stored.CanonicalJSON)
	if _, err := repo.GetPolicyRevision(ctx, projectID, mismatchID); !errors.Is(err, ErrPolicyRevisionCorrupt) {
		t.Errorf("digest mismatch: err = %v, want ErrPolicyRevisionCorrupt", err)
	}

	// Noncanonical bytes: every field present and decodable, but the byte
	// order is not the canonical encoding, so it names a different identity.
	canonicalHead := `{"schema":"` + projectpolicy.ProjectPolicySchemaV1 +
		`","security_domain_id":"` + stored.DomainID + `"`
	reordered := `{"security_domain_id":"` + stored.DomainID +
		`","schema":"` + projectpolicy.ProjectPolicySchemaV1 + `"` +
		stored.CanonicalJSON[len(canonicalHead):]
	noncanonicalID := hexID(0xb2)
	insertRawRevision(noncanonicalID, 12, stored.Digest, reordered)
	if _, err := repo.GetPolicyRevision(ctx, projectID, noncanonicalID); !errors.Is(err, ErrPolicyRevisionCorrupt) {
		t.Errorf("noncanonical bytes: err = %v, want ErrPolicyRevisionCorrupt", err)
	}

	// Domain mismatch: exact canonical bytes of a policy that names another
	// domain, filed under this one.
	foreign := resolvedPolicy()
	foreign.SecurityDomainID = hexID(0xfe)
	foreignRevision, err := projectpolicy.CanonicalizeProjectPolicy(foreign)
	if err != nil {
		t.Fatalf("canonicalize foreign: %v", err)
	}
	crossDomainID := hexID(0xb3)
	insertRawRevision(crossDomainID, 13, foreignRevision.Digest, foreignRevision.CanonicalJSON)
	if _, err := repo.GetPolicyRevision(ctx, projectID, crossDomainID); !errors.Is(err, ErrPolicyRevisionCorrupt) {
		t.Errorf("domain mismatch: err = %v, want ErrPolicyRevisionCorrupt", err)
	}

	// A corrupt latest revision fences the append path closed.
	before := policyStateSnapshot(t, d)
	if _, err := repo.AppendPolicyRevision(ctx, projectID, resolvedPolicy(withProcesses(600)),
		stored.RevisionID, stored.Digest, "operator"); !errors.Is(err, ErrPolicyRevisionCorrupt) {
		t.Errorf("append onto corrupt history: err = %v, want ErrPolicyRevisionCorrupt", err)
	}
	requireStateUnchanged(t, d, before)
}

// insertHostileAuthorityChain writes a complete active authority chain for a
// foreign domain through raw SQL, the way a writer with database access but
// no repository authority would. Its revision carries a real canonical policy
// so every verification the scan performs succeeds before the conflict rule
// fires.
func insertHostileAuthorityChain(t *testing.T, d *db.DB, hostProjectID string, policy projectpolicy.ProjectPolicyV1) (domainID string) {
	t.Helper()
	domainID = hexID(0xd0)
	membershipID := hexID(0xd1)
	revisionID := hexID(0xd2)
	policy.SecurityDomainID = domainID
	revision, err := projectpolicy.CanonicalizeProjectPolicy(policy)
	if err != nil {
		t.Fatalf("canonicalize hostile policy: %v", err)
	}
	rawExec(t, d, `INSERT INTO project_security_domains
	     (security_domain_id, display_label, created_at, created_by)
	 VALUES (?, 'hostile', '2026-08-14T12:00:00.000Z', 'hostile')`, domainID)
	rawExec(t, d, `INSERT INTO project_domain_memberships
	     (membership_id, project_id, security_domain_id, created_at, created_by)
	 VALUES (?, ?, ?, '2026-08-14T12:00:00.000Z', 'hostile')`, membershipID, hostProjectID, domainID)
	rawExec(t, d, `INSERT INTO project_policy_revisions
	     (revision_id, security_domain_id, revision_no, policy_digest,
	      canonical_json, created_at, created_by)
	 VALUES (?, ?, 1, ?, ?, '2026-08-14T12:00:00.000Z', 'hostile')`,
		revisionID, domainID, revision.Digest, revision.CanonicalJSON)
	return domainID
}

func TestPolicyAuthorityRejectsCanonicalPathOverlap(t *testing.T) {
	ctx := context.Background()
	setup := func(t *testing.T) (*Repository, *db.DB, string, string, string) {
		repo, d := newTestRepo(t)
		root := mkdir(t, filepath.Join(t.TempDir(), "root"))
		parentDir := mkdir(t, filepath.Join(root, "parent"))
		nestedDir := mkdir(t, filepath.Join(parentDir, "nested"))
		parent := registerPolicyProject(t, repo, root, "parent", parentDir)
		nested := registerPolicyProject(t, repo, root, "nested", nestedDir)
		sibling := registerPolicyProject(t, repo, root, "sibling", mkdir(t, filepath.Join(root, "sibling")))
		return repo, d, parent, nested, sibling
	}
	distinct := func(n uint64) projectpolicy.ProjectPolicyV1 {
		return resolvedPolicy(withBacking(fmt.Sprintf("%064x", 0x100+n)),
			withMemoryScope(fmt.Sprintf("%064x", 0x200+n)),
			withRemote(fmt.Sprintf("github.com/owner/repo-%d", n), projectpolicy.RemotePermissionContribute))
	}

	t.Run("nested after parent", func(t *testing.T) {
		repo, d, parent, nested, _ := setup(t)
		createPolicyDomain(t, repo, parent, distinct(1))
		before := policyStateSnapshot(t, d)
		if _, err := repo.CreatePolicyDomain(ctx, nested, distinct(2), "operator"); !errors.Is(err, ErrPolicyAuthorityConflict) {
			t.Errorf("nested domain: err = %v, want ErrPolicyAuthorityConflict", err)
		}
		requireStateUnchanged(t, d, before)
	})
	t.Run("parent after nested", func(t *testing.T) {
		repo, _, parent, nested, _ := setup(t)
		createPolicyDomain(t, repo, nested, distinct(2))
		if _, err := repo.CreatePolicyDomain(ctx, parent, distinct(1), "operator"); !errors.Is(err, ErrPolicyAuthorityConflict) {
			t.Errorf("parent domain: err = %v, want ErrPolicyAuthorityConflict", err)
		}
	})
	t.Run("siblings do not overlap", func(t *testing.T) {
		repo, _, parent, _, sibling := setup(t)
		createPolicyDomain(t, repo, parent, distinct(1))
		if _, err := repo.CreatePolicyDomain(ctx, sibling, distinct(3), "operator"); err != nil {
			t.Errorf("sibling domain: %v", err)
		}
	})
	t.Run("removed project retains its path binding", func(t *testing.T) {
		// Removal hides a project; it does not free its directory for a
		// second confined domain while the membership is retained.
		repo, d, parent, _, _ := setup(t)
		createPolicyDomain(t, repo, parent, distinct(1))
		var path string
		if err := d.Reader().QueryRowContext(ctx,
			`SELECT canonical_path FROM projects WHERE id = ?`, parent).Scan(&path); err != nil {
			t.Fatalf("read path: %v", err)
		}
		if err := repo.Remove(ctx, parent); err != nil {
			t.Fatalf("remove parent: %v", err)
		}
		reregistered := registerPolicyProject(t, repo, path, "reregistered", path)
		before := policyStateSnapshot(t, d)
		if _, err := repo.CreatePolicyDomain(ctx, reregistered, distinct(4), "operator"); !errors.Is(err, ErrPolicyAuthorityConflict) {
			t.Errorf("re-registered overlapping path: err = %v, want ErrPolicyAuthorityConflict", err)
		}
		requireStateUnchanged(t, d, before)
	})
	t.Run("append consults the path scan", func(t *testing.T) {
		// The repository cannot create an overlapping membership itself, so
		// the append-path scan is exercised through a hostile chain: the
		// project keeps its own domain, while a raw-inserted active
		// membership binds a foreign domain to a path nested inside it.
		repo, d, parent, _, _ := setup(t)
		first := createPolicyDomain(t, repo, parent, distinct(1))
		var parentPath string
		if err := d.Reader().QueryRowContext(ctx,
			`SELECT canonical_path FROM projects WHERE id = ?`, parent).Scan(&parentPath); err != nil {
			t.Fatalf("read path: %v", err)
		}
		hostileNested := mkdir(t, filepath.Join(parentPath, "hostile-nested"))
		hostileProject := registerPolicyProject(t, repo, parentPath, "hostile", hostileNested)
		insertHostileAuthorityChain(t, d, hostileProject, distinct(5))
		before := policyStateSnapshot(t, d)
		if _, err := repo.AppendPolicyRevision(ctx, parent, resolvedPolicy(withBacking(backingB),
			withMemoryScope(memoryB), withRemote(repoB, projectpolicy.RemotePermissionContribute),
			withProcesses(600)), first.RevisionID, first.Digest, "operator"); !errors.Is(err, ErrPolicyAuthorityConflict) {
			t.Errorf("append beside overlapping hostile membership: err = %v, want ErrPolicyAuthorityConflict", err)
		}
		requireStateUnchanged(t, d, before)
	})
}

func TestPolicyAuthorityRejectsDuplicateRetainedResources(t *testing.T) {
	ctx := context.Background()
	setup := func(t *testing.T) (*Repository, *db.DB, string, string) {
		repo, d := newTestRepo(t)
		root := mkdir(t, filepath.Join(t.TempDir(), "root"))
		first := registerPolicyProject(t, repo, root, "first", mkdir(t, filepath.Join(root, "first")))
		second := registerPolicyProject(t, repo, root, "second", mkdir(t, filepath.Join(root, "second")))
		return repo, d, first, second
	}
	firstPolicy := resolvedPolicy(withBacking(backingA), withMemoryScope(memoryA),
		withRemote(repoA, projectpolicy.RemotePermissionContribute))

	conflictCases := []struct {
		name      string
		candidate projectpolicy.ProjectPolicyV1
	}{
		{"duplicate retained backing", resolvedPolicy(withBacking(backingA),
			withMemoryScope(memoryB), withRemote(repoB, projectpolicy.RemotePermissionContribute))},
		{"duplicate writable remote", resolvedPolicy(withBacking(backingB),
			withMemoryScope(memoryB), withRemote(repoA, projectpolicy.RemotePermissionContribute))},
		{"duplicate writable memory scope", resolvedPolicy(withBacking(backingB),
			withMemoryScope(memoryA), withRemote(repoB, projectpolicy.RemotePermissionContribute))},
	}
	for _, tc := range conflictCases {
		t.Run(tc.name, func(t *testing.T) {
			repo, d, first, second := setup(t)
			createPolicyDomain(t, repo, first, firstPolicy)
			before := policyStateSnapshot(t, d)
			if _, err := repo.CreatePolicyDomain(ctx, second, tc.candidate, "operator"); !errors.Is(err, ErrPolicyAuthorityConflict) {
				t.Errorf("err = %v, want ErrPolicyAuthorityConflict", err)
			}
			requireStateUnchanged(t, d, before)
		})
	}

	t.Run("read-only mapping beside a writable one", func(t *testing.T) {
		repo, _, first, second := setup(t)
		createPolicyDomain(t, repo, first, firstPolicy)
		if _, err := repo.CreatePolicyDomain(ctx, second, resolvedPolicy(withBacking(backingB),
			withMemoryScope(memoryB), withRemote(repoA, projectpolicy.RemotePermissionRead)), "operator"); err != nil {
			t.Errorf("read-only candidate beside writable mapping: %v", err)
		}
	})
	t.Run("writable mapping beside a read-only one", func(t *testing.T) {
		repo, _, first, second := setup(t)
		createPolicyDomain(t, repo, first, resolvedPolicy(withBacking(backingA), withMemoryScope(memoryA),
			withRemote(repoA, projectpolicy.RemotePermissionRead)))
		if _, err := repo.CreatePolicyDomain(ctx, second, resolvedPolicy(withBacking(backingB),
			withMemoryScope(memoryB), withRemote(repoA, projectpolicy.RemotePermissionContribute)), "operator"); err != nil {
			t.Errorf("writable candidate beside read-only mapping: %v", err)
		}
	})
	t.Run("disjoint authority registers", func(t *testing.T) {
		repo, _, first, second := setup(t)
		createPolicyDomain(t, repo, first, firstPolicy)
		if _, err := repo.CreatePolicyDomain(ctx, second, resolvedPolicy(withBacking(backingB),
			withMemoryScope(memoryB), withRemote(repoB, projectpolicy.RemotePermissionContribute)), "operator"); err != nil {
			t.Errorf("disjoint domains: %v", err)
		}
	})
	t.Run("append rejects a hostile duplicate retained backing", func(t *testing.T) {
		repo, d, first, second := setup(t)
		stored := createPolicyDomain(t, repo, first, firstPolicy)
		// The hostile chain already binds backingA for another active domain,
		// so appending a revision that re-maps it must fail closed.
		hostileDraft := resolvedPolicy(withBacking(backingA),
			withMemoryScope(fmt.Sprintf("%064x", 0x321)),
			withRemote("github.com/hostile/repo", projectpolicy.RemotePermissionContribute))
		insertHostileAuthorityChain(t, d, second, hostileDraft)
		before := policyStateSnapshot(t, d)
		if _, err := repo.AppendPolicyRevision(ctx, first, resolvedPolicy(withBacking(backingA),
			withMemoryScope(memoryB), withRemote(repoB, projectpolicy.RemotePermissionContribute),
			withProcesses(600)), stored.RevisionID, stored.Digest, "operator"); !errors.Is(err, ErrPolicyAuthorityConflict) {
			t.Errorf("append re-mapping retained backing: err = %v, want ErrPolicyAuthorityConflict", err)
		}
		requireStateUnchanged(t, d, before)
	})
}

func TestInactiveAuthorityRowsAreInert(t *testing.T) {
	ctx := context.Background()

	t.Run("invalidated membership frees the project", func(t *testing.T) {
		repo, d := newTestRepo(t)
		root := mkdir(t, filepath.Join(t.TempDir(), "root"))
		projectID := registerPolicyProject(t, repo, root, "proj", mkdir(t, filepath.Join(root, "proj")))
		stored := createPolicyDomain(t, repo, projectID, resolvedPolicy())

		var membershipID string
		if err := d.Reader().QueryRowContext(ctx,
			`SELECT membership_id FROM project_domain_memberships WHERE security_domain_id = ?`,
			stored.DomainID).Scan(&membershipID); err != nil {
			t.Fatalf("read membership: %v", err)
		}
		invalidateRow(t, d, "project_domain_memberships", "membership_id", membershipID, "revoked", 1)

		if _, err := repo.AppendPolicyRevision(ctx, projectID, resolvedPolicy(withProcesses(600)),
			stored.RevisionID, stored.Digest, "operator"); !errors.Is(err, ErrProjectNotConfined) {
			t.Errorf("append through inert membership: err = %v, want ErrProjectNotConfined", err)
		}
		// The project may join a fresh domain, reusing the inert domain's
		// backing, remote, and memory scope.
		fresh := createPolicyDomain(t, repo, projectID, resolvedPolicy())
		if fresh.DomainID == stored.DomainID {
			t.Error("re-create joined the invalidated domain")
		}
		// History stays resolvable through the invalidated membership.
		if _, err := repo.GetPolicyRevision(ctx, projectID, stored.RevisionID); err != nil {
			t.Errorf("read through inert membership: %v", err)
		}
	})

	t.Run("restored revision is inert for appends", func(t *testing.T) {
		repo, d := newTestRepo(t)
		root := mkdir(t, filepath.Join(t.TempDir(), "root"))
		projectID := registerPolicyProject(t, repo, root, "proj", mkdir(t, filepath.Join(root, "proj")))
		first := createPolicyDomain(t, repo, projectID, resolvedPolicy())
		second, err := repo.AppendPolicyRevision(ctx, projectID,
			resolvedPolicy(withProcesses(600), withMemoryScope(memoryA)),
			first.RevisionID, first.Digest, "operator")
		if err != nil {
			t.Fatalf("append: %v", err)
		}
		invalidateRow(t, d, "project_policy_revisions", "revision_id", second.RevisionID, "restore", 2)

		if _, err := repo.AppendPolicyRevision(ctx, projectID, resolvedPolicy(withProcesses(700)),
			second.RevisionID, second.Digest, "operator"); !errors.Is(err, ErrPolicyRevisionConflict) {
			t.Errorf("append onto restored revision: err = %v, want ErrPolicyRevisionConflict", err)
		}
		// The restored revision stays readable and verifiable, but carries
		// its inert state so callers fence it.
		got, err := repo.GetPolicyRevision(ctx, projectID, second.RevisionID)
		if err != nil {
			t.Fatalf("read restored revision: %v", err)
		}
		if got.Invalidated == nil || got.Invalidated.Reason != "restore" {
			t.Errorf("restored revision invalidation = %+v, want reason restore", got.Invalidated)
		}
		if got.Digest != second.Digest {
			t.Error("restored revision bytes changed")
		}
		// The restored revision is inert, so its memory scope — held by no
		// active revision of that domain — no longer blocks another domain.
		// Were the restored row wrongly treated as retained authority, this
		// creation would conflict on the memory scope.
		other := registerPolicyProject(t, repo, root, "other", mkdir(t, filepath.Join(root, "other")))
		if _, err := repo.CreatePolicyDomain(ctx, other, resolvedPolicy(withBacking(backingB),
			withMemoryScope(memoryA), withRemote(repoB, projectpolicy.RemotePermissionContribute)), "operator"); err != nil {
			t.Fatalf("create domain reusing a restored revision's memory scope: %v", err)
		}
	})

	t.Run("revoked domain frees its resources", func(t *testing.T) {
		repo, d := newTestRepo(t)
		root := mkdir(t, filepath.Join(t.TempDir(), "root"))
		first := registerPolicyProject(t, repo, root, "first", mkdir(t, filepath.Join(root, "first")))
		second := registerPolicyProject(t, repo, root, "second", mkdir(t, filepath.Join(root, "second")))
		stored := createPolicyDomain(t, repo, first, resolvedPolicy())
		invalidateRow(t, d, "project_security_domains", "security_domain_id", stored.DomainID, "revoked", 3)
		if _, err := repo.CreatePolicyDomain(ctx, second, resolvedPolicy(), "operator"); err != nil {
			t.Errorf("create domain reusing revoked authority's resources: %v", err)
		}
	})

	t.Run("append through a revoked domain fails closed", func(t *testing.T) {
		repo, d := newTestRepo(t)
		root := mkdir(t, filepath.Join(t.TempDir(), "root"))
		projectID := registerPolicyProject(t, repo, root, "proj", mkdir(t, filepath.Join(root, "proj")))
		first := createPolicyDomain(t, repo, projectID, resolvedPolicy())
		second, err := repo.AppendPolicyRevision(ctx, projectID, resolvedPolicy(withProcesses(600)),
			first.RevisionID, first.Digest, "operator")
		if err != nil {
			t.Fatalf("append: %v", err)
		}
		// Only the domain row is invalidated; the membership itself stays
		// active, which must not keep admitting revisions to the domain.
		invalidateRow(t, d, "project_security_domains", "security_domain_id", first.DomainID, "revoked", 4)
		before := policyStateSnapshot(t, d)
		if _, err := repo.AppendPolicyRevision(ctx, projectID, resolvedPolicy(withProcesses(700)),
			second.RevisionID, second.Digest, "operator"); !errors.Is(err, ErrProjectNotConfined) {
			t.Errorf("append through revoked domain: err = %v, want ErrProjectNotConfined", err)
		}
		requireStateUnchanged(t, d, before)
	})

	t.Run("revoked domain frees its path", func(t *testing.T) {
		repo, d := newTestRepo(t)
		root := mkdir(t, filepath.Join(t.TempDir(), "root"))
		parentDir := mkdir(t, filepath.Join(root, "parent"))
		parent := registerPolicyProject(t, repo, root, "parent", parentDir)
		nested := registerPolicyProject(t, repo, root, "nested", mkdir(t, filepath.Join(parentDir, "nested")))
		stored := createPolicyDomain(t, repo, parent, resolvedPolicy())
		invalidateRow(t, d, "project_security_domains", "security_domain_id", stored.DomainID, "revoked", 5)
		// The candidate carries fully disjoint resources, so the only
		// authority the revoked domain could still contribute is its path.
		if _, err := repo.CreatePolicyDomain(ctx, nested, resolvedPolicy(withBacking(backingB),
			withMemoryScope(memoryB), withRemote(repoB, projectpolicy.RemotePermissionContribute)),
			"operator"); err != nil {
			t.Errorf("create domain nested under a revoked domain's path: %v", err)
		}
	})

	t.Run("retained history stays exact through a revoked domain", func(t *testing.T) {
		repo, d := newTestRepo(t)
		root := mkdir(t, filepath.Join(t.TempDir(), "root"))
		projectID := registerPolicyProject(t, repo, root, "proj", mkdir(t, filepath.Join(root, "proj")))
		first := createPolicyDomain(t, repo, projectID, resolvedPolicy())
		second, err := repo.AppendPolicyRevision(ctx, projectID, resolvedPolicy(withProcesses(600)),
			first.RevisionID, first.Digest, "operator")
		if err != nil {
			t.Fatalf("append: %v", err)
		}
		invalidateRow(t, d, "project_security_domains", "security_domain_id", first.DomainID, "revoked", 6)
		for _, stored := range []*StoredPolicyRevision{first, second} {
			got, err := repo.GetPolicyRevision(ctx, projectID, stored.RevisionID)
			if err != nil {
				t.Fatalf("read retained revision %d: %v", stored.Revision, err)
			}
			if got.DomainID != stored.DomainID || got.Revision != stored.Revision ||
				got.Digest != stored.Digest || got.CanonicalJSON != stored.CanonicalJSON {
				t.Errorf("retained revision %d drifted after domain invalidation", stored.Revision)
			}
			if got.Invalidated != nil {
				t.Errorf("retained revision %d carries an invalidation tuple; domain revocation must not rewrite history", stored.Revision)
			}
		}
	})
}
