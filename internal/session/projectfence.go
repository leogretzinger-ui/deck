package session

import (
	"errors"
	"sync"
)

// ErrProjectClosing refuses a new session, or the recovery of one, in a
// project whose working directory is about to be deleted.
var ErrProjectClosing = errors.New("session: the project is being closed")

// projectFence lets a caller that is about to delete a project's working
// directory stop new processes from being started there. A creation or a
// recovery holds a share of the fence from before it checks until its process
// is recorded, so a
// close that has taken the fence sees every session that will ever exist in
// the project, and none can start after.
type projectFence struct {
	mu       sync.Mutex
	idle     *sync.Cond
	closing  map[string]bool
	creating map[string]int
}

func newProjectFence() *projectFence {
	f := &projectFence{closing: map[string]bool{}, creating: map[string]int{}}
	f.idle = sync.NewCond(&f.mu)
	return f
}

// admit registers a creation in the project, or refuses it while the project
// is closing. The returned function ends the creation.
func (f *projectFence) admit(projectID string) (func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closing[projectID] {
		return nil, ErrProjectClosing
	}
	f.creating[projectID]++
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.creating[projectID]--; f.creating[projectID] == 0 {
			delete(f.creating, projectID)
			f.idle.Broadcast()
		}
	}, nil
}

// close marks the project closing, waits for creations already admitted to
// finish, and returns the function that lifts the fence.
func (f *projectFence) close(projectID string) func() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for f.closing[projectID] {
		f.idle.Wait()
	}
	f.closing[projectID] = true
	for f.creating[projectID] > 0 {
		f.idle.Wait()
	}
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		delete(f.closing, projectID)
		f.idle.Broadcast()
	}
}

// HoldProject refuses new sessions and recoveries in the project until the
// returned function is called, after those already admitted there have been
// recorded.
func (m *Manager) HoldProject(projectID string) (release func()) {
	return m.fence.close(projectID)
}
