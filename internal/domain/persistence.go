package domain

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// estadoPersistido é o conteúdo gravado em disco por um nó. Cada servidor tem o seu próprio arquivo,
// por exemplo data/SERVER_A/estado.json, e nenhum nó lê ou escreve o arquivo de outro.
type estadoPersistido struct {
	NodeID       string              `json:"node_id"`
	RideCount    int                 `json:"ride_count"`
	BookingCount int                 `json:"booking_count"`
	Rides        map[string]*Ride    `json:"rides"`
	Bookings     map[string]*Booking `json:"bookings"`
}

// copiaRide faz uma cópia independente da carona, lida sob a trava de leitura dela.
// Assim a serialização não lê as vagas enquanto um prepare as altera.
func copiaRide(ride *Ride) *Ride {
	ride.RLock()
	defer ride.RUnlock()

	segs := make([]*Segment, 0, len(ride.Segments))
	for _, s := range ride.Segments {
		c := *s
		segs = append(segs, &c)
	}

	return &Ride{
		ID:          ride.ID,
		CompanyID:   ride.CompanyID,
		DriverToken: ride.DriverToken,
		Route:       ride.Route,
		Date:        ride.Date,
		Time:        ride.Time,
		TotalSeats:  ride.TotalSeats,
		Segments:    segs,
	}
}

// SaveToFile grava o estado atual do repositório em path de forma atômica.
// Se a queda acontecer no meio da escrita, o arquivo anterior continua válido.
func (r *RideRepository) SaveToFile(path string) error {
	r.mu.RLock()
	copias := make(map[string]*Ride, len(r.rides))
	for id, ride := range r.rides {
		copias[id] = copiaRide(ride)
	}
	estado := estadoPersistido{
		NodeID:       r.nodeID,
		RideCount:    r.rideCount,
		BookingCount: r.bookingCount,
		Rides:        copias,
		Bookings:     r.bookings,
	}
	dados, err := json.MarshalIndent(estado, "", "  ")
	r.mu.RUnlock()

	if err != nil {
		return fmt.Errorf("falha ao serializar o estado: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("falha ao criar diretório de dados: %w", err)
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, dados, 0o644); err != nil {
		return fmt.Errorf("falha ao gravar arquivo temporário: %w", err)
	}

	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("falha ao substituir o arquivo de estado: %w", err)
	}

	return nil
}

// LoadRideRepository cria o repositório do nó a partir do arquivo em path.
// Se o arquivo não existir (primeira execução), devolve um repositório vazio.
func LoadRideRepository(nodeID, path string) (*RideRepository, error) {
	repo := NewRideRepository(nodeID)

	dados, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return repo, nil
	}
	if err != nil {
		return nil, fmt.Errorf("falha ao ler o estado de %s: %w", path, err)
	}

	var estado estadoPersistido
	if err := json.Unmarshal(dados, &estado); err != nil {
		return nil, fmt.Errorf("estado de %s corrompido: %w", path, err)
	}

	if estado.NodeID != nodeID {
		return nil, fmt.Errorf("arquivo %s pertence ao nó %s, não a %s", path, estado.NodeID, nodeID)
	}

	repo.rideCount = estado.RideCount
	repo.bookingCount = estado.BookingCount
	if estado.Rides != nil {
		repo.rides = estado.Rides
	}
	if estado.Bookings != nil {
		repo.bookings = estado.Bookings
	}

	return repo, nil
}
