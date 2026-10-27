package domain

import (
    "fmt"
    "sync"
    "time"
)

// Booking representa o registro de uma reserva feita por um passageiro no servidor.
type Booking struct {
    ID             string          `json:"id"`
    PassengerToken string          `json:"passenger_token"`
    Itinerary      ItineraryResult `json:"itinerary"`
    Date           string          `json:"date"`
    Status         string          `json:"status"`
}

// RideRepository é o repositório em memória do servidor local.
type RideRepository struct {
    mu           sync.RWMutex
    nodeID       string
    rides        map[string]*Ride
    bookings     map[string]*Booking
    rideCount    int
    bookingCount int
}

// NewRideRepository instancia e inicializa um novo repositório limpo em memória.
func NewRideRepository(nodeID string) *RideRepository {
    return &RideRepository{
        nodeID:   nodeID,
        rides:    make(map[string]*Ride),
        bookings: make(map[string]*Booking),
    }
}

// Add insere uma nova carona no repositório local e gera seu ID sequencial.
func (r *RideRepository) Add(ride *Ride) string {
    r.mu.Lock()
    defer r.mu.Unlock()

    r.rideCount++
    ride.ID = fmt.Sprintf("RIDE_%s_%04d", r.nodeID, r.rideCount)
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

// MarkBookingsCancelledByRide marca como cancelada qualquer reserva guardada neste nó que contenha a carona informada.
func (r *RideRepository) MarkBookingsCancelledByRide(rideID string) int {
    r.mu.Lock()
    defer r.mu.Unlock()

    count := 0
    for _, b := range r.bookings {
        for _, seg := range b.Itinerary.Segments {
            if seg.RideID == rideID && b.Status != "CANCELLED_BY_DRIVER" {
                b.Status = "CANCELLED_BY_DRIVER"
                count++
                break
            }
        }
    }
    return count
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