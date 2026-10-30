package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"sync"
	"time"

	"github.com/Figueredo-D/PBL_REDES/internal/cluster"
	"github.com/Figueredo-D/PBL_REDES/internal/concurrency"
	"github.com/Figueredo-D/PBL_REDES/internal/domain"
	"github.com/Figueredo-D/PBL_REDES/internal/protocol"
)

// txIDData é o corpo usado pelos endpoints de commit e abort do 2PC.
type txIDData struct {
	TransactionID string `json:"transaction_id"`
}

type Server struct {
	address        string
	id             string
	listener       net.Listener
	repo           *domain.RideRepository
	reqManager     *concurrency.RequestManager
	clusterManager *cluster.ClusterManager
	txLog          *domain.TxLog
	dataPath       string
	mu             sync.Mutex
	active         bool
}

// NewServer carrega o estado salvo deste nó e monta o servidor.
func NewServer(address string, serverID string, peers map[string]string, dataPath string) (*Server, error) {
	repo, err := domain.LoadRideRepository(serverID, dataPath)
	if err != nil {
		return nil, err
	}

	txLog, err := domain.LoadTxLog(filepath.Join(filepath.Dir(dataPath), "txlog.json"))
	if err != nil {
		return nil, err
	}

	clock := concurrency.NewLogicalClock(serverID)
	return &Server{
		address:        address,
		id:             serverID,
		repo:           repo,
		dataPath:       dataPath,
		reqManager:     concurrency.NewRequestManager(clock),
		clusterManager: cluster.NewClusterManager(serverID, peers),
		txLog:          txLog,
	}, nil
}

// saveState grava o estado atual do nó em disco.
func (s *Server) saveState() {
	if err := s.repo.SaveToFile(s.dataPath); err != nil {
		fmt.Printf("[SERVER %s] Aviso: falha ao salvar estado: %v\n", s.id, err)
	}
}

// recordDecision grava a decisão global antes de avisar os participantes.
func (s *Server) recordDecision(txID, decision string) {
	if err := s.txLog.RecordDecision(txID, decision); err != nil {
		fmt.Printf("[SERVER %s] Aviso: falha ao gravar decisão %s da transação %s: %v\n",
			s.id, decision, txID, err)
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

		s.sendResponse(conn, s.route(req))
	}
}

// route encaminha operações que fazem chamadas de rede para fora do Single Worker.
func (s *Server) route(req protocol.Request) protocol.Response {
	switch req.Action {
	case protocol.ActionSearchItinerary:
		return s.handleSearch(req)
	case protocol.ActionBookItinerary:
		return s.handleBook(req)
	case protocol.ActionCancelBooking:
		return s.handleCancelBooking(req)
	}

	var resp protocol.Response
	err := s.reqManager.Submit(func() error {
		resp = s.dispatch(req)
		return nil
	})
	if err != nil {
		return protocol.Response{
			Status:  "ERROR",
			Code:    "WORKER_EXECUTION_FAILED",
			Message: err.Error(),
		}
	}
	return resp
}

// handleSearch combina as caronas locais com as dos peers.
func (s *Server) handleSearch(req protocol.Request) protocol.Response {
	var data protocol.SearchItineraryData
	if err := json.Unmarshal([]byte(req.Data), &data); err != nil {
		return protocol.Response{Status: "ERROR", Code: "INVALID_DATA", Message: "Payload de busca inválido."}
	}

	var localRides []*domain.Ride
	if err := s.reqManager.Submit(func() error {
		localRides = s.repo.ListByDate(data.Date)
		return nil
	}); err != nil {
		return protocol.Response{Status: "ERROR", Code: "WORKER_EXECUTION_FAILED", Message: err.Error()}
	}

	tempRepo := domain.NewRideRepository(s.id)
	for _, ride := range localRides {
		tempRepo.AddWithID(ride)
	}

	for peerID := range s.clusterManager.Peers {
		peerRides, err := s.clusterManager.ListRidesByDateREST(peerID, data.Date)
		if err != nil {
			fmt.Printf("[SERVER %s] Peer %s indisponível na busca: %v\n", s.id, peerID, err)
			continue
		}
		for _, ride := range peerRides {
			tempRepo.AddWithID(ride)
		}
	}

	itineraries := tempRepo.SearchItineraries(domain.SearchFilter{
		Origin:      data.Origin,
		Destination: data.Destination,
		Date:        data.Date,
	})

	jsonResult, _ := json.Marshal(itineraries)
	return protocol.Response{
		Status:  "SUCCESS",
		Message: fmt.Sprintf("Encontradas %d opção(ões).", len(itineraries)),
		Data:    string(jsonResult),
	}
}

// handleBook executa a reserva distribuída por 2PC.
func (s *Server) handleBook(req protocol.Request) protocol.Response {
	var itin domain.ItineraryResult
	if err := json.Unmarshal([]byte(req.Data), &itin); err != nil {
		return protocol.Response{Status: "ERROR", Code: "INVALID_DATA", Message: "Payload de reserva inválido."}
	}
	if len(itin.Segments) == 0 {
		return protocol.Response{Status: "ERROR", Code: "INVALID_DATA", Message: "Itinerário sem trechos."}
	}

	lamportTS := s.reqManager.NextTimestamp()
	transactionID := fmt.Sprintf("TX_%d", time.Now().UnixNano())
	s.txLog.BeginCoordination(transactionID)

	refs := make([]domain.SegmentRef, 0, len(itin.Segments))
	for _, seg := range itin.Segments {
		refs = append(refs, domain.SegmentRef{RideID: seg.RideID, From: seg.From, To: seg.To})
	}

	var localErr error
	if err := s.reqManager.SubmitAt(lamportTS, s.id, func() error {
		localErr = s.txLog.PrepareWithCoordinator(s.repo, transactionID, s.id, refs)
		if localErr == nil {
			s.saveState()
		}
		return nil
	}); err != nil {
		localErr = err
	}

	successPrepare := localErr == nil
	votedPeers := []string{}
	contactedPeers := []string{}

	if successPrepare {
		protoSegments := make([]protocol.SegmentInfo, 0, len(itin.Segments))
		for _, seg := range itin.Segments {
			protoSegments = append(protoSegments, protocol.SegmentInfo{
				RideID:         seg.RideID,
				CompanyID:      seg.CompanyID,
				DriverToken:    seg.DriverToken,
				From:           seg.From,
				To:             seg.To,
				DepartureTime:  seg.DepartureTime,
				Price:          seg.Price,
				AvailableSeats: seg.AvailableSeats,
			})
		}

		prepareData := protocol.TwoPCPrepareData{
			TransactionID: transactionID,
			LamportTime:   lamportTS,
			CoordinatorID: s.id,
			Itinerary: protocol.ItineraryOption{
				ItineraryID:    itin.ItineraryID,
				Segments:       protoSegments,
				TotalPrice:     itin.TotalPrice,
				DepartureTime:  itin.DepartureTime,
				AvailableSeats: itin.AvailableSeats,
				Drivers:        itin.Drivers,
			},
		}

		// Envia prepare apenas aos nós que possuem trechos deste itinerário.
		peerSet := make(map[string]bool)
		for _, seg := range itin.Segments {
			if seg.CompanyID != "" && seg.CompanyID != s.id {
				peerSet[seg.CompanyID] = true
			}
		}

		for peerID := range peerSet {
			contactedPeers = append(contactedPeers, peerID)
			resp, err := s.clusterManager.PrepareREST(peerID, prepareData)
			if err != nil || resp.Status != "VOTE_COMMIT" {
				successPrepare = false
				break
			}
			votedPeers = append(votedPeers, peerID)
		}
	}

	if !successPrepare {
		s.recordDecision(transactionID, domain.DecisionAbort)

		if err := s.reqManager.Submit(func() error {
			s.txLog.Abort(s.repo, transactionID)
			s.saveState()
			return nil
		}); err != nil {
			fmt.Printf("[SERVER %s] Aviso: abort local falhou na transação %s: %v\n", s.id, transactionID, err)
		}

		for _, peerID := range contactedPeers {
			if err := s.clusterManager.AbortREST(peerID, transactionID); err != nil {
				fmt.Printf("[SERVER %s] Aviso: abort não confirmado em %s: %v\n", s.id, peerID, err)
			}
		}
		return protocol.Response{Status: "ERROR", Code: "2PC_ABORTED", Message: "Transação abortada: falha ao garantir vagas."}
	}

	s.recordDecision(transactionID, domain.DecisionCommit)
	for _, peerID := range votedPeers {
		if err := s.clusterManager.CommitREST(peerID, transactionID); err != nil {
			fmt.Printf("[SERVER %s] Aviso: commit não confirmado em %s: %v\n", s.id, peerID, err)
		}
	}

	var bookingID string
	if err := s.reqManager.Submit(func() error {
		s.txLog.Commit(transactionID)
		bookingID = s.repo.SaveBooking(req.Token, itin, "CONFIRMED")
		s.saveState()
		return nil
	}); err != nil {
		return protocol.Response{Status: "ERROR", Code: "WORKER_EXECUTION_FAILED", Message: err.Error()}
	}

	fmt.Printf("[SERVER %s] Transação 2PC bem-sucedida! Reserva %s confirmada.\n", s.id, bookingID)
	return protocol.Response{
		Status:  "SUCCESS",
		Message: fmt.Sprintf("Reserva confirmada sob o ID %s via 2PC.", bookingID),
		Data:    fmt.Sprintf(`{"booking_id":"%s"}`, bookingID),
	}
}

// handleCancelBooking marca a reserva como cancelada e pede a devolução das vagas
// às empresas que possuem trechos nela. Falhas ficam registradas para nova tentativa.
func (s *Server) handleCancelBooking(req protocol.Request) protocol.Response {
	var data protocol.CancelBookingData
	if err := json.Unmarshal([]byte(req.Data), &data); err != nil {
		return protocol.Response{Status: "ERROR", Code: "INVALID_DATA", Message: "Payload de cancelamento inválido."}
	}

	var booking domain.Booking
	if err := s.reqManager.Submit(func() error {
		var err error
		booking, err = s.repo.BeginCancelBooking(data.BookingID, req.Token)
		if err == nil {
			s.saveState()
		}
		return err
	}); err != nil {
		return protocol.Response{Status: "ERROR", Code: "CANCEL_FAILED", Message: err.Error()}
	}

	companies := make(map[string]bool)
	for _, companyID := range booking.PendingReleases {
		companies[companyID] = true
	}

	// Garante que cada empresa é processada uma vez, mesmo se houver vários trechos seus.
	for companyID := range companies {
		var releaseErr error

		if companyID == s.id {
			refs := make([]domain.SegmentRef, 0)
			for _, seg := range booking.Itinerary.Segments {
				if seg.CompanyID == s.id {
					refs = append(refs, domain.SegmentRef{RideID: seg.RideID, From: seg.From, To: seg.To})
				}
			}

			releaseErr = s.reqManager.Submit(func() error {
				s.repo.ApplyRelease(booking.ID, refs)
				s.repo.CompleteRelease(booking.ID, companyID)
				s.saveState()
				return nil
			})
		} else {
			releaseData := protocol.ReleaseSeatsData{
				BookingID: booking.ID,
				Segments:  make([]protocol.SegmentInfo, 0, len(booking.Itinerary.Segments)),
			}
			for _, seg := range booking.Itinerary.Segments {
				releaseData.Segments = append(releaseData.Segments, protocol.SegmentInfo{
					RideID:    seg.RideID,
					CompanyID: seg.CompanyID,
					From:      seg.From,
					To:        seg.To,
				})
			}

			releaseErr = s.clusterManager.ReleaseREST(companyID, releaseData)
			if releaseErr == nil {
				releaseErr = s.reqManager.Submit(func() error {
					s.repo.CompleteRelease(booking.ID, companyID)
					s.saveState()
					return nil
				})
			}
		}

		if releaseErr != nil {
			fmt.Printf("[SERVER %s] Devolução da reserva %s pendente em %s: %v\n",
				s.id, booking.ID, companyID, releaseErr)
		}
	}

	return protocol.Response{
		Status:  "SUCCESS",
		Message: "Reserva cancelada. As devoluções de vagas pendentes serão tentadas novamente.",
	}
}

// dispatch executa as ações locais dentro do Single Worker.
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
		s.saveState()
		fmt.Printf("[SERVER %s] Carona %s ofertada por %s.\n", s.id, rideID, req.Token)
		return protocol.Response{
			Status:  "SUCCESS",
			Message: fmt.Sprintf("Carona registrada sob o ID %s.", rideID),
			Data:    fmt.Sprintf(`{"ride_id":"%s"}`, rideID),
		}

	case protocol.ActionListRides:
		rides := s.repo.ListByDriver(req.Token)
		data, _ := json.Marshal(rides)
		return protocol.Response{Status: "SUCCESS", Data: string(data)}

	case protocol.ActionCancelRide:
		var data protocol.CancelRideData
		if err := json.Unmarshal([]byte(req.Data), &data); err != nil {
			return protocol.Response{Status: "ERROR", Code: "INVALID_DATA", Message: "Payload inválido."}
		}
		if err := s.repo.CancelRide(data.RideID, req.Token); err != nil {
			return protocol.Response{Status: "ERROR", Code: "CANCEL_FAILED", Message: err.Error()}
		}
		s.saveState()
		fmt.Printf("[SERVER %s] Carona %s cancelada por %s.\n", s.id, data.RideID, req.Token)
		return protocol.Response{Status: "SUCCESS", Message: "Carona cancelada com sucesso."}

	case "INTERNAL_LIST_RIDES_BY_DATE":
		var data struct {
			Date string `json:"date"`
		}
		_ = json.Unmarshal([]byte(req.Data), &data)
		rides := s.repo.ListByDate(data.Date)
		result, _ := json.Marshal(rides)
		return protocol.Response{Status: "SUCCESS", Data: string(result)}

	case protocol.Action2PCPrepare:
		var data protocol.TwoPCPrepareData
		if err := json.Unmarshal([]byte(req.Data), &data); err != nil {
			return protocol.Response{Status: "VOTE_ABORT", Message: "Payload de prepare inválido."}
		}

		refs := make([]domain.SegmentRef, 0, len(data.Itinerary.Segments))
		for _, seg := range data.Itinerary.Segments {
			refs = append(refs, domain.SegmentRef{RideID: seg.RideID, From: seg.From, To: seg.To})
		}
		if err := s.txLog.PrepareWithCoordinator(s.repo, data.TransactionID, data.CoordinatorID, refs); err != nil {
			return protocol.Response{Status: "VOTE_ABORT", Message: "Vagas esgotadas neste nó."}
		}
		s.saveState()
		return protocol.Response{Status: "VOTE_COMMIT", Message: "Preparado para confirmar."}

	case protocol.Action2PCCommit:
		var data txIDData
		if err := json.Unmarshal([]byte(req.Data), &data); err != nil {
			return protocol.Response{Status: "ERROR", Code: "INVALID_DATA", Message: "Payload de commit inválido."}
		}
		s.txLog.Commit(data.TransactionID)
		s.saveState()
		fmt.Printf("[SERVER %s] Recebido GLOBAL_COMMIT da transação %s.\n", s.id, data.TransactionID)
		return protocol.Response{Status: "SUCCESS", Message: "Commit efetivado."}

	case protocol.Action2PCAbort:
		var data txIDData
		if err := json.Unmarshal([]byte(req.Data), &data); err != nil {
			return protocol.Response{Status: "ERROR", Code: "INVALID_DATA", Message: "Payload de abort inválido."}
		}
		released := s.txLog.Abort(s.repo, data.TransactionID)
		s.saveState()
		fmt.Printf("[SERVER %s] Recebido GLOBAL_ABORT da transação %s. Trechos devolvidos: %d.\n",
			s.id, data.TransactionID, released)
		return protocol.Response{Status: "SUCCESS", Message: "Abort efetivado."}

	case protocol.ActionListBookings:
		bookings := s.repo.ListBookingsByPassenger(req.Token)
		data, _ := json.Marshal(bookings)
		return protocol.Response{Status: "SUCCESS", Data: string(data)}

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
	_, _ = conn.Write(data)
}
