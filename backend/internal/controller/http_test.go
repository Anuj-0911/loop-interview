package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"loop-interview/backend/internal/service"
	"loop-interview/backend/internal/storage"
)

func TestHTTPBookingFlow(t *testing.T) {
	db, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "api.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := storage.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	store := storage.New(db)
	if err := store.SeedDemo(context.Background()); err != nil {
		t.Fatal(err)
	}
	handler := New(service.New(store))
	request := func(method, path, body string, status int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != status {
			t.Fatalf("%s %s: status=%d, want %d, body=%s", method, path, rec.Code, status, rec.Body.String())
		}
		return rec
	}
	rec := request("GET", "/slots?resourceId=room-1", "", http.StatusOK)
	var slots []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &slots); err != nil || len(slots) == 0 {
		t.Fatalf("listing: %s, %v", rec.Body.String(), err)
	}
	slotPath := "/slots/" + slots[0].ID + "/hold"
	rec = request("POST", slotPath, `{"userId":"user-1"}`, http.StatusCreated)
	var hold struct {
		ID         string `json:"id"`
		ExpiresAt  string `json:"expiresAt"`
		ServerTime string `json:"serverTime"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &hold); err != nil || hold.ID == "" || hold.ExpiresAt == "" || hold.ServerTime == "" {
		t.Fatalf("hold: %s, %v", rec.Body.String(), err)
	}
	request("POST", slotPath, `{"userId":"user-2"}`, http.StatusConflict)
	request("POST", "/holds/"+hold.ID+"/confirm", `{"userId":"user-2"}`, http.StatusForbidden)
	rec = request("POST", "/holds/"+hold.ID+"/confirm", `{"userId":"user-1"}`, http.StatusOK)
	var booking struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &booking); err != nil || booking.ID == "" {
		t.Fatalf("booking: %s, %v", rec.Body.String(), err)
	}
	request("POST", "/holds/"+hold.ID+"/confirm", `{"userId":"user-1"}`, http.StatusOK)
	request("DELETE", "/bookings/"+booking.ID, `{"userId":"user-1"}`, http.StatusNoContent)
	rec = request("POST", slotPath, `{"userId":"user-2"}`, http.StatusCreated)
	if err := json.Unmarshal(rec.Body.Bytes(), &hold); err != nil {
		t.Fatal(err)
	}
	request("DELETE", "/holds/"+hold.ID, `{"userId":"user-2"}`, http.StatusNoContent)
	request("POST", slotPath, `{`, http.StatusBadRequest)
	request("POST", slotPath, `{"userId":"user-1","unexpected":true}`, http.StatusBadRequest)
	request("POST", slotPath, `{"userId":"user-1"} {}`, http.StatusBadRequest)
	request("POST", slotPath, `{}`, http.StatusBadRequest)
	request("POST", "/holds/missing/confirm", `{"userId":"user-1"}`, http.StatusNotFound)
	request("GET", "/slots", "", http.StatusBadRequest)
	request("GET", "/health", "", http.StatusOK)
}
