package server

import (
	"fmt"
	"time"

	"github.com/Figueredo-D/PBL_REDES/internal/domain"
)

// recoveryInterval é o intervalo entre as rodadas de recuperação das transações pendentes.
const recoveryInterval = 5 * time.Second

// decisionUnknown é devolvido quando o coordenador não conhece a transação.
const decisionUnknown = "UNKNOWN"

// StartRecovery inicia a recuperação do 2PC em segundo plano. Primeiro resolve as transações que este nó
// coordenava quando caiu e depois, em rodadas periódicas, consulta o coordenador de cada transação pendente.
// Deve ser chamada antes de Start(), que bloqueia a execução.
func (s *Server) StartRecovery() {
	go func() {
		s.resolveOwnInterrupted()
		s.recoverPending()

		ticker := time.NewTicker(recoveryInterval)
		defer ticker.Stop()
		for range ticker.C {
			s.recoverPending()
		}
	}()
}

// resolveOwnInterrupted trata as transações que este nó coordenava sem decisão gravada.
// Como o COMMIT só é enviado depois de gravar a decisão, essas transações são abortadas com segurança.
// Os participantes descobrem o ABORT ao consultar o coordenador na recuperação.
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
			// O coordenador ainda está decidindo. Não se decide por ele: espera a próxima rodada.
		default:
			// Coordenador sem registro da transação. Não é seguro liberar nem confirmar,
			// então a transação fica pendente e um aviso é registrado.
			fmt.Printf("[RECOVERY %s] Coordenador %s não conhece a transação %s. Mantida pendente.\n", s.id, coordID, txID)
		}
	}
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

// aplicarDecisao efetiva COMMIT ou ABORT de uma transação pendente deste nó, dentro do Single Worker.
// Chamadas repetidas são seguras: Commit e Abort são idempotentes no TxLog.
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
		fmt.Printf("[RECOVERY %s] Transação %s abortada pela recuperação. Trechos devolvidos: %d.\n", s.id, txID, liberadas)
	}
}
