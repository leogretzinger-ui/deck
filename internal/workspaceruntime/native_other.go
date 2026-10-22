//go:build !linux

package workspaceruntime

import (
	"context"
	"os"
)

type kernelFacts struct{}

func readKernel() (kernelFacts, error)          { return kernelFacts{}, ErrUnavailable }
func readKernelFor(string) (kernelFacts, error) { return kernelFacts{}, ErrUnavailable }
func privateDirectory(string, int) bool         { return false }
func privateFile(string, int) bool              { return false }
func (c *Controller) observe(context.Context) (State, error) {
	return State{Storage: "unknown", Runtime: "unknown"}, ErrUnavailable
}
func (c *Controller) prepareStores() error { return ErrUnavailable }

func (c *Controller) observeClosedProcesses(context.Context) error { return ErrUnavailable }

func (c *Controller) importRoot() (*os.Root, error) { return nil, ErrUnavailable }
