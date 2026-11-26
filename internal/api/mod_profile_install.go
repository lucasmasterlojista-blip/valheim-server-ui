package api

import (
	"net/http"

	"github.com/jonasthim/valheim-server-ui/internal/domain"
)

func installProfileSelectionHandler(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := InstanceID(r)
		if err != nil {
			WriteError(w, err)
			return
		}
		var input struct {
			Packages []domain.ProfilePackage `json:"packages"`
		}
		if err := DecodeJSON(r, &input); err != nil {
			WriteError(w, err)
			return
		}
		job, err := d.Mods.EnqueueProfileInstall(r.Context(), id, input.Packages, RequestedBy(r))
		if err != nil {
			WriteError(w, err)
			return
		}
		d.audit(r, "mods.profile_install", id, job.ID, map[string]any{"packages": len(input.Packages)})
		WriteJSON(w, http.StatusAccepted, map[string]any{"job": job})
	}
}
