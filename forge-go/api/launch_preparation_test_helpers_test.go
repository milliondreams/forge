package api

import "time"

func authorizePreparedLaunchForTest(server *Server, blueprintID string, request *LaunchGuildFromBlueprintRequest) {
	preparationID := "prep-test-" + *request.GuildID
	record := &launchPreparationRecord{
		response: LaunchPreparationResponse{
			ID:          preparationID,
			Fingerprint: request.Fingerprint,
			Status:      "ready",
			Phase:       "complete",
			ExpiresAt:   time.Now().UTC().Add(time.Minute),
		},
		userID: request.UserID, orgID: request.OrgID, blueprintID: blueprintID,
		guildID: *request.GuildID, preflightID: request.PreflightID,
	}
	store := server.preparationStore()
	store.mu.Lock()
	store.records[preparationID] = record
	store.mu.Unlock()
	request.PreparationID = preparationID
}
