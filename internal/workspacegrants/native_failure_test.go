package workspacegrants

import (
	"context"
	"testing"

	"github.com/noisefloor-oss/deck/internal/workspacekeys"
)

func TestNativeObservationStorageFailuresCloseStreamsUntilRestart(t *testing.T) {
	for _, completion := range []bool{false, true} {
		name := "epoch"
		if completion {
			name = "completion"
		}
		for _, failure := range []string{"begin", "read", "instance-write", "challenge-delete", "commit", "grant-delete", "issuance-fence"} {
			if !completion && (failure == "grant-delete" || failure == "issuance-fence") {
				continue
			}
			t.Run(name+"/"+failure, func(t *testing.T) {
				f := newFixture(t, true)
				delivery := f.grant(f.day)
				closed, release := attach(t, f, f.actor.Login, delivery)
				defer release()
				permit := f.prove(f.day, workspacekeys.Action{Operation: workspacekeys.Unlock, Method: f.day.ref})
				_, err := f.keys.BeginChallenge(f.ctx, f.actor, workspacekeys.Action{Operation: workspacekeys.AuthorizeBrowser, Method: f.day.ref})
				must(t, err)
				ctx := f.ctx
				var restore func()
				switch failure {
				case "begin":
					var cancel context.CancelFunc
					ctx, cancel = context.WithCancel(ctx)
					cancel()
				case "read":
					_, err = f.db.Exec(`ALTER TABLE workspacekeys_instance RENAME TO unavailable_instance`)
					restore = func() {
						_, e := f.db.Exec(`ALTER TABLE unavailable_instance RENAME TO workspacekeys_instance`)
						must(t, e)
					}
				case "instance-write":
					_, err = f.db.Exec(`CREATE TRIGGER refuse_write BEFORE UPDATE ON workspacekeys_instance BEGIN SELECT RAISE(ABORT,'synthetic write failure'); END`)
				case "challenge-delete":
					_, err = f.db.Exec(`CREATE TRIGGER refuse_write BEFORE DELETE ON workspacekeys_challenges BEGIN SELECT RAISE(ABORT,'synthetic write failure'); END`)
				case "grant-delete":
					_, err = f.db.Exec(`CREATE TRIGGER refuse_write BEFORE DELETE ON workspacegrants_grants BEGIN SELECT RAISE(ABORT,'synthetic write failure'); END`)
				case "issuance-fence":
					_, err = f.db.Exec(`CREATE TRIGGER refuse_write BEFORE INSERT ON workspacegrants_claims BEGIN SELECT RAISE(ABORT,'synthetic write failure'); END`)
				case "commit":
					_, err = f.db.Exec(`PRAGMA foreign_keys=ON; CREATE TABLE parent(id INTEGER PRIMARY KEY); CREATE TABLE child(id INTEGER REFERENCES parent(id) DEFERRABLE INITIALLY DEFERRED); CREATE TRIGGER refuse_write AFTER UPDATE ON workspacekeys_instance BEGIN INSERT INTO child VALUES(1); END`)
				}
				must(t, err)
				if completion {
					_, err = f.store.CompleteNative(ctx, f.actor.Login, permit, "observed-new-epoch")
				} else {
					err = f.keys.SetRuntimeEpoch(ctx, "observed-new-epoch")
				}
				denied(t, err)
				if closed.Load() != 1 || !f.store.fault.Load() {
					t.Fatal("native observation failure left old stream or admission open")
				}
				if restore != nil {
					restore()
				} else if failure != "begin" {
					_, err = f.db.Exec(`DROP TRIGGER refuse_write`)
					must(t, err)
				}
				denied(t, f.store.Require(WithToken(f.ctx, delivery.Token()), f.actor.Login))
				var count int
				must(t, f.db.QueryRow(`SELECT count(*) FROM workspacegrants_grants`).Scan(&count))
				if count != 1 {
					t.Fatal("failure fixture did not preserve rolled-back grant")
				}
				_, err = f.store.Issue(f.ctx, f.actor.Login, permit)
				denied(t, err)
			})
		}
	}
}
