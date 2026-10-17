package domain

// SearchFilter representa os parâmetros informados pelo passageiro na busca de itinerários.
type SearchFilter struct {
	Origin      string `json:"origin"`
	Destination string `json:"destination"` 
	Date        string `json:"date"`
}

// ItinerarySegmentDetail descreve detalhadamente um trecho do itinerário gerado na busca.
type ItinerarySegmentDetail struct {
	RideID         string  `json:"ride_id"`        // ID da carona correspondente
	CompanyID      string  `json:"company_id"`     // Empresa responsável por este trecho (utilizado no 2PC)
	DriverToken    string  `json:"driver_token"`   // Token do motorista deste trecho
	From           string  `json:"from"`           // Origem do trecho
	To             string  `json:"to"`             // Destino do trecho
	DepartureTime  string  `json:"departure_time"` // Horário de partida do trecho
	Price          float64 `json:"price"`          // Valor deste trecho
	AvailableSeats int     `json:"available_seats"`// Vagas restantes neste trecho
}

// ItineraryResult representa uma rota completa (direta ou combinada) encontrada pelo algoritmo de busca.
type ItineraryResult struct {
	ItineraryID    string                   `json:"itinerary_id"`    // Identificador do itinerário
	Segments       []ItinerarySegmentDetail `json:"segments"`        // Trechos que compõem o caminho
	TotalPrice     float64                  `json:"total_price"`     // Soma do preço de todos os trechos
	DepartureTime  string                   `json:"departure_time"`  // Horário de partida no primeiro trecho
	AvailableSeats int                      `json:"available_seats"` // Vagas gargalo (a menor quantidade de vagas entre os trechos)
	Drivers        []string                 `json:"drivers"`         // Lista de tokens dos motoristas envolvidos
}

// GraphEdge representa uma aresta no Grafo montado temporariamente para a busca em largura (BFS).
type GraphEdge struct {
	Ride        *Ride
	Segment     *Segment
	From        string
	To          string
	DriverToken string
	CompanyID   string
}

// PathNode é a estrutura utilizada na fila da BFS para acompanhar os caminhos percorridos.
type PathNode struct {
	City  string      // Cidade atual onde o nó se encontra
	Edges []GraphEdge // Lista de arestas/trechos percorridos até chegar a esta cidade
}

// FindItineraries utiliza a Busca em Largura (BFS) no Grafo em memória para encontrar
// todas as combinações de trajetos possíveis (diretos ou com conexão) que atendam ao filtro.
func FindItineraries(rides []*Ride, filter SearchFilter) []ItineraryResult {
	var results []ItineraryResult

	// Cria o adjacency list do Grafo mapeando: CidadeOrigem -> []ArestasDisponíveis
	graph := make(map[string][]GraphEdge)

	for _, ride := range rides {
		// Ignora caronas de datas diferentes da buscada
		if ride.Date != filter.Date {
			continue
		}

		// Leitura concorrencial segura usando a trava fina de leitura da carona
		ride.RLock()
		for _, seg := range ride.Segments {
			if seg.AvailableSeats > 0 {
				graph[seg.From] = append(graph[seg.From], GraphEdge{
					Ride:        ride,
					Segment:     seg,
					From:        seg.From,
					To:          seg.To,
					DriverToken: ride.DriverToken,
					CompanyID:   ride.CompanyID,
				})
			}
		}
		ride.RUnlock()
	}

	// Fila da BFS inicializada com a cidade de origem solicitada
	queue := []PathNode{{City: filter.Origin, Edges: []GraphEdge{}}}

	for len(queue) > 0 {
		// Desenfileira o primeiro caminho da fila
		current := queue[0]
		queue = queue[1:]

		// Se chegamos ao destino desejado e percorremos ao menos 1 trecho, montamos o resultado
		if current.City == filter.Destination && len(current.Edges) > 0 {
			res := buildItineraryResult(current.Edges)
			results = append(results, res)
			continue
		}

		// Trava de segurança: limita conexões a no máximo 4 trechos para evitar caminhos gigantes ou infinitos
		if len(current.Edges) >= 4 {
			continue
		}

		// Explora as conexões a partir da cidade atual
		for _, edge := range graph[current.City] {
			// Evita ciclos (passar pela mesma cidade duas vezes na mesma viagem)
			if hasVisitedCity(current.Edges, edge.To) {
				continue
			}

			// Validação temporal das conexões: 
			// A segunda carona não pode partir antes da primeira carona
			if len(current.Edges) > 0 {
				lastEdge := current.Edges[len(current.Edges)-1]
				if edge.Ride.ID != lastEdge.Ride.ID && edge.Ride.Time < lastEdge.Ride.Time {
					continue
				}
			}

			// Copia os trechos anteriores e adiciona o novo trecho
			newEdges := append([]GraphEdge{}, current.Edges...)
			newEdges = append(newEdges, edge)

			// Enfileira o novo nó expandido para continuar a BFS
			queue = append(queue, PathNode{
				City:  edge.To,
				Edges: newEdges,
			})
		}
	}

	return results
}

// hasVisitedCity verifica se uma cidade já faz parte dos trechos percorridos (prevenção de ciclos).
func hasVisitedCity(edges []GraphEdge, city string) bool {
	for _, e := range edges {
		if e.From == city || e.To == city {
			return true
		}
	}
	return false
}

// buildItineraryResult consolida as arestas percorridas em uma estrutura ItineraryResult pronta.
func buildItineraryResult(edges []GraphEdge) ItineraryResult {
	var segments []ItinerarySegmentDetail
	var totalPrice float64
	var drivers []string
	driverSet := make(map[string]bool)

	minSeats := edges[0].Segment.AvailableSeats
	firstDeparture := edges[0].Ride.Time

	for _, e := range edges {
		totalPrice += e.Segment.Price

		// Encontra a menor quantidade de assentos disponíveis no trajeto (gargalo do itinerário)
		if e.Segment.AvailableSeats < minSeats {
			minSeats = e.Segment.AvailableSeats
		}

		// Adiciona o motorista sem duplicatas
		if !driverSet[e.DriverToken] {
			driverSet[e.DriverToken] = true
			drivers = append(drivers, e.DriverToken)
		}

		segments = append(segments, ItinerarySegmentDetail{
			RideID:         e.Ride.ID,
			CompanyID:      e.CompanyID,
			DriverToken:    e.DriverToken,
			From:           e.Segment.From,
			To:             e.Segment.To,
			DepartureTime:  e.Ride.Time,
			Price:          e.Segment.Price,
			AvailableSeats: e.Segment.AvailableSeats,
		})
	}

	return ItineraryResult{
		ItineraryID:    edges[0].Ride.ID,
		Segments:       segments,
		TotalPrice:     totalPrice,
		DepartureTime:  firstDeparture,
		AvailableSeats: minSeats,
		Drivers:        drivers,
	}
}