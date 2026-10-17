package domain

import (
	"fmt"
	"sync"
	"time"
)

// Booking representa o registro de uma reserva feita por um passageiro no servidor.
type Booking struct {
	ID             string          `json:"id"`              // ID único do bilhete/reserva (ex: BOOK_0001)
	PassengerToken string          `json:"passenger_token"` // Token do passageiro
	Itinerary      ItineraryResult `json:"itinerary"`       // Detalhes do itinerário reservado
	Date           string          `json:"date"`            // Data de emissão da reserva
	Status         string          `json:"status"`          // Estado da reserva: PENDING_PREPARE, CONFIRMED, CANCELLED
}

// RideRepository é o repositório em memória do servidor local.
// Ele gerencia o cadastro de caronas locais e o histórico de reservas efetuadas.
type RideRepository struct {
	mu           sync.RWMutex        // Trava compartilhada do mapa de caronas (usada apenas para inserção/busca no mapa)
	rides        map[string]*Ride    // Mapa de Caronas: ID -> *Ride
	bookings     map[string]*Booking // Mapa de Reservas: ID -> *Booking
	rideCount    int                 // Contador interno para geração de IDs de caronas
	bookingCount int                 // Contador interno para geração de IDs de reservas
}

// NewRideRepository instancia e inicializa um novo repositório limpo em memória.
func NewRideRepository() *RideRepository {
	return &RideRepository{
		rides:    make(map[string]*Ride),
		bookings: make(map[string]*Booking),
	}
}

// Add insere uma nova carona no repositório local e gera seu ID sequencial.
func (r *RideRepository) Add(ride *Ride) string {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.rideCount++
	ride.ID = fmt.Sprintf("RIDE_%04d", r.rideCount)
	r.rides[ride.ID] = ride
	return ride.ID
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

// CancelRide cancela a carona informada e marca como CANCELLED_BY_DRIVER as reservas associadas.
func (r *RideRepository) CancelRide(rideID, driverToken string) error {
	r.mu.Lock()
	ride, exists := r.rides[rideID]
	if !exists {
		r.mu.Unlock()
		return fmt.Errorf("carona não encontrada")
	}

	if ride.DriverToken != driverToken {
		r.mu.Unlock()
		return fmt.Errorf("você não tem permissão para cancelar esta carona")
	}

	delete(r.rides, rideID)
	r.mu.Unlock()

	// Atualiza os status dos bilhetes de reservas sem prender a trava principal do repositório por muito tempo
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, b := range r.bookings {
		for _, seg := range b.Itinerary.Segments {
			if seg.RideID == rideID {
				b.Status = "CANCELLED_BY_DRIVER"
			}
		}
	}

	return nil
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

// --- MÉTODOS DE TRAVA FINA E TRANSAÇÕES DISTRIBUÍDAS (2PC) ---

// TryReserveLocalSegment é o método utilizado na Fase 1 (Prepare) do protocolo 2PC.
// Ele tenta bloquear/decrementar o assento de um trecho de forma isolada (trava fina no *Ride),
// sem travar o repositório todo ou alterar outras caronas não envolvidas.
func (r *RideRepository) TryReserveLocalSegment(rideID, from, to string) error {
	r.mu.RLock()
	ride, exists := r.rides[rideID]
	r.mu.RUnlock()

	if !exists {
		return fmt.Errorf("carona %s não encontrada neste servidor", rideID)
	}

	// Adquire trava EXCLUSIVA de escrita APENAS para esta carona específica
	ride.Lock()
	defer ride.Unlock()

	for _, s := range ride.Segments {
		if s.From == from && s.To == to {
			if s.AvailableSeats <= 0 {
				return fmt.Errorf("sem vagas no trecho %s -> %s", from, to)
			}
			// Decrementa a vaga (reserva garantida durante o 2PC)
			s.AvailableSeats--
			return nil
		}
	}

	return fmt.Errorf("trecho %s -> %s não pertence à carona %s", from, to, rideID)
}

// ReleaseLocalSegment é utilizado na Fase 2 do 2PC em caso de Abort/Rollback.
// Se um dos servidores do consórcio falhar na reserva, este método restaura/devolve a vaga.
func (r *RideRepository) ReleaseLocalSegment(rideID, from, to string) error {
	r.mu.RLock()
	ride, exists := r.rides[rideID]
	r.mu.RUnlock()

	if !exists {
		return fmt.Errorf("carona %s não encontrada", rideID)
	}

	// Adquire trava EXCLUSIVA apenas para esta carona específica
	ride.Lock()
	defer ride.Unlock()

	for _, s := range ride.Segments {
		if s.From == from && s.To == to {
			// Devolve a vaga decrementada anteriormente
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
	bookingID := fmt.Sprintf("BOOK_%04d", r.bookingCount)
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