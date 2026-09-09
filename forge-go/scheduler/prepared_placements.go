package scheduler

import (
	"sync"
	"time"
)

type PreparedPlacement struct {
	PreparationID string
	GuildID       string
	AgentID       string
	NodeID        string
	Capacity      ResourceCapacity
	ExpiresAt     time.Time
}

type PreparedPlacementMap struct {
	mu         sync.Mutex
	placements map[string]PreparedPlacement
}

func NewPreparedPlacementMap() *PreparedPlacementMap {
	return &PreparedPlacementMap{placements: map[string]PreparedPlacement{}}
}

func preparedPlacementKey(guildID, agentID string) string { return guildID + ":" + agentID }

func (m *PreparedPlacementMap) Reserve(value PreparedPlacement) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.placements[preparedPlacementKey(value.GuildID, value.AgentID)] = value
}

func (m *PreparedPlacementMap) Find(guildID, agentID string) (PreparedPlacement, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	value, ok := m.placements[preparedPlacementKey(guildID, agentID)]
	return value, ok
}

func (m *PreparedPlacementMap) Consume(guildID, agentID string) (PreparedPlacement, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := preparedPlacementKey(guildID, agentID)
	value, ok := m.placements[key]
	if ok {
		delete(m.placements, key)
	}
	return value, ok
}

func (m *PreparedPlacementMap) RemovePreparation(preparationID string) []PreparedPlacement {
	m.mu.Lock()
	defer m.mu.Unlock()
	removed := []PreparedPlacement{}
	for key, value := range m.placements {
		if value.PreparationID == preparationID {
			removed = append(removed, value)
			delete(m.placements, key)
		}
	}
	return removed
}

var GlobalPreparedPlacementMap = NewPreparedPlacementMap()
