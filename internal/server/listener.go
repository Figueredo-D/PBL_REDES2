package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"

	"github.com/Figueredo-D/PBL_REDES/internal/concurrency"
	"github.com/Figueredo-D/PBL_REDES/internal/domain"
	"github.com/Figueredo-D/PBL_REDES/internal/protocol"
)

type Server struct {
	address    string
	id         string // Identificador único deste servidor no consórcio USANDO!!
	listener   net.Listener
	repo       *domain.RideRepository
	reqManager *concurrency.RequestManager // Gestor de Fila de Prioridade com Relógio de Lamport
	mu         sync.Mutex
	active     bool
}

func NewServer(address string, serverID string) *Server {
	clock := concurrency.NewLogicalClock(serverID)
	return &Server{ 
		address:    address,
		id:         serverID, // Identificador único do servidor no consórcio
		repo:       domain.NewRideRepository(),
		reqManager: concurrency.NewRequestManager(clock),
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
				Code:    "MALFORMED_ENVELOPE", // Envelope JSON malformado
				Message: "Envelope JSON malformado.",
			})
			continue
		}

		// Envolve a execução do dispatch dentro do Single Worker Manager ordenado por Lamport
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

		// Adicionado o 's.id' como companyID exigido pelo novo domínio
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

		filter := domain.SearchFilter{
			Origin:      data.Origin,
			Destination: data.Destination,
			Date:        data.Date,
		}

		itineraries := s.repo.SearchItineraries(filter)
		jsonResult, _ := json.Marshal(itineraries)

		return protocol.Response{
			Status:  "SUCCESS",
			Message: fmt.Sprintf("Encontradas %d opção(ões).", len(itineraries)),
			Data:    string(jsonResult),
		}

	case protocol.ActionBookItinerary:
		var itin domain.ItineraryResult
		if err := json.Unmarshal([]byte(req.Data), &itin); err != nil {
			return protocol.Response{Status: "ERROR", Code: "INVALID_DATA", Message: "Payload de reserva inválido."}
		}

		// Utiliza o método local ajustado para salvar a reserva e confirmar o bilhete
		bookingID := s.repo.SaveBooking(req.Token, itin, "CONFIRMED")

		fmt.Printf("[SERVER %s] Reserva %s realizada por %s.\n", s.id, bookingID, req.Token)
		return protocol.Response{
			Status:  "SUCCESS",
			Message: fmt.Sprintf("Reserva confirmada sob o ID %s.", bookingID),
			Data:    fmt.Sprintf(`{"booking_id":"%s"}`, bookingID),
		}

	case protocol.ActionCancelBooking:
		var data protocol.CancelBookingData
		if err := json.Unmarshal([]byte(req.Data), &data); err != nil {
			return protocol.Response{Status: "ERROR", Code: "INVALID_DATA", Message: "Payload de cancelamento inválido."}
		}

		// Nota: O cancelamento de reservas individuais pode ser estendido no 2PC posteriormente.
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