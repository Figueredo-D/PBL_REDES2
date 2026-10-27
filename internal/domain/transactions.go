package domain

import (
	"fmt"
	"sync"
)

// SegmentRef identifica um trecho envolvido em uma transação 2PC.
type SegmentRef struct {
	RideID string
	From   string
	To     string
}

// TxLog registra, por transação, quais trechos locais este nó reservou na fase de prepare.
// Assim o abort devolve exatamente as vagas reservadas, e o commit apenas esquece o registro.
type TxLog struct {
	mu      sync.Mutex
	pending map[string][]SegmentRef // transações preparadas, aguardando commit ou abort
	aborted map[string]bool         // transações abortadas, para bloquear um prepare atrasado
}

// NewTxLog cria o registro vazio de transações deste nó.
func NewTxLog() *TxLog {
	return &TxLog{
		pending: make(map[string][]SegmentRef),
		aborted: make(map[string]bool),
	}
}

// Prepare tenta reservar os trechos locais da transação. Se um deles falhar, desfaz somente
// as reservas feitas nesta chamada, para não deixar vagas presas.
func (t *TxLog) Prepare(repo *RideRepository, txID string, segs []SegmentRef) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.aborted[txID] {
		return fmt.Errorf("transação %s já foi abortada", txID)
	}

	// Prepare repetido: a reserva já existe, então não reserva de novo
	if _, ok := t.pending[txID]; ok {
		return nil
	}

	var reserved []SegmentRef
	for _, s := range segs {
		if _, isLocal := repo.GetRide(s.RideID); !isLocal {
			continue
		}
		if err := repo.TryReserveLocalSegment(s.RideID, s.From, s.To); err != nil {
			// Rollback parcial: devolve apenas o que este prepare reservou
			for _, r := range reserved {
				repo.ReleaseLocalSegment(r.RideID, r.From, r.To)
			}
			return err
		}
		reserved = append(reserved, s)
	}

	t.pending[txID] = reserved
	return nil
}

// Commit efetiva a transação: as vagas continuam reservadas e o registro é removido.
// Chamadas repetidas não causam efeito.
func (t *TxLog) Commit(txID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.pending, txID)
}

// Abort desfaz a transação devolvendo as vagas que o prepare reservou neste nó.
// Se a transação nunca chegou a reservar nada aqui, não faz nada. Retorna a quantidade de trechos devolvidos.
func (t *TxLog) Abort(repo *RideRepository, txID string) int {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Marca como abortada, para que um prepare atrasado não reserve vagas depois
	t.aborted[txID] = true

	reserved := t.pending[txID]
	for _, r := range reserved {
		repo.ReleaseLocalSegment(r.RideID, r.From, r.To)
	}
	delete(t.pending, txID)
	return len(reserved)
}
