package server

import (
	"fmt"
	"time"

	"github.com/Figueredo-D/PBL_REDES/internal/domain"
	"github.com/Figueredo-D/PBL_REDES/internal/protocol"
)

// recoveryInterval é o intervalo entre as rodadas de recuperação.
const recoveryInterval = 5 * time.Second

// decisionUnknown é devolvido quando o coordenador não conhece a transação.
const decisionUnknown = "UNKNOWN"

// StartRecovery inicia a recuperação do 2PC e das devoluções de vagas pendentes.
// Deve ser chamada antes de Start(), que bloqueia a execução.
func (s *Server) StartRecovery() {
	go func() {
		s.resolveOwnInterrupted()
		s.recoverPending()
		s.recoverPendingReleases()

		ticker := time.NewTicker(recoveryInterval)
		defer ticker.Stop()

		for range ticker.C {
			s.recoverPending()
			s.recoverPendingReleases()
		}
	}()
}

// resolveOwnInterrupted trata as transações que este nó coordenava sem decisão gravada.
// Como o COMMIT só é enviado depois de gravar a decisão, essas transações são abortadas com segurança.
func (s *Server) resolveOwnInterrupted() {
	var resolvidas []string
	if err := s.reqManager.Submit(func() error {
		resolvidas = s.txLog.ResolveInterruptedCoordinations(s.repo)
		if len(resolvidas) > 0 {
			s.saveState()
		}
		return nil
	}); err != nil {
		fmt.Printf("[RECOVERY %s] Falha ao resolver coordenações interrompidas: %v\n", s.id, err)
		return
	}

	for _, txID := range resolvidas {
		fmt.Printf("[RECOVERY %s] Transação %s estava sem decisão gravada; abortada na recuperação.\n", s.id, txID)
	}
}

// recoverPending percorre as transações preparadas neste nó e pergunta a decisão a cada coordenador.
// Se o coordenador não responder, a transação continua pendente e será consultada na próxima rodada.
func (s *Server) recoverPending() {
	pendentes := s.txLog.PendingTransactions()

	for txID, coordID := range pendentes {
		decisao, err := s.decisaoDoCoordenador(txID, coordID)
		if err != nil {
			fmt.Printf("[RECOVERY %s] Decisão da transação %s indisponível (%v). Mantida pendente.\n", s.id, txID, err)
			continue
		}

		switch decisao {
		case domain.DecisionCommit, domain.DecisionAbort:
			s.aplicarDecisao(txID, decisao)
		case domain.DecisionInProgress:
			// O coordenador ainda está decidindo. Aguarda a próxima rodada.
		default:
			// Não é seguro liberar nem confirmar se o coordenador não conhece a transação.
			fmt.Printf("[RECOVERY %s] Coordenador %s não conhece a transação %s. Mantida pendente.\n", s.id, coordID, txID)
		}
	}
}

// recoverPendingReleases tenta novamente as devoluções de vagas de reservas canceladas.
// A reserva mantém a lista de empresas pendentes persistida; cada empresa concluída é removida
// dessa lista para que novas rodadas processem apenas o que ainda falta.
func (s *Server) recoverPendingReleases() {
	bookings := s.repo.BookingsWithPendingReleases()

	for _, booking := range bookings {
		for _, companyID := range booking.PendingReleases {
			if err := s.retryRelease(booking, companyID); err != nil {
				fmt.Printf("[RECOVERY %s] Devolução da reserva %s ainda pendente em %s: %v\n",
					s.id, booking.ID, companyID, err)
				continue
			}

			fmt.Printf("[RECOVERY %s] Devolução da reserva %s concluída em %s.\n",
				s.id, booking.ID, companyID)
		}
	}
}

// retryRelease tenta devolver as vagas de uma reserva em uma empresa.
// Em caso de sucesso, registra a conclusão no repositório local.
func (s *Server) retryRelease(booking domain.Booking, companyID string) error {
	var err error

	if companyID == s.id {
		refs := make([]domain.SegmentRef, 0, len(booking.Itinerary.Segments))
		for _, seg := range booking.Itinerary.Segments {
			if seg.CompanyID == s.id {
				refs = append(refs, domain.SegmentRef{
					RideID: seg.RideID,
					From:   seg.From,
					To:     seg.To,
				})
			}
		}

		err = s.reqManager.Submit(func() error {
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

		err = s.clusterManager.ReleaseREST(companyID, releaseData)
		if err == nil {
			err = s.reqManager.Submit(func() error {
				s.repo.CompleteRelease(booking.ID, companyID)
				s.saveState()
				return nil
			})
		}
	}

	if err != nil {
		return err
	}

	return nil
}

// decisaoDoCoordenador consulta a decisão de uma transação. Se o próprio nó é o coordenador,
// lê o registro local, sem chamada de rede.
func (s *Server) decisaoDoCoordenador(txID, coordID string) (string, error) {
	if coordID == "" {
		return "", fmt.Errorf("coordenador da transação não registrado")
	}

	if coordID == s.id {
		decisao := s.txLog.DecisionFor(txID)
		if decisao == "" {
			return decisionUnknown, nil
		}
		return decisao, nil
	}

	decisao, err := s.clusterManager.DecisionREST(coordID, txID)
	if err != nil {
		return "", err
	}
	return decisao, nil
}

// aplicarDecisao efetiva COMMIT ou ABORT de uma transação pendente deste nó,
// dentro do Single Worker. Chamadas repetidas são seguras.
func (s *Server) aplicarDecisao(txID, decisao string) {
	var liberadas int
	if err := s.reqManager.Submit(func() error {
		if decisao == domain.DecisionCommit {
			s.txLog.Commit(txID)
		} else {
			liberadas = s.txLog.Abort(s.repo, txID)
		}
		s.saveState()
		return nil
	}); err != nil {
		fmt.Printf("[RECOVERY %s] Falha ao aplicar %s na transação %s: %v\n", s.id, decisao, txID, err)
		return
	}

	if decisao == domain.DecisionCommit {
		fmt.Printf("[RECOVERY %s] Transação %s confirmada pela recuperação.\n", s.id, txID)
	} else {
		fmt.Printf("[RECOVERY %s] Transação %s abortada pela recuperação. Trechos devolvidos: %d.\n",
			s.id, txID, liberadas)
	}
}
