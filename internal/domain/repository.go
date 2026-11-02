package domain

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// ReleaseKey garante que a chave de idempotência de devolução de vagas inclua o ID do nó dono da reserva,
// evitando colisão caso reservas antigas de servidores distintos tenham o mesmo sufixo (ex: BOOK_0001).
func ReleaseKey(nodeID, bookingID string) string {
	if nodeID == "" || strings.HasPrefix(bookingID, nodeID+"_") {
		return bookingID
	}
	return nodeID + "_" + bookingID
}

// Booking representa o registro de uma reserva feita por um passageiro no servidor.
// PendingReleases lista as empresas que ainda precisam devolver as vagas de uma reserva cancelada.
type Booking struct {
	ID              string          `json:"id"`
	PassengerToken  string          `json:"passenger_token"`
	Itinerary       ItineraryResult `json:"itinerary"`
	Date            string          `json:"date"`
	Status          string          `json:"status"`
	PendingReleases []string        `json:"pending_releases,omitempty"`
}

// RideRepository é o repositório em memória do servidor local.
type RideRepository struct {
	mu                 sync.RWMutex
	nodeID             string
	rides              map[string]*Ride
	bookings           map[string]*Booking
	released           map[string]bool     // IDs de reservas cujas vagas locais já foram devolvidas (idempotência)
	pendingRideCancels map[string][]string // rideID -> lista de peers que ainda precisam ser avisados do cancelamento
	rideCount          int
	bookingCount       int
}

// NewRideRepository instancia e inicializa um novo repositório limpo em memória.
func NewRideRepository(nodeID string) *RideRepository {
	return &RideRepository{
		nodeID:             nodeID,
		rides:              make(map[string]*Ride),
		bookings:           make(map[string]*Booking),
		released:           make(map[string]bool),
		pendingRideCancels: make(map[string][]string),
	}
}

// Add insere uma nova carona no repositório local e gera seu ID sequencial.
func (r *RideRepository) Add(ride *Ride) string {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.rideCount++
	ride.ID = fmt.Sprintf("%s_RIDE_%04d", r.nodeID, r.rideCount)
	r.rides[ride.ID] = ride
	return ride.ID
}

// AddWithID insere uma carona mantendo o seu ID original.
func (r *RideRepository) AddWithID(ride *Ride) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rides[ride.ID] = ride
}

// GetRide busca uma carona local pelo seu ID.
func (r *RideRepository) GetRide(rideID string) (*Ride, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	ride, exists := r.rides[rideID]
	return ride, exists
}

// ListByDriver retorna todas as caronas criadas por um motorista específico.
func (r *RideRepository) ListByDriver(driverToken string) []*Ride {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []*Ride
	for _, ride := range r.rides {
		if ride.DriverToken == driverToken {
			result = append(result, ride)
		}
	}
	return result
}

// CancelRide remove a carona do repositório local após validar o dono.
func (r *RideRepository) CancelRide(rideID, driverToken string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	ride, exists := r.rides[rideID]
	if !exists {
		return fmt.Errorf("carona não encontrada")
	}

	if ride.DriverToken != driverToken {
		return fmt.Errorf("você não tem permissão para cancelar esta carona")
	}

	delete(r.rides, rideID)
	return nil
}

// CancelBookingsByRide marca como CANCELLED_BY_DRIVER qualquer reserva guardada neste nó que contenha
// a carona informada e agenda a devolução das vagas nos demais trechos do itinerário.
// Devolve cópias das reservas que estavam CONFIRMED e foram canceladas para disparo imediato das devoluções.
func (r *RideRepository) CancelBookingsByRide(rideID string) []Booking {
	r.mu.Lock()
	defer r.mu.Unlock()

	var affected []Booking
	for _, b := range r.bookings {
		hasRide := false
		for _, seg := range b.Itinerary.Segments {
			if seg.RideID == rideID {
				hasRide = true
				break
			}
		}
		if !hasRide {
			continue
		}

		if b.Status == "CONFIRMED" {
			seen := make(map[string]bool)
			var companies []string
			for _, seg := range b.Itinerary.Segments {
				if seg.RideID == rideID {
					continue
				}
				if seg.CompanyID != "" && !seen[seg.CompanyID] {
					seen[seg.CompanyID] = true
					companies = append(companies, seg.CompanyID)
				}
			}
			b.Status = "CANCELLED_BY_DRIVER"
			b.PendingReleases = companies
			affected = append(affected, *b)
		} else if b.Status != "CANCELLED_BY_DRIVER" && b.Status != "CANCELLED" {
			b.Status = "CANCELLED_BY_DRIVER"
			affected = append(affected, *b)
		}
	}
	return affected
}

// AddPendingRideCancel registra quais nós parceiros precisam ser notificados do cancelamento de uma carona local.
func (r *RideRepository) AddPendingRideCancel(rideID string, peers []string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(peers) == 0 {
		return
	}
	copia := make([]string, len(peers))
	copy(copia, peers)
	r.pendingRideCancels[rideID] = copia
}

// CompleteRideCancelNotify remove um parceiro da lista de notificações pendentes de cancelamento de carona.
func (r *RideRepository) CompleteRideCancelNotify(rideID, peerID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	peers, exists := r.pendingRideCancels[rideID]
	if !exists {
		return
	}
	var restantes []string
	for _, p := range peers {
		if p != peerID {
			restantes = append(restantes, p)
		}
	}
	if len(restantes) == 0 {
		delete(r.pendingRideCancels, rideID)
	} else {
		r.pendingRideCancels[rideID] = restantes
	}
}

// PendingRideCancels retorna uma cópia do mapa de caronas canceladas cujas notificações aos peers ainda estão pendentes.
func (r *RideRepository) PendingRideCancels() map[string][]string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make(map[string][]string, len(r.pendingRideCancels))
	for rideID, peers := range r.pendingRideCancels {
		if len(peers) > 0 {
			c := make([]string, len(peers))
			copy(c, peers)
			out[rideID] = c
		}
	}
	return out
}

// ListByDate busca todas as caronas registradas para uma determinada data.
func (r *RideRepository) ListByDate(date string) []*Ride {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []*Ride
	for _, ride := range r.rides {
		if ride.Date == date {
			result = append(result, ride)
		}
	}
	return result
}

// SearchItineraries executa a busca de rotas filtrando caronas na data desejada.
func (r *RideRepository) SearchItineraries(filter SearchFilter) []ItineraryResult {
	ridesOnDate := r.ListByDate(filter.Date)
	return FindItineraries(ridesOnDate, filter)
}

// TryReserveLocalSegment é usado na Fase 1 (Prepare) do 2PC.
func (r *RideRepository) TryReserveLocalSegment(rideID, from, to string) error {
	r.mu.RLock()
	ride, exists := r.rides[rideID]
	r.mu.RUnlock()

	if !exists {
		return fmt.Errorf("carona %s não encontrada neste servidor", rideID)
	}

	ride.Lock()
	defer ride.Unlock()

	for _, s := range ride.Segments {
		if s.From == from && s.To == to {
			if s.AvailableSeats <= 0 {
				return fmt.Errorf("sem vagas no trecho %s -> %s", from, to)
			}
			s.AvailableSeats--
			return nil
		}
	}

	return fmt.Errorf("trecho %s -> %s não pertence à carona %s", from, to, rideID)
}

// ReleaseLocalSegment é usado na Fase 2 do 2PC em caso de Abort.
func (r *RideRepository) ReleaseLocalSegment(rideID, from, to string) error {
	r.mu.RLock()
	ride, exists := r.rides[rideID]
	r.mu.RUnlock()

	if !exists {
		return fmt.Errorf("carona %s não encontrada", rideID)
	}

	ride.Lock()
	defer ride.Unlock()

	for _, s := range ride.Segments {
		if s.From == from && s.To == to {
			s.AvailableSeats++
			return nil
		}
	}

	return nil
}

// SaveBooking cria e salva o comprovante de reserva no banco em memória local do servidor.
func (r *RideRepository) SaveBooking(passengerToken string, itinerary ItineraryResult, status string) string {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.bookingCount++
	bookingID := fmt.Sprintf("%s_BOOK_%04d", r.nodeID, r.bookingCount)
	booking := &Booking{
		ID:             bookingID,
		PassengerToken: passengerToken,
		Itinerary:      itinerary,
		Date:           time.Now().Format("2006-01-02"),
		Status:         status,
	}

	r.bookings[bookingID] = booking
	return bookingID
}

// ListBookingsByPassenger recupera o histórico de bilhetes comprados por um passageiro.
func (r *RideRepository) ListBookingsByPassenger(passengerToken string) []*Booking {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []*Booking
	for _, b := range r.bookings {
		if b.PassengerToken == passengerToken {
			result = append(result, b)
		}
	}
	return result
}

// BeginCancelBooking marca a reserva do passageiro como CANCELLED e registra quais empresas ainda
// precisam devolver as vagas. Só reservas CONFIRMED podem ser canceladas, e só pelo dono.
// Devolve uma cópia da reserva para o chamador avisar os outros nós sem segurar a trava.
func (r *RideRepository) BeginCancelBooking(bookingID, passengerToken string) (Booking, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	b, exists := r.bookings[bookingID]
	if !exists || b.PassengerToken != passengerToken {
		return Booking{}, fmt.Errorf("reserva não encontrada")
	}

	switch b.Status {
	case "CONFIRMED":
		// segue o cancelamento
	case "CANCELLED":
		return Booking{}, fmt.Errorf("a reserva já foi cancelada")
	case "CANCELLED_BY_DRIVER":
		return Booking{}, fmt.Errorf("a reserva já foi cancelada pelo motorista")
	default:
		return Booking{}, fmt.Errorf("a reserva não pode ser cancelada no status %s", b.Status)
	}

	seen := make(map[string]bool)
	var companies []string
	for _, seg := range b.Itinerary.Segments {
		if !seen[seg.CompanyID] {
			seen[seg.CompanyID] = true
			companies = append(companies, seg.CompanyID)
		}
	}

	b.Status = "CANCELLED"
	b.PendingReleases = companies
	return *b, nil
}

// CompleteRelease registra que a empresa informada já devolveu as vagas da reserva cancelada.
func (r *RideRepository) CompleteRelease(bookingID, companyID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	b, exists := r.bookings[bookingID]
	if !exists {
		return
	}

	var restantes []string
	for _, c := range b.PendingReleases {
		if c != companyID {
			restantes = append(restantes, c)
		}
	}
	b.PendingReleases = restantes
}

// BookingsWithPendingReleases devolve cópias das reservas canceladas que ainda aguardam devolução de vagas
// em alguma empresa. Usado pela recuperação para tentar de novo.
func (r *RideRepository) BookingsWithPendingReleases() []Booking {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []Booking
	for _, b := range r.bookings {
		if len(b.PendingReleases) > 0 {
			result = append(result, *b)
		}
	}
	return result
}

// ApplyRelease devolve as vagas locais dos trechos informados, uma única vez por reserva.
// Chamadas repetidas com o mesmo bookingID não devolvem de novo. Trechos de caronas que não
// existem neste nó (de outras empresas ou já canceladas pelo motorista) são ignorados.
// Retorna a quantidade de trechos devolvidos.
func (r *RideRepository) ApplyRelease(bookingID string, segs []SegmentRef) int {
	r.mu.Lock()
	if r.released[bookingID] {
		r.mu.Unlock()
		return 0
	}
	r.released[bookingID] = true
	r.mu.Unlock()

	n := 0
	for _, s := range segs {
		if _, isLocal := r.GetRide(s.RideID); !isLocal {
			continue
		}
		if err := r.ReleaseLocalSegment(s.RideID, s.From, s.To); err == nil {
			n++
		}
	}
	return n
}
