package controller

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"time"

	"loop-interview/backend/internal/service"
)

type handler struct{ service *service.Service }

// New exposes the booking API. Demo identity is supplied as userId in JSON.
func New(svc *service.Service) http.Handler {
	h := handler{service: svc}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		respond(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /slots", h.slots)
	for _, prefix := range []string{"/resources", "/resource"} {
		mux.HandleFunc("GET "+prefix+"/{id}/slots", h.resourceSlots)
		mux.HandleFunc("PUT "+prefix+"/{id}/availability", h.availability)
	}
	mux.HandleFunc("POST /slots/{id}/hold", h.hold)
	mux.HandleFunc("POST /holds/{id}/confirm", h.confirm)
	mux.HandleFunc("DELETE /holds/{id}", h.release)
	mux.HandleFunc("DELETE /bookings/{id}", h.cancel)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (h handler) slots(w http.ResponseWriter, r *http.Request) {
	h.listSlots(w, r, r.URL.Query().Get("resourceId"), r.URL.Query().Get("date"))
}
func (h handler) resourceSlots(w http.ResponseWriter, r *http.Request) {
	resourceID, date := r.PathValue("id"), r.URL.Query().Get("date")
	if _, err := h.service.GenerateSlots(r.Context(), resourceID, date); err != nil {
		fail(w, err)
		return
	}
	h.listSlots(w, r, resourceID, date)
}
func (h handler) listSlots(w http.ResponseWriter, r *http.Request, resourceID, date string) {
	slots, err := h.service.ListSlots(r.Context(), resourceID, date)
	if err != nil {
		fail(w, err)
		return
	}
	result := make([]map[string]any, 0, len(slots))
	for _, view := range slots {
		slot := view.Slot
		result = append(result, map[string]any{"id": slot.ID, "resourceId": slot.ResourceID, "availabilityConfigId": slot.AvailabilityConfigID, "startTime": slot.StartTime, "endTime": slot.EndTime, "status": view.Status})
	}
	respond(w, http.StatusOK, result)
}
func (h handler) availability(w http.ResponseWriter, r *http.Request) {
	var input struct {
		StartTime           time.Time `json:"startTime"`
		EndTime             time.Time `json:"endTime"`
		SlotDurationMinutes int       `json:"slotDurationMinutes"`
	}
	if !decode(w, r, &input) {
		return
	}
	config, err := h.service.ConfigureAvailability(r.Context(), r.PathValue("id"), service.AvailabilityInput{StartTime: input.StartTime, EndTime: input.EndTime, SlotDurationMinutes: input.SlotDurationMinutes})
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusOK, map[string]any{"id": config.ID, "resourceId": config.ResourceID, "startTime": config.StartTime, "endTime": config.EndTime, "slotDurationMinutes": config.SlotDurationMinutes})
}
func (h handler) hold(w http.ResponseWriter, r *http.Request) {
	mutate(w, r, http.StatusCreated, func(userID string) (any, error) {
		hold, err := h.service.CreateHold(r.Context(), r.PathValue("id"), userID)
		return map[string]any{"id": hold.ID, "slotId": hold.SlotID, "userId": hold.UserID, "expiresAt": hold.ExpiresAt, "status": hold.Status, "serverTime": time.Now().UTC()}, err
	})
}
func (h handler) confirm(w http.ResponseWriter, r *http.Request) {
	mutate(w, r, http.StatusOK, func(userID string) (any, error) {
		booking, err := h.service.ConfirmHold(r.Context(), r.PathValue("id"), userID)
		return map[string]any{"id": booking.ID, "slotId": booking.SlotID, "userId": booking.UserID, "holdId": booking.HoldID, "status": booking.Status}, err
	})
}
func (h handler) release(w http.ResponseWriter, r *http.Request) {
	mutate(w, r, http.StatusNoContent, func(userID string) (any, error) {
		return nil, h.service.ReleaseHold(r.Context(), r.PathValue("id"), userID)
	})
}
func (h handler) cancel(w http.ResponseWriter, r *http.Request) {
	mutate(w, r, http.StatusNoContent, func(userID string) (any, error) {
		return nil, h.service.CancelBooking(r.Context(), r.PathValue("id"), userID)
	})
}
func mutate(w http.ResponseWriter, r *http.Request, status int, action func(string) (any, error)) {
	var input struct {
		UserID string `json:"userId"`
	}
	if !decode(w, r, &input) {
		return
	}
	result, err := action(input.UserID)
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, status, result)
}
func decode(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		badJSON(w, err)
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		badJSON(w, err)
		return false
	}
	return true
}
func badJSON(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		respond(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "body_too_large", "message": "Request body exceeds 64 KiB."})
		return
	}
	respond(w, http.StatusBadRequest, map[string]string{"error": "invalid_json", "message": "Provide one valid JSON object with supported fields."})
}
func respond(w http.ResponseWriter, status int, value any) {
	if status == http.StatusNoContent {
		w.WriteHeader(status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("encode response: %v", err)
	}
}
func fail(w http.ResponseWriter, err error) {
	status, code, message := http.StatusInternalServerError, "internal_error", "An internal error occurred."
	switch {
	case errors.Is(err, service.ErrInvalidInput):
		status, code, message = http.StatusBadRequest, "invalid_input", err.Error()
	case errors.Is(err, service.ErrResourceNotFound):
		status, code, message = http.StatusNotFound, "resource_not_found", "Resource not found."
	case errors.Is(err, service.ErrSlotNotFound):
		status, code, message = http.StatusNotFound, "slot_not_found", "Slot not found."
	case errors.Is(err, service.ErrUserNotFound):
		status, code, message = http.StatusNotFound, "user_not_found", "User not found."
	case errors.Is(err, service.ErrHoldNotFound):
		status, code, message = http.StatusNotFound, "hold_not_found", "Hold not found."
	case errors.Is(err, service.ErrBookingNotFound):
		status, code, message = http.StatusNotFound, "booking_not_found", "Booking not found."
	case errors.Is(err, service.ErrForbidden):
		status, code, message = http.StatusForbidden, "forbidden", "This reservation belongs to another user."
	case errors.Is(err, service.ErrHoldExpired):
		status, code, message = http.StatusGone, "hold_expired", "The hold has expired."
	case errors.Is(err, service.ErrSlotUnavailable):
		status, code, message = http.StatusConflict, "slot_unavailable", "The slot is held or booked."
	case errors.Is(err, service.ErrHoldNotActive):
		status, code, message = http.StatusConflict, "hold_not_active", "The hold cannot be changed in its current state."
	case errors.Is(err, service.ErrBookingNotActive):
		status, code, message = http.StatusConflict, "booking_not_active", "The booking cannot be cancelled in its current state."
	case errors.Is(err, service.ErrAvailabilityOverlap):
		status, code, message = http.StatusConflict, "availability_overlap", "The availability overlaps an existing window."
	case errors.Is(err, service.ErrSlotOverlap):
		status, code, message = http.StatusConflict, "slot_overlap", "The slots overlap existing slots."
	case errors.Is(err, context.DeadlineExceeded):
		status, code, message = http.StatusGatewayTimeout, "timeout", "The request timed out."
	case errors.Is(err, context.Canceled):
		status, code, message = http.StatusRequestTimeout, "request_cancelled", "The request was cancelled."
	}
	if status == http.StatusInternalServerError {
		log.Printf("API error: %v", err)
	}
	respond(w, status, map[string]string{"error": code, "message": message})
}
