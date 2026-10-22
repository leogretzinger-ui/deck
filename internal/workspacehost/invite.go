package workspacehost

import (
	"context"
	"os"
	"time"

	"github.com/noisefloor-oss/deck/internal/workspacegateway"
	"github.com/noisefloor-oss/deck/internal/workspacekeys"
)

// Invite exports a one-use enrollment link into the owner's RAM-backed runtime
// directory. The control database stores only its hash; stdout never gets it.
// Issuing a link does not reopen the native controller or reset live ceremonies.
func Invite(ctx context.Context, cfg Config) (string, time.Time, error) {
	if cfg.Validate() != nil || !privateFile(cfg.control(), cfg.Native.OwnerUID) ||
		!volatileDirectory(cfg.Native.RuntimeDir, cfg.Native.OwnerUID) {
		return "", time.Time{}, ErrConfiguration
	}
	db, err := workspacegateway.OpenControl(cfg.control())
	if err != nil {
		return "", time.Time{}, ErrUnavailable
	}
	defer func() { _ = db.Close() }()
	gatewayConfig := cfg.Gateway()
	keys, err := workspacekeys.OpenExisting(ctx, db, workspacekeys.Config{InstanceID: gatewayConfig.InstanceID,
		Origin: gatewayConfig.Origin, RPID: gatewayConfig.RPID, EncryptionPolicy: cfg.policy()})
	if err != nil {
		return "", time.Time{}, ErrUnavailable
	}
	invitations, err := workspacegateway.NewInvitations(ctx, db, keys, cfg.AllowedEmail, nil)
	if err != nil {
		return "", time.Time{}, ErrUnavailable
	}
	token, expires, err := invitations.Issue(ctx)
	if err != nil {
		return "", time.Time{}, workspacegateway.ErrInvitation
	}
	file, err := os.CreateTemp(cfg.Native.RuntimeDir, "deck-invitation-*.txt")
	if err != nil {
		return "", time.Time{}, ErrUnavailable
	}
	path := file.Name()
	_, writeErr := file.WriteString(cfg.Origin + "/#invite=" + token + "\n")
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(path)
		return "", time.Time{}, ErrUnavailable
	}
	return path, expires, nil
}
