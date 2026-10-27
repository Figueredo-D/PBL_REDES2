package concurrency

import (
	"container/heap"
	"sync"
)

// LogicalClock gere o Relógio Lógico de Lamport para ordenação de eventos distribuídos.
type LogicalClock struct {
	mu       sync.Mutex
	time     int64  // Contador lógico de Lamport
	serverID string // Identificador único deste servidor no consórcio
}

// NewLogicalClock cria uma nova instância do relógio lógico para o servidor.
func NewLogicalClock(serverID string) *LogicalClock {
	return &LogicalClock{
		time:     0,
		serverID: serverID,
	}
}

// Tick incrementa o relógio localmente (eventos internos do servidor).
func (c *LogicalClock) Tick() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.time++
	return c.time
}

// Update atualiza o relógio ao receber uma mensagem de outro servidor (regra de Lamport).
// Relógio_local = max(Relógio_local, Relógio_recebido) + 1
func (c *LogicalClock) Update(remoteTime int64) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if remoteTime > c.time {
		c.time = remoteTime
	}
	c.time++
	return c.time
}

// GetCurrent retorna o valor atual do relógio sem o incrementar.
func (c *LogicalClock) GetCurrent() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.time
}

// RequestItem representa uma tarefa ou requisição enfileirada para processamento sequencial.
type RequestItem struct {
	Timestamp int64        // Timestamp do Relógio de Lamport (da transação, quando vem de outro nó)
	ServerID  string       // ID do servidor que originou a transação (desempate)
	Task      func() error // Função a ser executada pelo worker
	Response  chan error   // Canal para devolver o resultado da execução ao chamador
	index     int          // Índice interno exigido pela interface heap.Interface do Go
}

// PriorityQueue implementa uma Min-Heap baseada na tupla (Timestamp, ServerID).
type PriorityQueue []*RequestItem

func (pq PriorityQueue) Len() int { return len(pq) }

func (pq PriorityQueue) Less(i, j int) bool {
	// Critério 1: menor timestamp de Lamport tem prioridade
	if pq[i].Timestamp == pq[j].Timestamp {
		// Critério 2 (desempate): menor ServerID lexicograficamente
		return pq[i].ServerID < pq[j].ServerID
	}
	return pq[i].Timestamp < pq[j].Timestamp
}

func (pq PriorityQueue) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
	pq[i].index = i
	pq[j].index = j
}

func (pq *PriorityQueue) Push(x interface{}) {
	n := len(*pq)
	item := x.(*RequestItem)
	item.index = n
	*pq = append(*pq, item)
}

func (pq *PriorityQueue) Pop() interface{} {
	old := *pq
	n := len(old)
	item := old[n-1]
	old[n-1] = nil  // Evita vazamento de memória
	item.index = -1 // Para segurança
	*pq = old[0 : n-1]
	return item
}

// RequestManager coordena a fila de prioridades e garante que apenas um Single Worker
// processa as requisições de forma estritamente sequencial.
type RequestManager struct {
	mu    sync.Mutex
	pq    PriorityQueue
	cond  *sync.Cond
	clock *LogicalClock
}

// NewRequestManager inicializa o gestor de requisições e dispara o Single Worker.
func NewRequestManager(clock *LogicalClock) *RequestManager {
	rm := &RequestManager{
		pq:    make(PriorityQueue, 0),
		clock: clock,
	}
	rm.cond = sync.NewCond(&rm.mu)
	heap.Init(&rm.pq)

	// Inicia a única goroutine (Single Worker) que consome a fila em segundo plano
	go rm.workerLoop()

	return rm
}

// NextTimestamp gera o timestamp de Lamport de uma nova transação iniciada por este nó.
// O coordenador envia esse valor nos prepares para que todos os participantes usem a mesma precedência.
func (rm *RequestManager) NextTimestamp() int64 {
	return rm.clock.Tick()
}

// Submit enfileira uma tarefa local (ações do próprio nó) e aguarda o Single Worker executá-la.
// O timestamp é gerado no momento da submissão.
func (rm *RequestManager) Submit(task func() error) error {
	rm.mu.Lock()

	ts := rm.clock.Tick()
	serverID := rm.clock.serverID

	responseChan := make(chan error, 1)

	item := &RequestItem{
		Timestamp: ts,
		ServerID:  serverID,
		Task:      task,
		Response:  responseChan,
	}

	heap.Push(&rm.pq, item)
	rm.cond.Signal()
	rm.mu.Unlock()

	return <-responseChan
}

// SubmitAt enfileira uma tarefa usando o timestamp de Lamport de uma transação recebida de outro nó.
// O relógio local é atualizado com a regra de Lamport, mas a ordem na fila usa (remoteTS, originID),
// assim todos os nós ordenam a mesma transação da mesma forma.
func (rm *RequestManager) SubmitAt(remoteTS int64, originID string, task func() error) error {
	rm.mu.Lock()

	rm.clock.Update(remoteTS)

	responseChan := make(chan error, 1)

	item := &RequestItem{
		Timestamp: remoteTS,
		ServerID:  originID,
		Task:      task,
		Response:  responseChan,
	}

	heap.Push(&rm.pq, item)
	rm.cond.Signal()
	rm.mu.Unlock()

	return <-responseChan
}

// workerLoop é a goroutine única que retira tarefas da fila de prioridade e as executa em ordem.
func (rm *RequestManager) workerLoop() {
	for {
		rm.mu.Lock()
		for len(rm.pq) == 0 {
			rm.cond.Wait()
		}

		// Retira o item de maior prioridade (menor timestamp, depois menor ServerID)
		item := heap.Pop(&rm.pq).(*RequestItem)
		rm.mu.Unlock()

		// Executa a tarefa de forma isolada e sequencial
		err := item.Task()

		// Devolve o resultado ao chamador
		item.Response <- err
	}
}
