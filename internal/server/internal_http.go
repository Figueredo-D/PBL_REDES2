package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/Figueredo-D/PBL_REDES/internal/domain"
	"github.com/Figueredo-D/PBL_REDES/internal/protocol"
)

// apiError é o formato padrão de erro da API interna, conforme o contrato em docs.
type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// internalSegmentDTO representa um trecho na resposta da API interna.
type internalSegmentDTO struct {
	From           string  `json:"from"`
	To             string  `json:"to"`
	Price          float64 `json:"price"`
	AvailableSeats int     `json:"available_seats"`
}

// internalRideDTO é a representação de uma carona na API interna.
// Usamos um DTO em vez de serializar domain.Ride diretamente, para copiar os dados sob a trava de leitura da carona.
type internalRideDTO struct {
	ID          string               `json:"id"`
	CompanyID   string               `json:"company_id"`
	DriverToken string               `json:"driver_token"`
	Route       []string             `json:"route"`
	Date        string               `json:"date"`
	Time        string               `json:"time"`
	TotalSeats  int                  `json:"total_seats"`
	Segments    []internalSegmentDTO `json:"segments"`
}

// StartInternalHTTP sobe a API REST interna do nó em um endereço separado do TCP dos clientes.
// Deve ser chamada antes de Start(), que bloqueia a execução.
func (s *Server) StartInternalHTTP(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/internal/rides", s.handleInternalRides)
	mux.HandleFunc("/internal/prepare", s.handleInternal2PC(protocol.Action2PCPrepare))
	mux.HandleFunc("/internal/commit", s.handleInternal2PC(protocol.Action2PCCommit))
	mux.HandleFunc("/internal/abort", s.handleInternal2PC(protocol.Action2PCAbort))

	l, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("falha ao abrir a API interna em %s: %w", addr, err)
	}

	httpServer := &http.Server{
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}

	fmt.Printf("[SERVER %s] API REST interna escutando em http://%s\n", s.id, addr)

	go func() {
		if err := httpServer.Serve(l); err != nil && err != http.ErrServerClosed {
			fmt.Printf("[SERVER %s] Erro na API REST interna: %v\n", s.id, err)
		}
	}()

	return nil
}

// handleInternalRides implementa GET /internal/rides?date=AAAA-MM-DD.
func (s *Server) handleInternalRides(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "use o método GET")
		return
	}

	date := r.URL.Query().Get("date")
	if _, err := time.Parse("2006-01-02", date); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_DATA", "parâmetro 'date' obrigatório no formato AAAA-MM-DD")
		return
	}

	rides := s.repo.ListByDate(date)
	out := make([]internalRideDTO, 0, len(rides))
	for _, ride := range rides {
		out = append(out, toInternalRideDTO(ride))
	}

	writeAPIJSON(w, http.StatusOK, out)
}

// handleInternal2PC implementa POST /internal/prepare, /internal/commit e /internal/abort.
// A requisição passa pelo mesmo Single Worker usado pelo TCP, então a lógica de participante
// continua em um único lugar (o dispatch) e a ordem de processamento permanece única.
func (s *Server) handleInternal2PC(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "use o método POST")
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "INVALID_DATA", "não foi possível ler o corpo da requisição")
			return
		}

		var resp protocol.Response
		task := func() error {
			resp = s.dispatch(protocol.Request{Action: action, Data: string(body)})
			return nil
		}

		if action == protocol.Action2PCPrepare {
			var data protocol.TwoPCPrepareData
			if jerr := json.Unmarshal(body, &data); jerr != nil {
				writeAPIError(w, http.StatusBadRequest, "INVALID_DATA", "corpo do prepare inválido")
				return
			}
			err = s.reqManager.SubmitAt(data.LamportTime, data.CoordinatorID, task)
		} else {
			err = s.reqManager.Submit(task)
		}

		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "WORKER_EXECUTION_FAILED", err.Error())
			return
		}

		if action == protocol.Action2PCPrepare {
			if resp.Status == "VOTE_COMMIT" {
				writeAPIJSON(w, http.StatusOK, resp)
				return
			}
			writeAPIJSON(w, http.StatusConflict, resp)
			return
		}

		if resp.Status != "SUCCESS" {
			writeAPIJSON(w, http.StatusBadRequest, resp)
			return
		}

		writeAPIJSON(w, http.StatusOK, resp)
	}
}

// toInternalRideDTO copia os dados da carona sob a trava de leitura, evitando leitura concorrente com reservas.
func toInternalRideDTO(ride *domain.Ride) internalRideDTO {
	ride.RLock()
	defer ride.RUnlock()

	segments := make([]internalSegmentDTO, 0, len(ride.Segments))
	for _, seg := range ride.Segments {
		segments = append(segments, internalSegmentDTO{
			From:           seg.From,
			To:             seg.To,
			Price:          seg.Price,
			AvailableSeats: seg.AvailableSeats,
		})
	}

	return internalRideDTO{
		ID:          ride.ID,
		CompanyID:   ride.CompanyID,
		DriverToken: ride.DriverToken,
		Route:       ride.Route,
		Date:        ride.Date,
		Time:        ride.Time,
		TotalSeats:  ride.TotalSeats,
		Segments:    segments,
	}
}

func writeAPIJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	writeAPIJSON(w, status, apiError{Code: code, Message: message})
}
