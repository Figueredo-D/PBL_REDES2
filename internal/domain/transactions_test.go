package domain

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLamportPriorityOrdering(t *testing.T) {
	// Menor timestamp tem prioridade
	if !HasHigherPriority(5, "SERVER_B", 10, "SERVER_A") {
		t.Errorf("Esperava que Lamport 5 tivesse prioridade sobre Lamport 10")
	}
	if HasHigherPriority(10, "SERVER_A", 5, "SERVER_B") {
		t.Errorf("Não esperava que Lamport 10 tivesse prioridade sobre Lamport 5")
	}

	// Desempate por CoordinatorID quando timestamps empatam
	if !HasHigherPriority(5, "SERVER_A", 5, "SERVER_B") {
		t.Errorf("Esperava que SERVER_A vencesse SERVER_B em caso de empate")
	}
	if HasHigherPriority(5, "SERVER_B", 5, "SERVER_A") {
		t.Errorf("Não esperava que SERVER_B vencesse SERVER_A em caso de empate")
	}
}

func TestLamportSeatContentionAndPreemption(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "txlog_test_*")
	if err != nil {
		t.Fatalf("Erro ao criar dir temporário: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	txLogPath := filepath.Join(tmpDir, "txlog.json")
	txLog, err := LoadTxLog(txLogPath)
	if err != nil {
		t.Fatalf("Erro ao carregar txlog: %v", err)
	}

	repo := NewRideRepository("SERVER_A")
	ride, err := NewRide("SERVER_A_RIDE_0001", "SERVER_A", "driver1", []string{"Salvador", "Feira"}, "2026-11-20", "08:00", 1, []float64{30})
	if err != nil {
		t.Fatalf("Erro ao criar carona: %v", err)
	}
	repo.AddWithID(ride)

	seg := SegmentRef{RideID: "SERVER_A_RIDE_0001", From: "Salvador", To: "Feira"}

	// 1. TX1 (Lamport 10, SERVER_B) chega primeiro e reserva a única vaga
	err = txLog.PrepareWithPriority(repo, "TX_1", "SERVER_B", 10, []SegmentRef{seg})
	if err != nil {
		t.Fatalf("TX1 falhou ao reservar: %v", err)
	}

	// Vaga local deve estar esgotada
	if ride.Segments[0].AvailableSeats != 0 {
		t.Fatalf("Esperava 0 vagas disponíveis após TX1, obteve: %d", ride.Segments[0].AvailableSeats)
	}

	// 2. TX2 (Lamport 5, SERVER_A) chega com timestamp menor (MAIOR prioridade).
	// Deve preemptar TX1 e assumir a vaga.
	err = txLog.PrepareWithPriority(repo, "TX_2", "SERVER_A", 5, []SegmentRef{seg})
	if err != nil {
		t.Fatalf("TX2 deveria ter preemptado TX1, mas falhou: %v", err)
	}

	// TX1 deve estar marcada como abortada
	if !txLog.aborted["TX_1"] {
		t.Errorf("TX1 deveria estar marcada como abortada após preempção")
	}

	// TX2 deve estar em pending
	if _, ok := txLog.pending["TX_2"]; !ok {
		t.Errorf("TX2 deveria estar na lista de pendentes")
	}

	// 3. TX3 (Lamport 15, SERVER_C) chega com MENOR prioridade que TX2.
	// Deve ser rejeitada com ErrLowerPriority
	err = txLog.PrepareWithPriority(repo, "TX_3", "SERVER_C", 15, []SegmentRef{seg})
	if !errors.Is(err, ErrLowerPriority) {
		t.Fatalf("Esperava ErrLowerPriority para TX3, obteve: %v", err)
	}

	// 4. Commit de TX2 deve funcionar
	err = txLog.Commit("TX_2")
	if err != nil {
		t.Fatalf("Commit de TX2 falhou: %v", err)
	}

	// 5. Tentativa tardia de commit de TX1 (preemptada) deve falhar
	err = txLog.Commit("TX_1")
	if err == nil {
		t.Fatalf("Commit de TX1 deveria ter falhado pois ela foi abortada por precedência")
	}
}

