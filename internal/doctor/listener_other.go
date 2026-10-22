//go:build !linux && !darwin

package doctor

import "github.com/noisefloor-oss/deck/internal/config"

func ownedListener(*config.Config) bool { return false }
