package service

// Convenience entry points for fixtures; production uses the control API.
// createClient issues a single-device credential and a single-use invitation.
func (s *runtimeState) createClient(owner string) (map[string]any, error) {
	return s.createClientMode(owner, false, defaultInvitationTimeoutSeconds)
}

func (s *runtimeState) listClients(secret string) []clientInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listClientsLocked(secret)
}
