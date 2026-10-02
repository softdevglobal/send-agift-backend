package handlers

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/google/uuid"

	"myapp/internal/middleware"
	"myapp/internal/services"
	"myapp/internal/utils"
)

// PushHandler lets the mobile app register the device it runs on for push
// notifications, and remove it on sign-out.
type PushHandler struct {
	push *services.PushService
}

func NewPushHandler(push *services.PushService) *PushHandler {
	return &PushHandler{push: push}
}

func pushCustomer(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	raw, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	id, err := uuid.Parse(raw)
	if err != nil {
		utils.Error(w, http.StatusUnauthorized, "sign in required")
		return uuid.Nil, false
	}
	return id, true
}

// RegisterDevice handles POST /customers/me/push-devices with
// {"token": "...", "platform": "android" | "ios", "app_version": "1.0.0"}.
func (h *PushHandler) RegisterDevice(w http.ResponseWriter, r *http.Request) {
	customerID, ok := pushCustomer(w, r)
	if !ok {
		return
	}
	var in services.PushDeviceInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.push.RegisterDevice(r.Context(), customerID, in); err != nil {
		h.writeError(w, err, "could not register device")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// UnregisterDevice handles DELETE /customers/me/push-devices with
// {"token": "..."}.
func (h *PushHandler) UnregisterDevice(w http.ResponseWriter, r *http.Request) {
	customerID, ok := pushCustomer(w, r)
	if !ok {
		return
	}
	var in struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.push.UnregisterDevice(r.Context(), customerID, in.Token); err != nil {
		h.writeError(w, err, "could not remove device")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *PushHandler) writeError(w http.ResponseWriter, err error, fallback string) {
	if errors.Is(err, services.ErrInvalidPushDevice) {
		utils.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	log.Printf("%s: %v", fallback, err)
	utils.Error(w, http.StatusInternalServerError, fallback)
}
