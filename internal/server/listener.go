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

// reservedSeg guarda um trecho local reservado durante uma transação 2PC.
type reservedSeg struct {
    RideID string
    From   string
    To     string
}

type Server struct {
    address        string
    id             string
    listener       net.Listener
    repo           *domain.RideRepository
    reqManager     *concurrency.RequestManager
    clusterManager *cluster.ClusterManager
    mu             sync.Mutex
    active         bool

    pendingMu sync.Mutex
    pending   map[string][]reservedSeg
}

func NewServer(address string, serverID string, peers map[string]string) *Server {
    clock := concurrency.NewLogicalClock(serverID)
    return &Server{
        address:        address,
        id:             serverID,
        repo:           domain.NewRideRepository(serverID),
        reqManager:     concurrency.NewRequestManager(clock),
        clusterManager: cluster.NewClusterManager(serverID, peers),
        pending:        make(map[string][]reservedSeg),
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

        // Mensagens internas entre nós são processadas fora da fila do worker,
        // para evitar deadlock quando dois nós coordenam transações ao mesmo tempo.
        if s.isInternalAction(req.Action) {
            resp = s.dispatch(req)
        } else {
            err := s.reqManager.Submit(func() error {
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
        }

        s.sendResponse(conn, resp)
    }
}

// isInternalAction indica se a ação é uma mensagem trocada entre nós do consórcio.
func (s *Server) isInternalAction(action string) bool {
    switch action {
    case "INTERNAL_LIST_RIDES_BY_DATE", "INTERNAL_RIDE_CANCELLED", protocol.Action2PCPrepare, protocol.Action2PCCommit, protocol.Action2PCAbort:
        return true
    }
    return false
}

// releaseSegments devolve as vagas de uma lista de trechos reservados localmente.
func (s *Server) releaseSegments(segs []reservedSeg) {
    for _, seg := range segs {
        s.repo.ReleaseLocalSegment(seg.RideID, seg.From, seg.To)
    }
}

// parseTransactionID extrai o ID da transação do payload de COMMIT ou ABORT.
func parseTransactionID(raw string) (string, error) {
    var data struct {
        TransactionID string `json:"transaction_id"`
    }
    if err := json.Unmarshal([]byte(raw), &data); err != nil {
        return "", err
    }
    return data.TransactionID, nil
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

        // Avisa os outros nós para marcarem como canceladas as reservas que eles guardam com esta carona
        notifyPayload, _ := json.Marshal(protocol.CancelRideData{RideID: data.RideID})
        for peerID := range s.clusterManager.Peers {
            s.clusterManager.SendMessage(peerID, protocol.Request{
                Action: "INTERNAL_RIDE_CANCELLED",
                Data:   string(notifyPayload),
            })
        }

        fmt.Printf("[SERVER %s] Carona %s cancelada por %s.\n", s.id, data.RideID, req.Token)
        return protocol.Response{Status: "SUCCESS", Message: "Carona cancelada com sucesso."}

    case protocol.ActionSearchItinerary:
        var data protocol.SearchItineraryData
        if err := json.Unmarshal([]byte(req.Data), &data); err != nil {
            return protocol.Response{Status: "ERROR", Code: "INVALID_DATA", Message: "Payload de busca inválido."}
        }

        tempRepo := domain.NewRideRepository(s.id)
        for _, r := range s.repo.ListByDate(data.Date) {
            tempRepo.AddWithID(r)
        }

        for peerID := range s.clusterManager.Peers {
            resp, err := s.clusterManager.SendMessage(peerID, protocol.Request{
                Action: "INTERNAL_LIST_RIDES_BY_DATE",
                Token:  req.Token,
                Data:   fmt.Sprintf(`{"date":"%s"}`, data.Date),
            })
            if err == nil && resp.Status == "SUCCESS" {
                var peerRides []*domain.Ride
                if json.Unmarshal([]byte(resp.Data), &peerRides) == nil {
                    for _, r := range peerRides {
                        tempRepo.AddWithID(r)
                    }
                }
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

    case "INTERNAL_RIDE_CANCELLED":
        var data protocol.CancelRideData
        if err := json.Unmarshal([]byte(req.Data), &data); err != nil {
            return protocol.Response{Status: "ERROR", Code: "INVALID_DATA", Message: "Payload inválido."}
        }

        n := s.repo.MarkBookingsCancelledByRide(data.RideID)
        fmt.Printf("[SERVER %s] Carona %s cancelada na origem: %d reserva(s) marcada(s) como cancelada(s).\n", s.id, data.RideID, n)
        return protocol.Response{Status: "SUCCESS"}

    case protocol.ActionBookItinerary:
        var itin domain.ItineraryResult
        if err := json.Unmarshal([]byte(req.Data), &itin); err != nil {
            return protocol.Response{Status: "ERROR", Code: "INVALID_DATA", Message: "Payload de reserva inválido."}
        }

        // --- 2PC: PAPEL DE COORDENADOR ---
        transactionID := fmt.Sprintf("TX_%d", time.Now().UnixNano())

        // 1. Reserva localmente apenas os trechos cuja empresa é este nó
        var reserved []reservedSeg
        successPrepare := true
        for _, seg := range itin.Segments {
            if seg.CompanyID != s.id {
                continue
            }
            if err := s.repo.TryReserveLocalSegment(seg.RideID, seg.From, seg.To); err != nil {
                fmt.Printf("[SERVER %s] PREPARE local falhou no trecho %s -> %s (carona %s): %v\n", s.id, seg.From, seg.To, seg.RideID, err)
                successPrepare = false
                break
            }
            reserved = append(reserved, reservedSeg{RideID: seg.RideID, From: seg.From, To: seg.To})
        }

        // 2. Se as vagas locais foram garantidas, envia 2PC_PREPARE a todos os peers
        votedPeers := []string{}
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
                if err != nil {
                    fmt.Printf("[SERVER %s] PREPARE falhou no peer %s: %v\n", s.id, peerID, err)
                    successPrepare = false
                    break
                }
                if resp.Status != "VOTE_COMMIT" {
                    fmt.Printf("[SERVER %s] Peer %s votou %s: %s (código %s)\n", s.id, peerID, resp.Status, resp.Message, resp.Code)
                    successPrepare = false
                    break
                }
                votedPeers = append(votedPeers, peerID)
            }
        }

        // Decisão global: ABORT
        if !successPrepare {
            s.releaseSegments(reserved)
            for peerID := range s.clusterManager.Peers {
                s.clusterManager.SendMessage(peerID, protocol.Request{
                    Action: protocol.Action2PCAbort,
                    Data:   fmt.Sprintf(`{"transaction_id":"%s"}`, transactionID),
                })
            }
            return protocol.Response{Status: "ERROR", Code: "2PC_ABORTED", Message: "Transação abortada: falha ao garantir vagas."}
        }

        // Decisão global: COMMIT
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

        var reserved []reservedSeg
        canCommit := true
        for _, seg := range data.Itinerary.Segments {
            if seg.CompanyID != s.id {
                continue
            }
            if err := s.repo.TryReserveLocalSegment(seg.RideID, seg.From, seg.To); err != nil {
                fmt.Printf("[SERVER %s] PREPARE recebido, mas o trecho %s -> %s (carona %s) falhou: %v\n", s.id, seg.From, seg.To, seg.RideID, err)
                canCommit = false
                break
            }
            reserved = append(reserved, reservedSeg{RideID: seg.RideID, From: seg.From, To: seg.To})
        }

        if !canCommit {
            s.releaseSegments(reserved)
            return protocol.Response{Status: "VOTE_ABORT", Message: "Vagas esgotadas neste nó."}
        }

        s.pendingMu.Lock()
        s.pending[data.TransactionID] = reserved
        s.pendingMu.Unlock()

        return protocol.Response{Status: "VOTE_COMMIT", Message: "Preparado para confirmar."}

    case protocol.Action2PCCommit:
        txID, err := parseTransactionID(req.Data)
        if err != nil {
            return protocol.Response{Status: "ERROR", Code: "INVALID_DATA", Message: "Payload de commit inválido."}
        }

        s.pendingMu.Lock()
        delete(s.pending, txID)
        s.pendingMu.Unlock()

        fmt.Printf("[SERVER %s] Recebido GLOBAL_COMMIT para a transação %s.\n", s.id, txID)
        return protocol.Response{Status: "SUCCESS", Message: "Commit efetivado."}

    case protocol.Action2PCAbort:
        txID, err := parseTransactionID(req.Data)
        if err != nil {
            return protocol.Response{Status: "ERROR", Code: "INVALID_DATA", Message: "Payload de abort inválido."}
        }

        s.pendingMu.Lock()
        segs := s.pending[txID]
        delete(s.pending, txID)
        s.pendingMu.Unlock()

        s.releaseSegments(segs)

        fmt.Printf("[SERVER %s] Recebido GLOBAL_ABORT para a transação %s. %d trecho(s) liberado(s).\n", s.id, txID, len(segs))
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