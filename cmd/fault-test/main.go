package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Figueredo-D/PBL_REDES/internal/domain"
	"github.com/Figueredo-D/PBL_REDES/internal/protocol"
	"github.com/Figueredo-D/PBL_REDES/internal/server"
)

type faultTestClient struct {
	addr   string
	conn   net.Conn
	reader *bufio.Reader
}

func dialClient(addr string, timeout time.Duration) (*faultTestClient, error) {
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, err
	}
	return &faultTestClient{
		addr:   addr,
		conn:   conn,
		reader: bufio.NewReader(conn),
	}, nil
}

func (c *faultTestClient) Close() {
	if c.conn != nil {
		_ = c.conn.Close()
	}
}

func (c *faultTestClient) Send(action, token string, payload interface{}, timeout time.Duration) (*protocol.Response, error) {
	var payloadStr string
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		payloadStr = string(b)
	}

	req := protocol.Request{Action: action, Token: token, Data: payloadStr}
	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')

	_ = c.conn.SetDeadline(time.Now().Add(timeout))
	if _, err := c.conn.Write(data); err != nil {
		return nil, err
	}

	line, err := c.reader.ReadBytes('\n')
	if err != nil {
		return nil, err
	}

	var resp protocol.Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func main() {
	flagSeats := flag.Int("seats", 10, "Total inicial de assentos por trecho")
	flagWorkers := flag.Int("workers", 15, "Quantidade de passageiros concorrentes")
	flag.Parse()

	fmt.Println("================================================================")
	fmt.Println("   TESTE AUTOMATIZADO SOB CARGA COM INJEÇÃO DE FALHAS (2PC)    ")
	fmt.Println("================================================================")
	fmt.Println("Objetivo: Comprovar os 3 invariantes de segurança do Problema 2:")
	fmt.Println("  1. Nenhum assento confirmado para mais de um passageiro (Zero Overbooking)")
	fmt.Println("  2. Nenhum itinerário confirmado pela metade (Atomicidade Tudo-ou-Nada)")
	fmt.Println("  3. Nenhum assento bloqueado após queda e recuperação do servidor")
	fmt.Println("----------------------------------------------------------------")

	// Cria diretório temporário para dados isolados do teste
	tempDir, err := os.MkdirTemp("", "vai_junto_fault_test_*")
	if err != nil {
		fmt.Printf("[ERRO] Falha ao criar diretório temporário: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tempDir)

	dataDirA := filepath.Join(tempDir, "SERVER_A")
	dataDirB := filepath.Join(tempDir, "SERVER_B")
	dataDirC := filepath.Join(tempDir, "SERVER_C")

	_ = os.MkdirAll(dataDirA, 0o755)
	_ = os.MkdirAll(dataDirB, 0o755)
	_ = os.MkdirAll(dataDirC, 0o755)

	tcpA, restA := "127.0.0.1:8080", "127.0.0.1:9080"
	tcpB, restB := "127.0.0.1:8081", "127.0.0.1:9081"
	tcpC, restC := "127.0.0.1:8082", "127.0.0.1:9082"

	peersA := map[string]string{
		"SERVER_B": "http://" + restB,
		"SERVER_C": "http://" + restC,
	}
	peersB := map[string]string{
		"SERVER_A": "http://" + restA,
		"SERVER_C": "http://" + restC,
	}
	peersC := map[string]string{
		"SERVER_A": "http://" + restA,
		"SERVER_B": "http://" + restB,
	}

	fmt.Println("[INICIALIZAÇÃO] Subindo instâncias dos servidores A, B e C...")

	srvA, err := server.NewServer(tcpA, "SERVER_A", peersA, filepath.Join(dataDirA, "estado.json"))
	if err != nil {
		logFatal("SERVER_A", err)
	}
	_ = srvA.StartInternalHTTP(restA)
	srvA.StartRecovery()
	go func() { _ = srvA.Start() }()

	srvB, err := server.NewServer(tcpB, "SERVER_B", peersB, filepath.Join(dataDirB, "estado.json"))
	if err != nil {
		logFatal("SERVER_B", err)
	}
	_ = srvB.StartInternalHTTP(restB)
	srvB.StartRecovery()
	go func() { _ = srvB.Start() }()

	srvC, err := server.NewServer(tcpC, "SERVER_C", peersC, filepath.Join(dataDirC, "estado.json"))
	if err != nil {
		logFatal("SERVER_C", err)
	}
	_ = srvC.StartInternalHTTP(restC)
	srvC.StartRecovery()
	go func() { _ = srvC.Start() }()

	time.Sleep(300 * time.Millisecond)

	// 1. Setup de Caronas Interligadas
	date := time.Now().AddDate(0, 0, 20).Format("2006-01-02")
	fmt.Printf("[SETUP] Ofertando caronas com %d assentos em cada empresa na data %s...\n", *flagSeats, date)

	cA, err := dialClient(tcpA, 3*time.Second)
	if err != nil {
		logFatal("Conectando em A", err)
	}
	defer cA.Close()

	cB, err := dialClient(tcpB, 3*time.Second)
	if err != nil {
		logFatal("Conectando em B", err)
	}
	defer cB.Close()

	cC, err := dialClient(tcpC, 3*time.Second)
	if err != nil {
		logFatal("Conectando em C", err)
	}
	defer cC.Close()

	// A: Salvador -> Feira
	respA, err := cA.Send(protocol.ActionOfferRide, "driver_A", protocol.OfferRideData{
		Route:         []string{"Salvador", "Feira"},
		Date:          date,
		Time:          "08:00",
		TotalSeats:    *flagSeats,
		SegmentPrices: []float64{30},
	}, 3*time.Second)
	if err != nil || respA.Status != "SUCCESS" {
		logFatal("Oferta A", err)
	}

	// B: Feira -> Conquista
	respB, err := cB.Send(protocol.ActionOfferRide, "driver_B", protocol.OfferRideData{
		Route:         []string{"Feira", "Conquista"},
		Date:          date,
		Time:          "10:00",
		TotalSeats:    *flagSeats,
		SegmentPrices: []float64{40},
	}, 3*time.Second)
	if err != nil || respB.Status != "SUCCESS" {
		logFatal("Oferta B", err)
	}

	// C: Conquista -> Montes Claros
	respC, err := cC.Send(protocol.ActionOfferRide, "driver_C", protocol.OfferRideData{
		Route:         []string{"Conquista", "Montes Claros"},
		Date:          date,
		Time:          "14:00",
		TotalSeats:    *flagSeats,
		SegmentPrices: []float64{50},
	}, 3*time.Second)
	if err != nil || respC.Status != "SUCCESS" {
		logFatal("Oferta C", err)
	}

	fmt.Println("  ✔ Caronas registradas. Itinerário combinado: Salvador -> Feira -> Conquista -> Montes Claros (A + B + C).")

	// 2. Busca do Itinerário Combinado pelo Coordenador (Servidor A)
	searchResp, err := cA.Send(protocol.ActionSearchItinerary, "passageiro_teste", protocol.SearchItineraryData{
		Origin:      "Salvador",
		Destination: "Montes Claros",
		Date:        date,
	}, 5*time.Second)
	if err != nil || searchResp.Status != "SUCCESS" {
		logFatal("Busca de itinerário multi-empresa", err)
	}

	var itineraries []domain.ItineraryResult
	if err := json.Unmarshal([]byte(searchResp.Data), &itineraries); err != nil || len(itineraries) == 0 {
		logFatal("Parsing do itinerário combinado", err)
	}
	fullItin := itineraries[0]
	fmt.Printf("  ✔ Itinerário multi-empresa descoberto com %d trechos.\n", len(fullItin.Segments))

	// 3. Disparo de Reservas Concorrentes e Injeção de Queda no Servidor B
	fmt.Println("\n[EXECUÇÃO] Disparando reservas simultâneas e INJETANDO QUEDA do Servidor B...")

	var successfulBookings int64
	var failedBookings int64

	var wg sync.WaitGroup
	wg.Add(*flagWorkers)

	for i := 1; i <= *flagWorkers; i++ {
		go func(workerID int) {
			defer wg.Done()

			token := fmt.Sprintf("pass_%03d", workerID)
			cli, err := dialClient(tcpA, 6*time.Second)
			if err != nil {
				atomic.AddInt64(&failedBookings, 1)
				return
			}
			defer cli.Close()

			// Envia reserva do itinerário multi-empresa
			resp, err := cli.Send(protocol.ActionBookItinerary, token, fullItin, 6*time.Second)
			if err == nil && resp.Status == "SUCCESS" {
				atomic.AddInt64(&successfulBookings, 1)
			} else {
				atomic.AddInt64(&failedBookings, 1)
			}
		}(i)

		// No meio das requisições simultâneas, DERRUBA o Servidor B!
		if i == *flagWorkers/2 {
			go func() {
				time.Sleep(10 * time.Millisecond)
				fmt.Println("\n  💥 [INJEÇÃO DE FALHA] CRASH NO SERVIDOR B! Parando porta TCP e API REST interna...")
				srvB.Stop()
				fmt.Println("  💥 [INJEÇÃO DE FALHA] Servidor B agora está COMPLETAMENTE INOPERANTE.")
			}()
		}
	}

	wg.Wait()
	fmt.Printf("[EXECUÇÃO] Rodada sob falha concluída: %d confirmadas, %d falharam/recusadas.\n",
		atomic.LoadInt64(&successfulBookings), atomic.LoadInt64(&failedBookings))

	// 4. Teste de Liveness dos nós sobreviventes enquanto B está fora
	fmt.Println("\n[LIVENESS] Testando se o Servidor A continua operando normalmente para caronas locais...")
	localResp, err := cA.Send(protocol.ActionSearchItinerary, "passageiro_local", protocol.SearchItineraryData{
		Origin:      "Salvador",
		Destination: "Feira",
		Date:        date,
	}, 3*time.Second)
	if err == nil && localResp.Status == "SUCCESS" {
		fmt.Println("  ✔ PASSOU: Servidor A continua atendendo requisições independentes do nó que falhou!")
	} else {
		fmt.Println("  ✖ FALHA: Servidor A travou com a queda de B!")
	}

	// 5. Recuperação do Servidor B
	fmt.Println("\n[RECUPERAÇÃO] Reiniciando Servidor B (carregando estado e txlog em disco)...")
	srvBRec, err := server.NewServer(tcpB, "SERVER_B", peersB, filepath.Join(dataDirB, "estado.json"))
	if err != nil {
		logFatal("Recuperação do SERVER_B", err)
	}
	_ = srvBRec.StartInternalHTTP(restB)
	srvBRec.StartRecovery()
	go func() { _ = srvBRec.Start() }()

	fmt.Println("  ✔ Servidor B reiniciado. Aguardando reconciliação da thread de recovery...")
	time.Sleep(2 * time.Second) // Aguarda a thread periódica de recuperação rodar

	// 6. VALIDAÇÃO DOS 3 INVARIANTES DO EDITAL
	fmt.Println("\n================================================================")
	fmt.Println("               VERIFICAÇÃO FORMAL DOS INVARIANTES              ")
	fmt.Println("================================================================")

	allPass := true

	// Invariante 1: Zero Overbooking
	fmt.Println("1. Verificando Ausência de Overbooking (Invariante 1)...")
	seatsA := getAvailableSeats("http://"+restA, date, "Salvador", "Feira")
	seatsB := getAvailableSeats("http://"+restB, date, "Feira", "Conquista")
	seatsC := getAvailableSeats("http://"+restC, date, "Conquista", "Montes Claros")

	fmt.Printf("   • Vagas restantes: A=%d, B=%d, C=%d (Iniciais: %d)\n", seatsA, seatsB, seatsC, *flagSeats)

	if seatsA < 0 || seatsB < 0 || seatsC < 0 {
		fmt.Println("   ✖ FALHA: Detectada quantidade negativa de vagas (OVERBOOKING)!")
		allPass = false
	} else {
		fmt.Println("   ✔ PASSOU: Nenhum assento foi confirmado para mais de um passageiro!")
	}

	// Invariante 2: Zero Itinerário pela Metade (Atomicidade 2PC)
	fmt.Println("\n2. Verificando Atomicidade do Itinerário (Invariante 2)...")
	// Como o itinerário reservado envolvia trechos de A, B e C igualmente,
	// o número de vagas consumidas em A, B e C para esse itinerário DEVE ser rigorosamente idêntico!
	consumedA := *flagSeats - seatsA
	consumedB := *flagSeats - seatsB
	consumedC := *flagSeats - seatsC

	fmt.Printf("   • Vagas consumidas pelo itinerário multi-empresa: A=%d, B=%d, C=%d\n",
		consumedA, consumedB, consumedC)

	if consumedA != consumedB || consumedB != consumedC {
		fmt.Printf("   ✖ FALHA DE ATOMICIDADE: Itinerário foi reservado pela metade! (A:%d, B:%d, C:%d)\n",
			consumedA, consumedB, consumedC)
		allPass = false
	} else {
		fmt.Printf("   ✔ PASSOU: Atomicidade 2PC respeitada! Exatamente %d reservas foram confirmadas por completo em todos os nós!\n", consumedA)
	}

	// Invariante 3: Zero Assentos Bloqueados após Queda e Recuperação
	fmt.Println("\n3. Verificando Liberação de Assentos Bloqueados pós-Recuperação (Invariante 3)...")
	txLogBPath := filepath.Join(dataDirB, "txlog.json")
	txB, err := domain.LoadTxLog(txLogBPath)
	if err != nil {
		fmt.Printf("   ✖ Erro ao inspecionar txlog de B: %v\n", err)
		allPass = false
	} else {
		pendingB := txB.PendingTransactions()
		if len(pendingB) > 0 {
			fmt.Printf("   ✖ FALHA: Servidor B ainda possui %d transações bloqueadas em PREPARED!\n", len(pendingB))
			allPass = false
		} else {
			fmt.Println("   ✔ PASSOU: Todas as transações pendentes foram resolvidas pelo Recovery! Zero assentos bloqueados.")
		}
	}

	// Fechamento limpo
	srvA.Stop()
	srvBRec.Stop()
	srvC.Stop()

	fmt.Println("\n----------------------------------------------------------------")
	if allPass {
		fmt.Println("RESULTADO FINAL: TODOS OS INVARIANTES DO EDITAL FORAM CONFIRMADOS COM SUCESSO! 🎉")
		fmt.Println("================================================================")
		os.Exit(0)
	} else {
		fmt.Println("RESULTADO FINAL: VIOLAÇÃO DE INVARIANTES DETECTADA!")
		fmt.Println("================================================================")
		os.Exit(1)
	}
}

func getAvailableSeats(baseURL, date, from, to string) int {
	endpoint := fmt.Sprintf("%s/internal/rides?date=%s", baseURL, url.QueryEscape(date))
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(endpoint)
	if err != nil {
		return -999
	}
	defer resp.Body.Close()

	var rides []struct {
		Segments []struct {
			From           string `json:"from"`
			To             string `json:"to"`
			AvailableSeats int    `json:"available_seats"`
		} `json:"segments"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&rides); err != nil {
		return -999
	}

	for _, r := range rides {
		for _, s := range r.Segments {
			if s.From == from && s.To == to {
				return s.AvailableSeats
			}
		}
	}
	return -999
}

func logFatal(context string, err error) {
	fmt.Printf("\n[FATAL - %s]: %v\n", context, err)
	os.Exit(1)
}
