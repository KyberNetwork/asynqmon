package asynqmon

import (
	"encoding/json"
	"net/http"
)

// instanceInfo is the JSON representation of a redis instance exposed to the
// Web UI (both via the /api/instances endpoint and the injected window.INSTANCES).
type instanceInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// instancePayload converts the internal instance list into the UI-facing payload.
func instancePayload(instances []redisInstance) []instanceInfo {
	payload := make([]instanceInfo, 0, len(instances))
	for _, inst := range instances {
		payload = append(payload, instanceInfo{ID: inst.id, Name: inst.name})
	}
	return payload
}

func newListInstancesHandlerFunc(instances []redisInstance) http.HandlerFunc {
	payload := instancePayload(instances)
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(payload); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
}
