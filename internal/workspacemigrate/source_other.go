//go:build !linux

package workspacemigrate

import (
	"context"
	"errors"
)

var ErrSource = errors.New("workspace migration: Linux source required")

type SourceConfig struct{}

func LoadSourceConfig(string) (SourceConfig, error)          { return SourceConfig{}, ErrSource }
func Snapshot(context.Context, SourceConfig) (string, error) { return "", ErrSource }
