//go:build !linux

package workspaceplain

import "context"

type identity struct {
	Device uint64
	Inode  uint64
}

func privateIdentity(string, int, bool) (identity, error) { return identity{}, ErrUnavailable }
func processIdentity(int, int) (string, error)            { return "", ErrUnavailable }
func bootIdentity() (string, error)                       { return "", ErrUnavailable }
func (c *Controller) layout(bool) (bool, error)           { return false, ErrUnavailable }
func (c *Controller) prepare() error                      { return ErrUnavailable }
func (c *Controller) observe(context.Context) (State, error) {
	return State{Runtime: "unknown"}, ErrUnavailable
}
