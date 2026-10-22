//go:build !linux

package workspaceruntime

import "context"

func (*Controller) verifyGatewayIngress(context.Context) error { return ErrUnavailable }
