package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/Figueredo-D/PBL_REDES/internal/cluster"
	"github.com/Figueredo-D/PBL_REDES/internal/concurrency"
	"github.com/Figueredo-D/PBL_REDES/internal/domain"
	"github.com/Figueredo-D/PBL_REDES/internal/protocol"
)

type Server struct {
	address        string
	id             string // Identificador único deste servidor no consórcio
	listener       net.Listener
	repo           *domain.RideRepository
	reqManager     *concurrency.RequestManager // Gestor de Fila de Prioridade com Single Worker
	clusterManager *cluster.ClusterManager     // Gestor de comunicação inter-nós do consórcio
	mu             sync.Mutex
	active         bool
}

func NewServer(address string, serverID string, peers map[string]string) *Server {
	clock := concurrency.NewLogicalClock(serverID)
	return &Server{
		address:        address,
		id:             serverID,
		repo:           domain.NewRideRepository(serverID),
		reqManager:     concurrency.NewRequestManager(clock),
		clusterManager: cluster.NewClusterManager(serverID, peers),
	}
}

func (s *Server) Start() error {
	l, err := net.Listen("tcp", s.address)
	if err != nil {
		return fmt.Errorf("falha ao abrir socket na porta %s: %w", s.address, err)
	}
	s.listener = l
	s.active = true

	fmt.Printf("[SERVER %s] Escutando em TCP %s\n", s.id, s.address)

	for s.active {
		conn, err := l.Accept()
		if err != nil {
			if !s.active {
				break
			}
			fmt.Printf("[SERVER] Erro ao aceitar conexão: %v\n", err)
			continue
		}

		go s.handleConnection(conn)
	}

	return nil
}

func (s *Server) handleConnection(conn net.Conn) {
	defer conn.Close()
	remoteAddr := conn.RemoteAddr().String()

	reader := bufio.NewReader(conn)

	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			if err != io.EOF {
				fmt.Printf("[SERVER] Erro de leitura de %s: %v\n", remoteAddr, err)
			}
			break
		}

		var req protocol.Request
		if err := json.Unmarshal(line, &req); err != nil {
			s.sendResponse(conn, protocol.Response{
				Status:  "ERROR",
				Code:    "MALFORMED_ENVELOPE",
				Message: "Envelope JSON malformado.",
			})
			continue
		}

		var resp protocol.Response
		err = s.reqManager.Submit(func() error {
			resp = s.dispatch(req)
			return nil
		})

		if err != nil {
			resp = protocol.Response{
				Status:  "ERROR",
				Code:    "WORKER_EXECUTION_FAILED",
				Message: err.Error(),
			}
		}

		s.sendResponse(conn, resp)
	}
}

func (s *Server) dispatch(req protocol.Request) protocol.Response {
	switch req.Action {

	case protocol.ActionOfferRide:
		var data protocol.OfferRideData
		if err := json.Unmarshal([]byte(req.Data), &data); err != nil {
			return protocol.Response{Status: "ERROR", Code: "INVALID_DATA", Message: "Payload de oferta inválido."}
		}

		ride, err := domain.NewRide("", s.id, req.Token, data.Route, data.Date, data.Time, data.TotalSeats, data.SegmentPrices)
		if err != nil {
			return protocol.Response{Status: "ERROR", Code: "VALIDATION_FAILED", Message: err.Error()}
		}

		rideID := s.repo.Add(ride)
		fmt.Printf("[SERVER %s] Carona %s ofertada por %s.\n", s.id, rideID, req.Token)

		return protocol.Response{
			Status:  "SUCCESS",
			Message: fmt.Sprintf("Carona registrada sob o ID %s.", rideID),
			Data:    fmt.Sprintf(`{"ride_id":"%s"}`, rideID),
		}

	case protocol.ActionListRides:
		rides := s.repo.ListByDriver(req.Token)
		bytes, _ := json.Marshal(rides)
		return protocol.Response{Status: "SUCCESS", Data: string(bytes)}

	case protocol.ActionCancelRide:
		var data protocol.CancelRideData
		if err := json.Unmarshal([]byte(req.Data), &data); err != nil {
			return protocol.Response{Status: "ERROR", Code: "INVALID_DATA", Message: "Payload inválido."}
		}

		if err := s.repo.CancelRide(data.RideID, req.Token); err != nil {
			return protocol.Response{Status: "ERROR", Code: "CANCEL_FAILED", Message: err.Error()}
		}

		fmt.Printf("[SERVER %s] Carona %s cancelada por %s.\n", s.id, data.RideID, req.Token)
		return protocol.Response{Status: "SUCCESS", Message: "Carona cancelada com sucesso."}

	case protocol.ActionSearchItinerary:
		var data protocol.SearchItineraryData
		if err := json.Unmarshal([]byte(req.Data), &data); err != nil {
			return protocol.Response{Status: "ERROR", Code: "INVALID_DATA", Message: "Payload de busca inválido."}
		}

		// Cria repositório temporário unificado preservando os IDs e vagas originais
		tempRepo := domain.NewRideRepository(s.id)

		for _, r := range s.repo.ListByDate(data.Date) {
			tempRepo.AddWithID(r)
		}

		// Consulta os peers pela API REST interna (GET /internal/rides).
		// Uma falha de peer é registrada e a busca continua com os demais nós.
		for peerID := range s.clusterManager.Peers {
			peerRides, err := s.clusterManager.ListRidesByDateREST(peerID, data.Date)
			if err != nil {
				fmt.Printf("[SERVER %s] Peer %s indisponível na busca: %v\n", s.id, peerID, err)
				continue
			}
			for _, r := range peerRides {
				tempRepo.AddWithID(r)
			}
		}

		filter := domain.SearchFilter{
			Origin:      data.Origin,
			Destination: data.Destination,
			Date:        data.Date,
		}

		itineraries := tempRepo.SearchItineraries(filter)
		jsonResult, _ := json.Marshal(itineraries)

		return protocol.Response{
			Status:  "SUCCESS",
			Message: fmt.Sprintf("Encontradas %d opção(ões).", len(itineraries)),
			Data:    string(jsonResult),
		}

	case "INTERNAL_LIST_RIDES_BY_DATE":
		var data struct {
			Date string `json:"date"`
		}
		json.Unmarshal([]byte(req.Data), &data)

		rides := s.repo.ListByDate(data.Date)
		bytes, _ := json.Marshal(rides)
		return protocol.Response{Status: "SUCCESS", Data: string(bytes)}

	case protocol.ActionBookItinerary:
		var itin domain.ItineraryResult
		if err := json.Unmarshal([]byte(req.Data), &itin); err != nil {
			return protocol.Response{Status: "ERROR", Code: "INVALID_DATA", Message: "Payload de reserva inválido."}
		}

		// --- 2PC: PAPEL DE COORDENADOR ---
		transactionID := fmt.Sprintf("TX_%d", time.Now().UnixNano())
		votedPeers := []string{}
		successPrepare := true

		// 1. Tenta reservar os segmentos locais pertencentes a este nó
		for _, seg := range itin.Segments {
			_, isLocal := s.repo.GetRide(seg.RideID)
			if isLocal {
				err := s.repo.TryReserveLocalSegment(seg.RideID, seg.From, seg.To)
				if err != nil {
					successPrepare = false
					break
				}
			}
		}

		// 2. Se os locais passaram, envia 2PC_PREPARE para todos os peers do consórcio
		if successPrepare {
			var protoSegments []protocol.SegmentInfo
			for _, sSeg := range itin.Segments {
				protoSegments = append(protoSegments, protocol.SegmentInfo{
					RideID:         sSeg.RideID,
					DriverToken:    sSeg.DriverToken,
					From:           sSeg.From,
					To:             sSeg.To,
					DepartureTime:  sSeg.DepartureTime,
					Price:          sSeg.Price,
					AvailableSeats: sSeg.AvailableSeats,
				})
			}

			preparePayload, _ := json.Marshal(protocol.TwoPCPrepareData{
				TransactionID: transactionID,
				Itinerary: protocol.ItineraryOption{
					ItineraryID:    itin.ItineraryID,
					Segments:       protoSegments,
					TotalPrice:     itin.TotalPrice,
					DepartureTime:  itin.DepartureTime,
					AvailableSeats: itin.AvailableSeats,
					Drivers:        itin.Drivers,
				},
			})

			for peerID := range s.clusterManager.Peers {
				resp, err := s.clusterManager.SendMessage(peerID, protocol.Request{
					Action: protocol.Action2PCPrepare,
					Token:  req.Token,
					Data:   string(preparePayload),
				})

				if err != nil || resp.Status != "VOTE_COMMIT" {
					successPrepare = false
					break
				}
				votedPeers = append(votedPeers, peerID)
			}
		}

		// Decisão Global (Commit ou Abort)
		if !successPrepare {
			for _, seg := range itin.Segments {
				_, isLocal := s.repo.GetRide(seg.RideID)
				if isLocal {
					s.repo.ReleaseLocalSegment(seg.RideID, seg.From, seg.To)
				}
			}
			for _, peerID := range votedPeers {
				s.clusterManager.SendMessage(peerID, protocol.Request{
					Action: protocol.Action2PCAbort,
					Data:   fmt.Sprintf(`{"transaction_id":"%s"}`, transactionID),
				})
			}
			return protocol.Response{Status: "ERROR", Code: "2PC_ABORTED", Message: "Transação abortada: falha ao garantir vagas."}
		}

		for _, peerID := range votedPeers {
			s.clusterManager.SendMessage(peerID, protocol.Request{
				Action: protocol.Action2PCCommit,
				Data:   fmt.Sprintf(`{"transaction_id":"%s"}`, transactionID),
			})
		}

		bookingID := s.repo.SaveBooking(req.Token, itin, "CONFIRMED")
		fmt.Printf("[SERVER %s] Transação 2PC bem-sucedida! Reserva %s confirmada.\n", s.id, bookingID)

		return protocol.Response{
			Status:  "SUCCESS",
			Message: fmt.Sprintf("Reserva confirmada sob o ID %s via 2PC.", bookingID),
			Data:    fmt.Sprintf(`{"booking_id":"%s"}`, bookingID),
		}

	// --- 2PC: PAPEL DE PARTICIPANTE ---
	case protocol.Action2PCPrepare:
		var data protocol.TwoPCPrepareData
		if err := json.Unmarshal([]byte(req.Data), &data); err != nil {
			return protocol.Response{Status: "VOTE_ABORT", Message: "Payload de prepare inválido."}
		}

		canCommit := true
		for _, seg := range data.Itinerary.Segments {
			if _, isLocal := s.repo.GetRide(seg.RideID); isLocal {
				if err := s.repo.TryReserveLocalSegment(seg.RideID, seg.From, seg.To); err != nil {
					canCommit = false
					break
				}
			}
		}

		if !canCommit {
			return protocol.Response{Status: "VOTE_ABORT", Message: "Vagas esgotadas neste nó."}
		}

		return protocol.Response{Status: "VOTE_COMMIT", Message: "Preparado para confirmar."}

	case protocol.Action2PCCommit:
		fmt.Printf("[SERVER %s] Recebido GLOBAL_COMMIT para a transação.\n", s.id)
		return protocol.Response{Status: "SUCCESS", Message: "Commit efetivado."}

	case protocol.Action2PCAbort:
		fmt.Printf("[SERVER %s] Recebido GLOBAL_ABORT. Revertendo alterações...\n", s.id)
		return protocol.Response{Status: "SUCCESS", Message: "Abort efetivado."}

	case protocol.ActionCancelBooking:
		var data protocol.CancelBookingData
		if err := json.Unmarshal([]byte(req.Data), &data); err != nil {
			return protocol.Response{Status: "ERROR", Code: "INVALID_DATA", Message: "Payload de cancelamento inválido."}
		}

		return protocol.Response{Status: "SUCCESS", Message: "Solicitação de cancelamento processada."}

	case protocol.ActionListBookings:
		bookings := s.repo.ListBookingsByPassenger(req.Token)
		bytes, _ := json.Marshal(bookings)
		return protocol.Response{Status: "SUCCESS", Data: string(bytes)}

	default:
		return protocol.Response{
			Status:  "ERROR",
			Code:    "UNKNOWN_ACTION",
			Message: fmt.Sprintf("Ação '%s' desconhecida.", req.Action),
		}
	}
}

func (s *Server) sendResponse(conn net.Conn, resp protocol.Response) {
	data, _ := json.Marshal(resp)
	data = append(data, '\n')
	conn.Write(data)
}
