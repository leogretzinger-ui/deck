package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func migrateThrough(t *testing.T, database *sql.DB, maxVersion int) []Migration {
	t.Helper()
	ctx := context.Background()
	migrations, err := LoadMigrations()
	if err != nil {
		t.Fatalf("load migrations: %v", err)
	}
	if _, err := database.ExecContext(ctx, createSchemaMigrations); err != nil {
		t.Fatalf("create schema migrations: %v", err)
	}
	for _, migration := range migrations {
		if migration.Version > maxVersion {
			break
		}
		if err := applyOne(ctx, database, migration); err != nil {
			t.Fatalf("apply migration %d: %v", migration.Version, err)
		}
	}
	return migrations
}

func TestMigrate024BackfillsProfilesWithoutDroppingLegacyProjects(t *testing.T) {
	database := open(t, filepath.Join(t.TempDir(), "control.db"), 5000)
	defer func() { _ = database.Close() }()
	migrations := migrateThrough(t, database, 23)

	projects := []struct {
		id, name, email, created, used string
		removed                        any
	}{
		{"active-old", "Deck Author", "deck@example.invalid", "2026-08-01T00:00:00.000Z", "2026-08-10T00:00:00.000Z", nil},
		{"active-new", "Work Author", "work@example.invalid", "2026-08-02T00:00:00.000Z", "2026-08-12T00:00:00.000Z", nil},
		{"removed-newer", "Deck Author", "deck@example.invalid", "2026-08-03T00:00:00.000Z", "2026-08-13T00:00:00.000Z", "2026-08-13T01:00:00.000Z"},
		{"incomplete", "", "", "2026-08-04T00:00:00.000Z", "2026-08-11T00:00:00.000Z", nil},
	}
	for _, project := range projects {
		if _, err := database.Exec(`
			INSERT INTO projects (
			  id, name, canonical_path, git_author_name, git_author_email,
			  created_at, last_used_at, removed_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			project.id, project.id, "/workspace/"+project.id, project.name,
			project.email, project.created, project.used, project.removed); err != nil {
			t.Fatalf("seed project %s: %v", project.id, err)
		}
	}

	var migration024 *Migration
	for i := range migrations {
		if migrations[i].Version == 24 {
			migration024 = &migrations[i]
			break
		}
	}
	if migration024 == nil {
		t.Fatal("migration 024 not embedded")
	}
	if err := applyOne(context.Background(), database, *migration024); err != nil {
		t.Fatalf("apply migration 024: %v", err)
	}

	var profiles, snapshots int
	if err := database.QueryRow(`SELECT count(*) FROM git_identity_profiles`).Scan(&profiles); err != nil {
		t.Fatalf("count profiles: %v", err)
	}
	if err := database.QueryRow(`SELECT count(*) FROM project_git_identity_snapshots`).Scan(&snapshots); err != nil {
		t.Fatalf("count snapshots: %v", err)
	}
	if profiles != 2 || snapshots != 3 {
		t.Fatalf("backfill counts profiles=%d snapshots=%d, want 2 and 3", profiles, snapshots)
	}

	var defaultName string
	var catalogRevision int
	if err := database.QueryRow(`
		SELECT p.name, c.revision
		FROM git_identity_catalog c
		JOIN git_identity_profiles p ON p.id = c.default_profile_id
		WHERE c.singleton = 1`).Scan(&defaultName, &catalogRevision); err != nil {
		t.Fatalf("read default: %v", err)
	}
	if defaultName != "Work Author" || catalogRevision != 1 {
		t.Fatalf("default = %q revision=%d, want Work Author revision 1", defaultName, catalogRevision)
	}

	for _, id := range []string{"active-old", "active-new", "removed-newer"} {
		var profileID string
		var profileRevision, setupRevision int
		if err := database.QueryRow(`
			SELECT git_identity_profile_id, git_identity_profile_revision,
			       git_identity_setup_revision
			FROM projects WHERE id = ?`, id,
		).Scan(&profileID, &profileRevision, &setupRevision); err != nil {
			t.Fatalf("read project %s identity: %v", id, err)
		}
		if len(profileID) != 32 || profileRevision != 1 || setupRevision != 1 {
			t.Errorf("project %s identity = %q/%d/%d", id, profileID, profileRevision, setupRevision)
		}
	}

	var incompleteProfile any
	var incompleteProfileRevision any
	var incompleteSetupRevision int
	if err := database.QueryRow(`
		SELECT git_identity_profile_id, git_identity_profile_revision,
		       git_identity_setup_revision
		FROM projects WHERE id = 'incomplete'`,
	).Scan(&incompleteProfile, &incompleteProfileRevision, &incompleteSetupRevision); err != nil {
		t.Fatalf("read incomplete project: %v", err)
	}
	if incompleteProfile != nil || incompleteProfileRevision != nil || incompleteSetupRevision != 0 {
		t.Fatalf("incomplete project was backfilled: %v/%v/%d", incompleteProfile, incompleteProfileRevision, incompleteSetupRevision)
	}

	if _, err := database.Exec(`
		UPDATE project_git_identity_snapshots SET name = 'changed'
		WHERE project_id = 'active-old' AND revision = 1`); err == nil {
		t.Fatal("immutable project snapshot accepted an update")
	}
	if _, err := database.Exec(`
		UPDATE git_identity_profiles SET deleted_at = '2026-08-13T02:00:00.000Z'
		WHERE id = (SELECT default_profile_id FROM git_identity_catalog WHERE singleton = 1)`); err == nil {
		t.Fatal("default profile was deleted before replacement")
	}
}

func TestMigrate024EmptyInstallationHasEmptyCatalog(t *testing.T) {
	database := open(t, filepath.Join(t.TempDir(), "control.db"), 5000)
	defer func() { _ = database.Close() }()
	if _, err := Migrate(context.Background(), database); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	var defaultID any
	var revision int
	if err := database.QueryRow(`
		SELECT default_profile_id, revision FROM git_identity_catalog WHERE singleton = 1`,
	).Scan(&defaultID, &revision); err != nil {
		t.Fatalf("read catalog: %v", err)
	}
	if defaultID != nil || revision != 0 {
		t.Fatalf("empty catalog = %v revision %d, want nil/0", defaultID, revision)
	}
}
