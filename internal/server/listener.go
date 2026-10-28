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
	id             string // Identificador único deste servidor no consórcio
	listener       net.Listener
	repo           *domain.RideRepository
	reqManager     *concurrency.RequestManager // Gestor de Fila de Prioridade com Single Worker
	clusterManager *cluster.ClusterManager     // Gestor de comunicação inter-nós do consórcio
	txLog          *domain.TxLog               // Registro das transações 2PC deste nó (pendentes, decisões e coordenadores)
	dataPath       string                      // Arquivo JSON deste nó (ex: data/SERVER_A/estado.json)
	mu             sync.Mutex
	active         bool
}

// NewServer carrega o estado salvo deste nó (ou começa vazio na primeira execução) e monta o servidor.
// O registro de transações fica no mesmo diretório do estado, em txlog.json.
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

// saveState grava o estado atual do nó em disco. Deve ser chamada dentro do Single Worker, logo após uma mudança.
func (s *Server) saveState() {
	if err := s.repo.SaveToFile(s.dataPath); err != nil {
		fmt.Printf("[SERVER %s] Aviso: falha ao salvar estado: %v\n", s.id, err)
	}
}

// recordDecision grava a decisão global da transação antes de ela ser enviada aos participantes.
func (s *Server) recordDecision(txID, decision string) {
	if err := s.txLog.RecordDecision(txID, decision); err != nil {
		fmt.Printf("[SERVER %s] Aviso: falha ao gravar decisão %s da transação %s: %v\n", s.id, decision, txID, err)
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

		resp := s.route(req)
		s.sendResponse(conn, resp)
	}
}

// route decide onde cada ação é executada. Ações que chamam outros nós (busca e 2PC do coordenador)
// rodam fora do Single Worker; o worker só recebe trabalho local e rápido.
func (s *Server) route(req protocol.Request) protocol.Response {
	switch req.Action {
	case protocol.ActionSearchItinerary:
		return s.handleSearch(req)
	case protocol.ActionBookItinerary:
		return s.handleBook(req)
	}

	// Demais ações são locais: passam pelo worker, na ordem da fila de prioridade
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

// handleSearch monta a busca unificada. A leitura local passa pelo worker; a consulta aos peers via REST
// acontece fora dele, para não prender o worker enquanto espera a rede.
func (s *Server) handleSearch(req protocol.Request) protocol.Response {
	var data protocol.SearchItineraryData
	if err := json.Unmarshal([]byte(req.Data), &data); err != nil {
		return protocol.Response{Status: "ERROR", Code: "INVALID_DATA", Message: "Payload de busca inválido."}
	}

	// 1. Leitura local dentro do worker
	var localRides []*domain.Ride
	if err := s.reqManager.Submit(func() error {
		localRides = s.repo.ListByDate(data.Date)
		return nil
	}); err != nil {
		return protocol.Response{Status: "ERROR", Code: "WORKER_EXECUTION_FAILED", Message: err.Error()}
	}

	// 2. Repositório temporário unificado, preservando os IDs e as vagas originais
	tempRepo := domain.NewRideRepository(s.id)
	for _, r := range localRides {
		tempRepo.AddWithID(r)
	}

	// 3. Consulta os peers via REST fora do worker. Uma falha de peer é registrada e a busca segue.
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
}

// handleBook executa o papel de coordenador do 2PC. Gera o timestamp de Lamport da transação e o envia
// em todos os prepares. Somente as operações locais entram no worker; as chamadas REST saem dele.
// A decisão global é gravada no TxLog antes de qualquer COMMIT ou ABORT ser enviado.
func (s *Server) handleBook(req protocol.Request) protocol.Response {
	var itin domain.ItineraryResult
	if err := json.Unmarshal([]byte(req.Data), &itin); err != nil {
		return protocol.Response{Status: "ERROR", Code: "INVALID_DATA", Message: "Payload de reserva inválido."}
	}

	// Itinerário vazio não pode ser confirmado: senão todos os peers votariam COMMIT sem reservar nada
	if len(itin.Segments) == 0 {
		return protocol.Response{Status: "ERROR", Code: "INVALID_DATA", Message: "Itinerário sem trechos."}
	}

	// Timestamp de Lamport desta transação. Define a precedência em todos os nós envolvidos.
	lamportTS := s.reqManager.NextTimestamp()
	transactionID := fmt.Sprintf("TX_%d", time.Now().UnixNano())

	// Marca a transação como em andamento antes de qualquer reserva.
	// Se o coordenador cair sem decisão gravada, a recuperação sabe que ela não foi decidida.
	s.txLog.BeginCoordination(transactionID)

	refs := make([]domain.SegmentRef, 0, len(itin.Segments))
	for _, seg := range itin.Segments {
		refs = append(refs, domain.SegmentRef{RideID: seg.RideID, From: seg.From, To: seg.To})
	}

	// 1. Fase 1 local: reserva os trechos locais pelo TxLog, dentro do worker, na ordem da transação
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
	votedPeers := []string{}     // peers que responderam VOTE_COMMIT
	contactedPeers := []string{} // peers que receberam PREPARE, mesmo que a resposta tenha falhado

	// 2. Fase 1 remota: envia PREPARE via REST fora do worker
	if successPrepare {
		var protoSegments []protocol.SegmentInfo
		for _, sSeg := range itin.Segments {
			protoSegments = append(protoSegments, protocol.SegmentInfo{
				RideID:         sSeg.RideID,
				CompanyID:      sSeg.CompanyID,
				DriverToken:    sSeg.DriverToken,
				From:           sSeg.From,
				To:             sSeg.To,
				DepartureTime:  sSeg.DepartureTime,
				Price:          sSeg.Price,
				AvailableSeats: sSeg.AvailableSeats,
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

		for peerID := range s.clusterManager.Peers {
			contactedPeers = append(contactedPeers, peerID)

			resp, err := s.clusterManager.PrepareREST(peerID, prepareData)
			if err != nil || resp.Status != "VOTE_COMMIT" {
				successPrepare = false
				break
			}
			votedPeers = append(votedPeers, peerID)
		}
	}

	// 3. Decisão global: ABORT
	if !successPrepare {
		// Grava a decisão antes de avisar qualquer participante
		s.recordDecision(transactionID, domain.DecisionAbort)

		// Devolve somente as vagas locais que este prepare reservou
		if err := s.reqManager.Submit(func() error {
			s.txLog.Abort(s.repo, transactionID)
			s.saveState()
			return nil
		}); err != nil {
			fmt.Printf("[SERVER %s] Aviso: abort local falhou na transação %s: %v\n", s.id, transactionID, err)
		}

		// O ABORT vai para todos os peers contatados, inclusive os que falharam por timeout
		for _, peerID := range contactedPeers {
			if err := s.clusterManager.AbortREST(peerID, transactionID); err != nil {
				fmt.Printf("[SERVER %s] Aviso: abort não confirmado em %s: %v\n", s.id, peerID, err)
			}
		}
		return protocol.Response{Status: "ERROR", Code: "2PC_ABORTED", Message: "Transação abortada: falha ao garantir vagas."}
	}

	// 4. Decisão global: COMMIT. Grava antes de enviar aos peers que votaram.
	s.recordDecision(transactionID, domain.DecisionCommit)

	for _, peerID := range votedPeers {
		if err := s.clusterManager.CommitREST(peerID, transactionID); err != nil {
			fmt.Printf("[SERVER %s] Aviso: commit não confirmado em %s: %v\n", s.id, peerID, err)
		}
	}

	// Efetiva localmente e salva a reserva dentro do worker
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

// dispatch executa as ações locais. Roda sempre dentro do Single Worker.
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

		s.saveState()
		fmt.Printf("[SERVER %s] Carona %s cancelada por %s.\n", s.id, data.RideID, req.Token)
		return protocol.Response{Status: "SUCCESS", Message: "Carona cancelada com sucesso."}

	case "INTERNAL_LIST_RIDES_BY_DATE":
		var data struct {
			Date string `json:"date"`
		}
		json.Unmarshal([]byte(req.Data), &data)

		rides := s.repo.ListByDate(data.Date)
		bytes, _ := json.Marshal(rides)
		return protocol.Response{Status: "SUCCESS", Data: string(bytes)}

	// --- 2PC: PAPEL DE PARTICIPANTE ---
	case protocol.Action2PCPrepare:
		var data protocol.TwoPCPrepareData
		if err := json.Unmarshal([]byte(req.Data), &data); err != nil {
			return protocol.Response{Status: "VOTE_ABORT", Message: "Payload de prepare inválido."}
		}

		refs := make([]domain.SegmentRef, 0, len(data.Itinerary.Segments))
		for _, seg := range data.Itinerary.Segments {
			refs = append(refs, domain.SegmentRef{RideID: seg.RideID, From: seg.From, To: seg.To})
		}

		// O TxLog reserva apenas os trechos locais e registra o que foi reservado para este abort
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
		fmt.Printf("[SERVER %s] Recebido GLOBAL_ABORT da transação %s. Trechos devolvidos: %d.\n", s.id, data.TransactionID, released)
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
