package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Figueredo-D/PBL_REDES/internal/domain"
	"github.com/Figueredo-D/PBL_REDES/internal/protocol"
)

type memoryRoundTripper struct {
	mu       sync.RWMutex
	handlers map[string]http.Handler
	offline  map[string]bool
}

func (m *memoryRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	m.mu.RLock()
	off := m.offline[req.URL.Host]
	h, ok := m.handlers[req.URL.Host]
	m.mu.RUnlock()

	if off || !ok {
		return nil, fmt.Errorf("servidor %s indisponível", req.URL.Host)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Result(), nil
}

func sendDirectRequest(t *testing.T, srv *Server, action, token string, payload interface{}) protocol.Response {
	t.Helper()

	var dataStr string
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("falha ao serializar payload: %v", err)
		}
		dataStr = string(b)
	}

	return srv.route(protocol.Request{
		Action: action,
		Token:  token,
		Data:   dataStr,
	})
}

func TestCancelRidePropagatesAndReleasesSeatsAcrossServers(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cancel_ride_test_*")
	if err != nil {
		t.Fatalf("erro criando tempDir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	restA, restB, restC := "node-a:9080", "node-b:9081", "node-c:9082"

	peersA := map[string]string{"SERVER_B": "http://" + restB, "SERVER_C": "http://" + restC}
	peersB := map[string]string{"SERVER_A": "http://" + restA, "SERVER_C": "http://" + restC}
	peersC := map[string]string{"SERVER_A": "http://" + restA, "SERVER_B": "http://" + restB}

	srvA, err := NewServer(":0", "SERVER_A", peersA, filepath.Join(tmpDir, "SERVER_A", "estado.json"))
	if err != nil {
		t.Fatalf("NewServer A: %v", err)
	}
	srvB, err := NewServer(":0", "SERVER_B", peersB, filepath.Join(tmpDir, "SERVER_B", "estado.json"))
	if err != nil {
		t.Fatalf("NewServer B: %v", err)
	}
	srvC, err := NewServer(":0", "SERVER_C", peersC, filepath.Join(tmpDir, "SERVER_C", "estado.json"))
	if err != nil {
		t.Fatalf("NewServer C: %v", err)
	}

	rt := &memoryRoundTripper{
		handlers: map[string]http.Handler{
			restA: srvA.internalHTTPHandler(),
			restB: srvB.internalHTTPHandler(),
			restC: srvC.internalHTTPHandler(),
		},
		offline: make(map[string]bool),
	}
	oldTransport := http.DefaultTransport
	http.DefaultTransport = rt
	defer func() { http.DefaultTransport = oldTransport }()

	date := "2026-12-20"

	// 1. Motorista em A oferta Salvador -> Feira (3 vagas)
	respOfferA := sendDirectRequest(t, srvA, protocol.ActionOfferRide, "driver_A", protocol.OfferRideData{
		Route:         []string{"Salvador", "Feira"},
		Date:          date,
		Time:          "08:00",
		TotalSeats:    3,
		SegmentPrices: []float64{30},
	})
	if respOfferA.Status != "SUCCESS" {
		t.Fatalf("esperava SUCCESS na oferta A, obteve: %+v", respOfferA)
	}
	var offerAData struct {
		RideID string `json:"ride_id"`
	}
	_ = json.Unmarshal([]byte(respOfferA.Data), &offerAData)

	// 2. Motorista em B oferta Feira -> Conquista (3 vagas)
	respOfferB := sendDirectRequest(t, srvB, protocol.ActionOfferRide, "driver_B", protocol.OfferRideData{
		Route:         []string{"Feira", "Conquista"},
		Date:          date,
		Time:          "10:00",
		TotalSeats:    3,
		SegmentPrices: []float64{40},
	})
	if respOfferB.Status != "SUCCESS" {
		t.Fatalf("esperava SUCCESS na oferta B, obteve: %+v", respOfferB)
	}
	var offerBData struct {
		RideID string `json:"ride_id"`
	}
	_ = json.Unmarshal([]byte(respOfferB.Data), &offerBData)

	// 3. Passageiro conectado ao Servidor A busca e reserva o itinerário combinado (A + B)
	respSearch := sendDirectRequest(t, srvA, protocol.ActionSearchItinerary, "pass_local_A", protocol.SearchItineraryData{
		Origin:      "Salvador",
		Destination: "Conquista",
		Date:        date,
	})
	var itins []domain.ItineraryResult
	_ = json.Unmarshal([]byte(respSearch.Data), &itins)
	if len(itins) != 1 {
		t.Fatalf("esperava 1 itinerário combinado, obteve %d", len(itins))
	}

	respBookA := sendDirectRequest(t, srvA, protocol.ActionBookItinerary, "pass_local_A", itins[0])
	if respBookA.Status != "SUCCESS" {
		t.Fatalf("falha ao reservar em A: %+v", respBookA)
	}

	// 4. Passageiro conectado ao Servidor C reserva o mesmo itinerário combinado (A + B)
	respBookC := sendDirectRequest(t, srvC, protocol.ActionBookItinerary, "pass_remote_C", itins[0])
	if respBookC.Status != "SUCCESS" {
		t.Fatalf("falha ao reservar em C: %+v", respBookC)
	}

	// 5. Passageiro conectado ao Servidor B reserva o mesmo itinerário combinado (A + B)
	respBookB := sendDirectRequest(t, srvB, protocol.ActionBookItinerary, "pass_remote_B", itins[0])
	if respBookB.Status != "SUCCESS" {
		t.Fatalf("falha ao reservar em B: %+v", respBookB)
	}

	// Verifica que as 3 vagas em B foram ocupadas (0 disponíveis)
	rideB, ok := srvB.repo.GetRide(offerBData.RideID)
	if !ok || rideB.Segments[0].AvailableSeats != 0 {
		t.Fatalf("esperava 0 vagas disponíveis em B antes do cancelamento da carona A, obteve %d", rideB.Segments[0].AvailableSeats)
	}

	// 6. Simula queda temporária de SERVER_C no momento em que o motorista em A cancela a carona
	rt.mu.Lock()
	rt.offline[restC] = true
	rt.mu.Unlock()

	respCancelRide := sendDirectRequest(t, srvA, protocol.ActionCancelRide, "driver_A", protocol.CancelRideData{
		RideID: offerAData.RideID,
	})
	if respCancelRide.Status != "SUCCESS" {
		t.Fatalf("falha ao cancelar carona em A: %+v", respCancelRide)
	}

	// Reservas em A e B já devem estar CANCELLED_BY_DRIVER imediatamente e suas 2 vagas devolvidas em B
	checkStatus := func(srv *Server, passenger, expected string) {
		t.Helper()
		resp := sendDirectRequest(t, srv, protocol.ActionListBookings, passenger, nil)
		var bookings []domain.Booking
		_ = json.Unmarshal([]byte(resp.Data), &bookings)
		if len(bookings) != 1 {
			t.Fatalf("esperava 1 reserva para %s, obteve %d", passenger, len(bookings))
		}
		if bookings[0].Status != expected {
			t.Fatalf("reserva de %s deveria estar %s, mas está %s", passenger, expected, bookings[0].Status)
		}
	}

	checkStatus(srvA, "pass_local_A", "CANCELLED_BY_DRIVER")
	checkStatus(srvB, "pass_remote_B", "CANCELLED_BY_DRIVER")
	if rideB.Segments[0].AvailableSeats != 2 {
		t.Fatalf("esperava 2 vagas devolvidas em B enquanto C está offline, obteve %d", rideB.Segments[0].AvailableSeats)
	}

	// 7. SERVER_C volta a ficar online e a recuperação de A reenvia a notificação pendente
	rt.mu.Lock()
	rt.offline[restC] = false
	rt.mu.Unlock()

	srvA.recoverPendingRideCancels()

	// Agora a reserva em C também deve estar CANCELLED_BY_DRIVER e a 3ª vaga em B devolvida!
	checkStatus(srvC, "pass_remote_C", "CANCELLED_BY_DRIVER")
	if rideB.Segments[0].AvailableSeats != 3 {
		t.Fatalf("esperava todas as 3 vagas devolvidas em B após recuperação de C, obteve %d", rideB.Segments[0].AvailableSeats)
	}
}
