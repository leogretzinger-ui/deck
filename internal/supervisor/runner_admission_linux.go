package supervisor

import "context"

// AdmitRunnerLaunch authenticates and measures one request received from an
// already-accepted runner connection, closes its received descriptors, and
// reserves the bound prepared run. It neither owns the connection nor starts
// the run. Receiving is intentionally blocking: the caller cancels a blocked
// admission by closing its connection. ctx applies after a complete
// authenticated message is received, when Store.Reserve begins.
func AdmitRunnerLaunch(ctx context.Context, conn int, runner RunnerAdmission, store *Store) (*Run, error) {
	request, binding, fds, err := AcceptRunnerRequest(conn, runner)
	if err != nil {
		return nil, err
	}
	closeAll(fds)
	return store.Reserve(ctx, ReserveInput{
		ApplicationSessionID:         request.Request.ApplicationSessionID,
		ApplicationSessionGeneration: request.Request.ApplicationSessionGeneration,
		LaunchRequest:                request,
		PTYBinding:                   binding,
	})
}
