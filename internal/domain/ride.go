package domain

import (
	"fmt"
	"sync"
)

// Segment representa um trecho individual de uma carona entre duas cidades adjacentes.
// Exemplo: Numa viagem Salvador -> Feira -> Conquista, teremos 2 Segments:
// 1) Salvador -> Feira
// 2) Feira -> Conquista
type Segment struct {
	From           string  `json:"from"`            // Cidade de origem do trecho
	To             string  `json:"to"`              // Cidade de destino do trecho
	Price          float64 `json:"price"`           // Valor cobrado por este trecho específico
	AvailableSeats int     `json:"available_seats"` // Número de vagas restantes neste trecho
}

// Ride representa a publicação completa de uma carona por um motorista.
type Ride struct {
	ID          string       `json:"id"`           // Identificador único da carona (ex: RIDE_0001)
	CompanyID   string       `json:"company_id"`   // ID da empresa/servidor do consórcio proprietário desta carona
	DriverToken string       `json:"driver_token"` // Token/Identificador do motorista que criou a carona
	Route       []string     `json:"route"`        // Lista ordenada de cidades da rota (ex: ["Salvador", "Feira", "Conquista"])
	Date        string       `json:"date"`         // Data da viagem (formato: YYYY-MM-DD)
	Time        string       `json:"time"`         // Horário de partida (formato: HH:MM)
	TotalSeats  int          `json:"total_seats"`  // Quantidade total inicial de assentos oferecidos
	Segments    []*Segment   `json:"segments"`     // Trechos individuais calculados a partir da rota
	mu          sync.RWMutex `json:"-"`            // Trava fina exclusiva por carona/rota (ignorada na serialização JSON)
}

// NewRide é o construtor responsável por instanciar e validar uma nova carona.
func NewRide(id, companyID, driverToken string, route []string, date, timeStr string, totalSeats int, prices []float64) (*Ride, error) {
	// Validação 1: A rota precisa ter pelo menos 2 cidades (Origem e Destino)
	if len(route) < 2 {
		return nil, fmt.Errorf("a rota precisa de pelo menos 2 cidades")
	}

	// Validação 2: A quantidade de preços fornecidos deve ser exatamente igual ao número de trechos (cidades - 1)
	if len(prices) != len(route)-1 {
		return nil, fmt.Errorf("quantidade de preços (%d) difere da quantidade de trechos (%d)", len(prices), len(route)-1)
	}

	// Validação 3: Assentos devem ser um número positivo
	if totalSeats <= 0 {
		return nil, fmt.Errorf("a quantidade de assentos deve ser maior que zero")
	}

	// Constrói a lista de trechos (segments) ligando a cidade i com a cidade i+1
	var segments []*Segment
	for i := 0; i < len(route)-1; i++ {
		segments = append(segments, &Segment{
			From:           route[i],
			To:             route[i+1],
			Price:          prices[i],
			AvailableSeats: totalSeats,
		})
	}

	return &Ride{
		ID:          id,
		CompanyID:   companyID,
		DriverToken: driverToken,
		Route:       route,
		Date:        date,
		Time:        timeStr,
		TotalSeats:  totalSeats,
		Segments:    segments,
	}, nil
}

// Lock adquire a trava de escrita exclusiva da carona.
// Deve ser chamada ao alterar assentos (reserva ou cancelamento).
func (r *Ride) Lock() { r.mu.Lock() }

// Unlock libera a trava de escrita exclusiva da carona.
func (r *Ride) Unlock() { r.mu.Unlock() }

// RLock adquire a trava de leitura compartilhada da carona.
// Permite que múltiplas goroutines leiam as vagas simultaneamente.
func (r *Ride) RLock() { r.mu.RLock() }

// RUnlock libera a trava de leitura compartilhada da carona.
func (r *Ride) RUnlock() { r.mu.RUnlock() }
