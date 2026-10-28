package domain

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Estados que um participante pode receber ao perguntar sobre uma transação.
const (
	DecisionCommit     = "COMMIT"
	DecisionAbort      = "ABORT"
	DecisionInProgress = "IN_PROGRESS"
)

// SegmentRef identifica um trecho envolvido em uma transação 2PC.
type SegmentRef struct {
	RideID string `json:"ride_id"`
	From   string `json:"from"`
	To     string `json:"to"`
}

// txLogArquivo é o conteúdo gravado em disco. Cada nó tem o seu próprio txlog.json.
type txLogArquivo struct {
	Pending      map[string][]SegmentRef `json:"pending"`      // transações preparadas, aguardando commit ou abort
	Aborted      map[string]bool         `json:"aborted"`      // transações abortadas, para bloquear um prepare atrasado
	Coordinators map[string]string       `json:"coordinators"` // txID -> ID do coordenador (quem consultar na recuperação)
	Decisions    map[string]string       `json:"decisions"`    // txID -> COMMIT ou ABORT, gravado antes de qualquer aviso
	InProgress   map[string]bool         `json:"in_progress"`  // transações que este nó está coordenando sem decisão ainda
}

// TxLog registra o estado das transações 2PC deste nó: reservas locais, coordenadores,
// decisões globais e transações em andamento. Toda mudança é gravada em disco logo em seguida.
type TxLog struct {
	mu           sync.Mutex
	path         string
	pending      map[string][]SegmentRef
	aborted      map[string]bool
	coordinators map[string]string
	decisions    map[string]string
	inProgress   map[string]bool
}

// LoadTxLog carrega o registro de transações do arquivo em path.
// Se o arquivo não existir (primeira execução), devolve um registro vazio.
// Arquivos gravados por versões anteriores, sem os campos novos, também são aceitos.
func LoadTxLog(path string) (*TxLog, error) {
	t := &TxLog{
		path:         path,
		pending:      make(map[string][]SegmentRef),
		aborted:      make(map[string]bool),
		coordinators: make(map[string]string),
		decisions:    make(map[string]string),
		inProgress:   make(map[string]bool),
	}

	dados, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return t, nil
	}
	if err != nil {
		return nil, fmt.Errorf("falha ao ler o registro de transações %s: %w", path, err)
	}

	var arq txLogArquivo
	if err := json.Unmarshal(dados, &arq); err != nil {
		return nil, fmt.Errorf("registro de transações %s corrompido: %w", path, err)
	}

	if arq.Pending != nil {
		t.pending = arq.Pending
	}
	if arq.Aborted != nil {
		t.aborted = arq.Aborted
	}
	if arq.Coordinators != nil {
		t.coordinators = arq.Coordinators
	}
	if arq.Decisions != nil {
		t.decisions = arq.Decisions
	}
	if arq.InProgress != nil {
		t.inProgress = arq.InProgress
	}

	return t, nil
}

// salvarLocked grava o registro em disco de forma atômica. Deve ser chamada com t.mu travado.
func (t *TxLog) salvarLocked() {
	if t.path == "" {
		return
	}

	arq := txLogArquivo{
		Pending:      t.pending,
		Aborted:      t.aborted,
		Coordinators: t.coordinators,
		Decisions:    t.decisions,
		InProgress:   t.inProgress,
	}

	dados, err := json.MarshalIndent(arq, "", "  ")
	if err != nil {
		fmt.Printf("[TXLOG] Aviso: falha ao serializar registro: %v\n", err)
		return
	}

	if err := os.MkdirAll(filepath.Dir(t.path), 0o755); err != nil {
		fmt.Printf("[TXLOG] Aviso: falha ao criar diretório: %v\n", err)
		return
	}

	tmp := t.path + ".tmp"
	if err := os.WriteFile(tmp, dados, 0o644); err != nil {
		fmt.Printf("[TXLOG] Aviso: falha ao gravar arquivo temporário: %v\n", err)
		return
	}

	if err := os.Rename(tmp, t.path); err != nil {
		fmt.Printf("[TXLOG] Aviso: falha ao substituir o registro: %v\n", err)
	}
}

// Prepare reserva os trechos locais sem registrar coordenador. Mantido para compatibilidade com o listener atual.
func (t *TxLog) Prepare(repo *RideRepository, txID string, segs []SegmentRef) error {
	return t.PrepareWithCoordinator(repo, txID, "", segs)
}

// PrepareWithCoordinator reserva os trechos locais da transação e registra quem é o coordenador.
// Se um dos trechos falhar, desfaz somente as reservas feitas nesta chamada.
func (t *TxLog) PrepareWithCoordinator(repo *RideRepository, txID, coordinatorID string, segs []SegmentRef) error {
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
	if coordinatorID != "" {
		t.coordinators[txID] = coordinatorID
	}
	t.salvarLocked()
	return nil
}

// BeginCoordination marca uma transação como em andamento neste nó, antes de qualquer reserva.
// Enquanto estiver marcada sem decisão, consultas de recuperação recebem IN_PROGRESS e ninguém decide por ela.
func (t *TxLog) BeginCoordination(txID string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.inProgress[txID] = true
	t.salvarLocked()
}

// RecordDecision grava a decisão global (COMMIT ou ABORT) da transação. Deve ser chamada antes de
// qualquer COMMIT ou ABORT ser enviado aos participantes. Uma decisão já gravada não pode mudar.
func (t *TxLog) RecordDecision(txID, decision string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if decision != DecisionCommit && decision != DecisionAbort {
		return fmt.Errorf("decisão inválida: %s", decision)
	}

	if atual, ok := t.decisions[txID]; ok {
		if atual == decision {
			return nil
		}
		return fmt.Errorf("transação %s já decidida como %s", txID, atual)
	}

	t.decisions[txID] = decision
	delete(t.inProgress, txID)
	t.salvarLocked()
	return nil
}

// DecisionFor informa o estado de uma transação deste nó: COMMIT, ABORT, IN_PROGRESS,
// ou uma string vazia quando o nó não conhece a transação.
func (t *TxLog) DecisionFor(txID string) string {
	t.mu.Lock()
	defer t.mu.Unlock()

	if d, ok := t.decisions[txID]; ok {
		return d
	}
	if t.inProgress[txID] {
		return DecisionInProgress
	}
	return ""
}

// PendingTransactions devolve as transações preparadas que aguardam decisão, com o ID do coordenador de cada uma.
// Transações criadas pelo próprio nó podem ter coordenador vazio.
func (t *TxLog) PendingTransactions() map[string]string {
	t.mu.Lock()
	defer t.mu.Unlock()

	out := make(map[string]string, len(t.pending))
	for txID := range t.pending {
		out[txID] = t.coordinators[txID]
	}
	return out
}

// ResolveInterruptedCoordinations trata as transações que este nó coordenava quando caiu, sem decisão gravada.
// Como o COMMIT só é enviado depois de gravar a decisão, nenhum participante pode ter recebido COMMIT
// dessas transações, então todas são abortadas com segurança. Devolve os IDs resolvidos para que
// o nó avise os participantes.
func (t *TxLog) ResolveInterruptedCoordinations(repo *RideRepository) []string {
	t.mu.Lock()
	defer t.mu.Unlock()

	var resolvidas []string
	for txID := range t.inProgress {
		if _, ok := t.decisions[txID]; !ok {
			t.decisions[txID] = DecisionAbort
		}
		t.abortLocked(repo, txID)
		delete(t.inProgress, txID)
		resolvidas = append(resolvidas, txID)
	}

	if len(resolvidas) > 0 {
		t.salvarLocked()
	}
	return resolvidas
}

// Commit efetiva a transação: as vagas continuam reservadas e o registro de pendência é removido.
// Chamadas repetidas não causam efeito.
func (t *TxLog) Commit(txID string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	delete(t.pending, txID)
	delete(t.coordinators, txID)
	t.salvarLocked()
}

// Abort desfaz a transação devolvendo as vagas que o prepare reservou neste nó.
// Se a transação nunca reservou nada aqui, não faz nada. Retorna a quantidade de trechos devolvidos.
func (t *TxLog) Abort(repo *RideRepository, txID string) int {
	t.mu.Lock()
	defer t.mu.Unlock()

	n := t.abortLocked(repo, txID)
	t.salvarLocked()
	return n
}

// abortLocked faz o trabalho do Abort. Deve ser chamada com t.mu travado e não grava em disco.
func (t *TxLog) abortLocked(repo *RideRepository, txID string) int {
	// Marca como abortada, para que um prepare atrasado não reserve vagas depois
	t.aborted[txID] = true

	reserved := t.pending[txID]
	for _, r := range reserved {
		repo.ReleaseLocalSegment(r.RideID, r.From, r.To)
	}
	delete(t.pending, txID)
	delete(t.coordinators, txID)
	return len(reserved)
}
