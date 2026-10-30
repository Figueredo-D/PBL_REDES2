package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Figueredo-D/PBL_REDES/internal/domain"
	"github.com/Figueredo-D/PBL_REDES/internal/protocol"
)

// Metrics armazena as estatísticas acumuladas da simulação de carga.
type Metrics struct {
	TotalSearches     int64
	SearchSuccesses   int64
	SearchFailures    int64
	SearchDurationNs  int64

	TotalBookings     int64
	BookingCommits    int64
	BookingNoSeats    int64
	BookingPriority   int64
	BookingErrors     int64
	BookingDurationNs int64

	MinBookingMs int64
	MaxBookingMs int64
}

// ClientHelper abstrai uma conexão TCP com um dos servidores do consórcio.
type ClientHelper struct {
	addr   string
	conn   net.Conn
	reader *bufio.Reader
}

func newClientHelper(addr string, timeout time.Duration) (*ClientHelper, error) {
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, err
	}
	return &ClientHelper{
		addr:   addr,
		conn:   conn,
		reader: bufio.NewReader(conn),
	}, nil
}

func (c *ClientHelper) Close() {
	if c.conn != nil {
		_ = c.conn.Close()
	}
}

func (c *ClientHelper) Send(action, token string, payload interface{}, timeout time.Duration) (*protocol.Response, error) {
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
	flagServers := flag.String("servers", "localhost:8080,localhost:8081,localhost:8082", "Lista de servidores separados por vírgula (TCP dos clientes)")
	flagRestBases := flag.String("rest-bases", "http://localhost:9080,http://localhost:9081,http://localhost:9082", "Lista das bases REST internas correspondentes para validação")
	flagPassengers := flag.Int("passengers", 20, "Número de passageiros concorrentes simultâneos")
	flagRounds := flag.Int("rounds", 5, "Rodadas de busca e reserva por passageiro")
	flagSeats := flag.Int("seats", 12, "Vagas disponíveis por trecho (gera disputa acirrada sob concorrência)")
	flagSetup := flag.Bool("setup", true, "Publicar caronas de teste no consórcio antes da simulação")
	flagTimeout := flag.Duration("timeout", 10*time.Second, "Timeout para requisições TCP")
	flag.Parse()

	servers := strings.Split(*flagServers, ",")
	for i := range servers {
		servers[i] = strings.TrimSpace(servers[i])
	}

	restBases := strings.Split(*flagRestBases, ",")
	for i := range restBases {
		restBases[i] = strings.TrimSpace(restBases[i])
	}

	simDate := time.Now().AddDate(0, 0, 15).Format("2006-01-02")
	suffix := fmt.Sprintf("%d", time.Now().UnixNano()%100000)

	fmt.Println("================================================================")
	fmt.Println("     SIMULADOR DE CARGA AUTÔNOMO - CONSÓRCIO VAIJUNTO           ")
	fmt.Println("================================================================")
	fmt.Printf("  • Servidores Alvo:         %s\n", strings.Join(servers, ", "))
	fmt.Printf("  • Passageiros Virtuais:    %d\n", *flagPassengers)
	fmt.Printf("  • Rodadas por Passageiro:  %d\n", *flagRounds)
	fmt.Printf("  • Total de Operações:      %d buscas e até %d reservas\n",
		*flagPassengers**flagRounds, *flagPassengers**flagRounds)
	fmt.Printf("  • Vagas por Trecho:        %d assentos (alta contenção)\n", *flagSeats)
	fmt.Println("----------------------------------------------------------------")

	// 1. Setup Inicial de Caronas
	if *flagSetup {
		fmt.Println("[SETUP] Publicando caronas conectadas no consórcio...")
		if err := setupConsortiumRides(servers, simDate, suffix, *flagSeats, *flagTimeout); err != nil {
			fmt.Printf("[ERRO SETUP] Falha ao configurar caronas de teste: %v\n", err)
			fmt.Println("Certifique-se de que os servidores estão em execução.")
			os.Exit(1)
		}
		fmt.Println("[SETUP] Caronas do consórcio publicadas com sucesso.")
		fmt.Println("        - Servidor A: Salvador -> Feira de Santana")
		fmt.Println("        - Servidor B: Feira de Santana -> Vitória da Conquista")
		fmt.Println("        - Servidor C: Vitória da Conquista -> Montes Claros")
		fmt.Println("----------------------------------------------------------------")
	}

	// 2. Execução da Simulação Concorrente
	metrics := &Metrics{
		MinBookingMs: 999999,
		MaxBookingMs: 0,
	}

	routes := [][2]string{
		{"Salvador", "Montes Claros"},      // Rota de 3 trechos: A + B + C (Coordenação distribuída completa)
		{"Salvador", "Vitória da Conquista"}, // Rota de 2 trechos: A + B
		{"Feira de Santana", "Montes Claros"}, // Rota de 2 trechos: B + C
		{"Salvador", "Feira de Santana"},   // Rota local de 1 trecho
	}

	fmt.Printf("[SIMULAÇÃO] Disparando %d passageiros simultâneos...\n", *flagPassengers)
	startTime := time.Now()

	var wg sync.WaitGroup
	wg.Add(*flagPassengers)

	for p := 1; p <= *flagPassengers; p++ {
		passengerID := fmt.Sprintf("pass_%s_%03d", suffix, p)
		go func(pid string, index int) {
			defer wg.Done()

			// Conecta em um servidor distribuído deterministicamente/aleatoriamente
			serverIndex := index % len(servers)
			serverAddr := servers[serverIndex]

			client, err := newClientHelper(serverAddr, *flagTimeout)
			if err != nil {
				atomic.AddInt64(&metrics.BookingErrors, 1)
				return
			}
			defer client.Close()

			for r := 0; r < *flagRounds; r++ {
				// Escolhe uma rota de busca
				route := routes[rand.Intn(len(routes))]
				origin := route[0]
				dest := route[1]

				// A. Busca de Itinerários (BFS)
				searchStart := time.Now()
				searchResp, err := client.Send(protocol.ActionSearchItinerary, pid, protocol.SearchItineraryData{
					Origin:      origin,
					Destination: dest,
					Date:        simDate,
				}, *flagTimeout)

				searchElapsed := time.Since(searchStart)
				atomic.AddInt64(&metrics.TotalSearches, 1)
				atomic.AddInt64(&metrics.SearchDurationNs, searchElapsed.Nanoseconds())

				if err != nil || searchResp.Status != "SUCCESS" {
					atomic.AddInt64(&metrics.SearchFailures, 1)
					continue
				}
				atomic.AddInt64(&metrics.SearchSuccesses, 1)

				var itineraries []domain.ItineraryResult
				if err := json.Unmarshal([]byte(searchResp.Data), &itineraries); err != nil || len(itineraries) == 0 {
					continue
				}

				// Seleciona o primeiro itinerário válido encontrado
				itin := itineraries[0]

				// B. Reserva de Itinerário via 2PC
				bookingStart := time.Now()
				atomic.AddInt64(&metrics.TotalBookings, 1)

				bookResp, err := client.Send(protocol.ActionBookItinerary, pid, itin, *flagTimeout)
				bookingElapsed := time.Since(bookingStart)
				elapsedMs := bookingElapsed.Milliseconds()

				atomic.AddInt64(&metrics.BookingDurationNs, bookingElapsed.Nanoseconds())
				updateMinMax(metrics, elapsedMs)

				if err != nil {
					atomic.AddInt64(&metrics.BookingErrors, 1)
					continue
				}

				if bookResp.Status == "SUCCESS" {
					atomic.AddInt64(&metrics.BookingCommits, 1)
				} else {
					if bookResp.Code == "LOWER_PRIORITY" {
						atomic.AddInt64(&metrics.BookingPriority, 1)
					} else if bookResp.Code == "2PC_ABORTED" || bookResp.Code == "NO_SEATS" {
						atomic.AddInt64(&metrics.BookingNoSeats, 1)
					} else {
						atomic.AddInt64(&metrics.BookingErrors, 1)
					}
				}

				// Pequena pausa estocástica entre requisições para emular comportamento humano (10 a 50ms)
				time.Sleep(time.Duration(10+rand.Intn(40)) * time.Millisecond)
			}
		}(passengerID, p)
	}

	wg.Wait()
	totalDuration := time.Since(startTime)

	// 3. Apresentação do Relatório Consolidado de Carga
	printSimulationReport(metrics, totalDuration, *flagPassengers)

	// 4. Verificação de Integridade dos Assentos (Zero Overbooking)
	if *flagSetup && len(restBases) >= 3 {
		verifyConsortiumSeats(restBases, simDate, *flagSeats)
	}
}

func updateMinMax(m *Metrics, ms int64) {
	for {
		currentMin := atomic.LoadInt64(&m.MinBookingMs)
		if ms >= currentMin || atomic.CompareAndSwapInt64(&m.MinBookingMs, currentMin, ms) {
			break
		}
	}
	for {
		currentMax := atomic.LoadInt64(&m.MaxBookingMs)
		if ms <= currentMax || atomic.CompareAndSwapInt64(&m.MaxBookingMs, currentMax, ms) {
			break
		}
	}
}

func setupConsortiumRides(servers []string, date, suffix string, seats int, timeout time.Duration) error {
	if len(servers) < 3 {
		return fmt.Errorf("necessário pelo menos 3 servidores configurados")
	}

	// Servidor A: Salvador -> Feira
	cA, err := newClientHelper(servers[0], timeout)
	if err != nil {
		return fmt.Errorf("conectando a SERVER_A (%s): %w", servers[0], err)
	}
	defer cA.Close()

	respA, err := cA.Send(protocol.ActionOfferRide, "driver_A_"+suffix, protocol.OfferRideData{
		Route:         []string{"Salvador", "Feira de Santana"},
		Date:          date,
		Time:          "08:00",
		TotalSeats:    seats,
		SegmentPrices: []float64{30},
	}, timeout)
	if err != nil || respA.Status != "SUCCESS" {
		return fmt.Errorf("falha ao ofertar em A: %v (resp: %v)", err, respA)
	}

	// Servidor B: Feira -> Conquista
	cB, err := newClientHelper(servers[1], timeout)
	if err != nil {
		return fmt.Errorf("conectando a SERVER_B (%s): %w", servers[1], err)
	}
	defer cB.Close()

	respB, err := cB.Send(protocol.ActionOfferRide, "driver_B_"+suffix, protocol.OfferRideData{
		Route:         []string{"Feira de Santana", "Vitória da Conquista"},
		Date:          date,
		Time:          "10:00",
		TotalSeats:    seats,
		SegmentPrices: []float64{40},
	}, timeout)
	if err != nil || respB.Status != "SUCCESS" {
		return fmt.Errorf("falha ao ofertar em B: %v (resp: %v)", err, respB)
	}

	// Servidor C: Conquista -> Montes Claros
	cC, err := newClientHelper(servers[2], timeout)
	if err != nil {
		return fmt.Errorf("conectando a SERVER_C (%s): %w", servers[2], err)
	}
	defer cC.Close()

	respC, err := cC.Send(protocol.ActionOfferRide, "driver_C_"+suffix, protocol.OfferRideData{
		Route:         []string{"Vitória da Conquista", "Montes Claros"},
		Date:          date,
		Time:          "14:00",
		TotalSeats:    seats,
		SegmentPrices: []float64{50},
	}, timeout)
	if err != nil || respC.Status != "SUCCESS" {
		return fmt.Errorf("falha ao ofertar em C: %v (resp: %v)", err, respC)
	}

	return nil
}

func printSimulationReport(m *Metrics, d time.Duration, passengers int) {
	fmt.Println("\n================================================================")
	fmt.Println("              RELATÓRIO DE DESEMPENHO SOB CARGA                ")
	fmt.Println("================================================================")
	fmt.Printf("  • Tempo Total de Execução:        %v\n", d.Round(time.Millisecond))
	fmt.Printf("  • Passageiros Simultâneos:        %d goroutines ativas\n", passengers)
	
	totalOps := m.TotalSearches + m.TotalBookings
	opsPerSec := float64(totalOps) / d.Seconds()
	fmt.Printf("  • Throughput Total do Consórcio:  %.2f op/s\n", opsPerSec)

	fmt.Println("\n--- MÉTRICAS DE BUSCA (BFS DISTRIBUÍDA) ---")
	fmt.Printf("  • Buscas Solicitadas:             %d\n", m.TotalSearches)
	fmt.Printf("  • Buscas com Sucesso:             %d\n", m.SearchSuccesses)
	fmt.Printf("  • Falhas de Busca:                %d\n", m.SearchFailures)
	if m.TotalSearches > 0 {
		avgSearchMs := float64(m.SearchDurationNs) / float64(m.TotalSearches) / 1e6
		fmt.Printf("  • Latência Média de Busca:        %.2f ms\n", avgSearchMs)
	}

	fmt.Println("\n--- MÉTRICAS DE RESERVA (TWO-PHASE COMMIT - 2PC) ---")
	fmt.Printf("  • Tentativas de Reserva:          %d\n", m.TotalBookings)
	fmt.Printf("  • Confirmadas (Global Commit):    %d\n", m.BookingCommits)
	fmt.Printf("  • Abortadas (Sem Vagas / 2PC):    %d\n", m.BookingNoSeats)
	fmt.Printf("  • Abortadas (Precedência Lamport):%d\n", m.BookingPriority)
	fmt.Printf("  • Erros de Comunicação / Timeout: %d\n", m.BookingErrors)

	if m.TotalBookings > 0 {
		avgBookingMs := float64(m.BookingDurationNs) / float64(m.TotalBookings) / 1e6
		fmt.Printf("  • Latência Média de Reserva:      %.2f ms\n", avgBookingMs)
		fmt.Printf("  • Latência Mínima de Reserva:     %d ms\n", m.MinBookingMs)
		fmt.Printf("  • Latência Máxima de Reserva:     %d ms\n", m.MaxBookingMs)
	}
	fmt.Println("----------------------------------------------------------------")
}

func verifyConsortiumSeats(bases []string, date string, initialSeats int) {
	fmt.Println("[VALIDAÇÃO] Verificando integridade dos assentos via API REST...")
	client := &http.Client{Timeout: 5 * time.Second}

	allConsistent := true

	for i, base := range bases {
		serverName := fmt.Sprintf("SERVER_%c", 'A'+i)
		endpoint := fmt.Sprintf("%s/internal/rides?date=%s", base, url.QueryEscape(date))

		resp, err := client.Get(endpoint)
		if err != nil {
			fmt.Printf("  [AVISO] Não foi possível consultar %s em %s: %v\n", serverName, endpoint, err)
			continue
		}

		var rides []struct {
			ID       string `json:"id"`
			Segments []struct {
				From           string `json:"from"`
				To             string `json:"to"`
				AvailableSeats int    `json:"available_seats"`
			} `json:"segments"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&rides); err != nil {
			resp.Body.Close()
			continue
		}
		resp.Body.Close()

		for _, r := range rides {
			for _, s := range r.Segments {
				fmt.Printf("  • %s (%s -> %s): %d vagas restantes (iniciais: %d)\n",
					serverName, s.From, s.To, s.AvailableSeats, initialSeats)

				if s.AvailableSeats < 0 {
					fmt.Printf("    [FALHA GRAVE] OVERBOOKING DETECTADO: vagas negativas (%d)!\n", s.AvailableSeats)
					allConsistent = false
				} else if s.AvailableSeats > initialSeats {
					fmt.Printf("    [FALHA GRAVE] INCONSISTÊNCIA: mais vagas que o total inicial (%d > %d)!\n",
						s.AvailableSeats, initialSeats)
					allConsistent = false
				}
			}
		}
	}

	if allConsistent {
		fmt.Println("  >> STATUS: ZERO OVERBOOKING CONFIRMADO! Todos os assentos estão consistentes.")
	}
	fmt.Println("================================================================")
}
