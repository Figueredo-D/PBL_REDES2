package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Figueredo-D/PBL_REDES/internal/domain"
	"github.com/Figueredo-D/PBL_REDES/internal/protocol"
)

type Client struct {
	address string
	conn    net.Conn
	reader  *bufio.Reader
}

func NewClient(address string) (*Client, error) {
	c := &Client{address: address}
	if err := c.connect(); err != nil {
		return nil, err
	}
	return c, nil
}

// connect abre uma nova conexão TCP com o nó escolhido, fechando a anterior se existir.
func (c *Client) connect() error {
	if c.conn != nil {
		c.conn.Close()
	}
	conn, err := net.DialTimeout("tcp", c.address, 3*time.Second)
	if err != nil {
		return err
	}
	c.conn = conn
	c.reader = bufio.NewReader(conn)
	return nil
}

// roundTrip envia a requisição e lê a resposta na conexão atual.
func (c *Client) roundTrip(reqBytes []byte) (*protocol.Response, error) {
	c.conn.SetDeadline(time.Now().Add(15 * time.Second))

	if _, err := c.conn.Write(reqBytes); err != nil {
		return nil, err
	}

	respBytes, err := c.reader.ReadBytes('\n')
	if err != nil {
		return nil, err
	}

	var resp protocol.Response
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Send envia a requisição. Se a conexão tiver caído (por exemplo, o servidor reiniciou),
// reconecta uma vez e tenta de novo.
func (c *Client) Send(action, token string, payload interface{}) (*protocol.Response, error) {
	var payloadString string
	if payload != nil {
		b, _ := json.Marshal(payload)
		payloadString = string(b)
	}

	req := protocol.Request{Action: action, Token: token, Data: payloadString}
	reqBytes, _ := json.Marshal(req)
	reqBytes = append(reqBytes, '\n')

	resp, err := c.roundTrip(reqBytes)
	if err == nil {
		return resp, nil
	}

	if cerr := c.connect(); cerr != nil {
		return nil, fmt.Errorf("servidor indisponível: %w", cerr)
	}
	return c.roundTrip(reqBytes)
}

func (c *Client) Close() {
	if c.conn != nil {
		c.conn.Close()
	}
}

// resolveServerAddr normaliza o endereço de host:porta, adicionando porta 8080 se omitida.
func resolveServerAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	if !strings.Contains(addr, ":") {
		return addr + ":8080"
	}
	return addr
}

func main() {
	scanner := bufio.NewScanner(os.Stdin)

	fmt.Println("================================================================")
	fmt.Println("            SISTEMA DE CARONAS DISTRIBUÍDO - MOTORISTA          ")
	fmt.Println("================================================================")

	fmt.Print(" Digite seu Token de Motorista: ")
	scanner.Scan()
	driverToken := strings.TrimSpace(scanner.Text())
	if driverToken == "" {
		driverToken = "driver_default"
	}

	var serverAddr string

	// 1. Tenta obter endereço via argumento posicional ou variáveis de ambiente (ideal para Docker e scripts)
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-") {
		serverAddr = resolveServerAddr(os.Args[1])
	} else if env := strings.TrimSpace(os.Getenv("SERVER_HOST")); env != "" {
		serverAddr = resolveServerAddr(env)
	} else if env := strings.TrimSpace(os.Getenv("SERVER_ADDR")); env != "" {
		serverAddr = resolveServerAddr(env)
	}

	// 2. Caso não tenha sido informado previamente, exibe o menu interativo
	if serverAddr == "" {
		fmt.Println("\n----------------------------------------------------------------")
		fmt.Println("                 SELECIONE O NÓ DO CONSÓRCIO                    ")
		fmt.Println("----------------------------------------------------------------")
		fmt.Println("  [1] Servidor A  (localhost:8080)")
		fmt.Println("  [2] Servidor B  (localhost:8081)")
		fmt.Println("  [3] Servidor C  (localhost:8082)")
		fmt.Println("  [4] Outro endereço (Host:Porta remoto)")
		fmt.Print(" Escolha o servidor (1-4): ")

		scanner.Scan()
		nodeChoice := strings.TrimSpace(scanner.Text())

		switch nodeChoice {
		case "2":
			serverAddr = "localhost:8081"
		case "3":
			serverAddr = "localhost:8082"
		case "4":
			fmt.Print(" Digite o endereço do servidor (ex: 192.168.1.100:8080): ")
			scanner.Scan()
			customAddr := strings.TrimSpace(scanner.Text())
			serverAddr = resolveServerAddr(customAddr)
			if serverAddr == "" {
				serverAddr = "localhost:8080"
			}
		case "1":
			fallthrough
		default:
			serverAddr = "localhost:8080"
		}
	}

	client, err := NewClient(serverAddr)
	if err != nil {
		fmt.Printf("\n[ ERRO DE CONEXÃO] Não foi possível conectar ao nó em (%s): %v\n", serverAddr, err)
		os.Exit(1)
	}
	defer client.Close()

	fmt.Printf("[ CONECTADO] Motorista [%s] conectado com sucesso ao nó: %s\n", driverToken, serverAddr)

	for {
		fmt.Println("\n----------------------------------------------------------------")
		fmt.Printf("   PAINEL DO MOTORISTA  |  Usuário: [%s]  |  Nó: [%s]\n", driverToken, serverAddr)
		fmt.Println("----------------------------------------------------------------")
		fmt.Println("  [1]  Ofertar Nova Carona")
		fmt.Println("  [2]  Minhas Caronas Cadastradas")
		fmt.Println("  [3]  Cancelar uma Carona")
		fmt.Println("  [0]  Sair")
		fmt.Print(" Escolha uma opção: ")

		scanner.Scan()
		option := strings.TrimSpace(scanner.Text())

		switch option {
		case "1":
			handleOfferRide(client, scanner, driverToken)
		case "2":
			handleListRides(client, driverToken)
		case "3":
			handleCancelRide(client, scanner, driverToken)
		case "0":
			fmt.Println("\n A encerrar sessão do motorista...")
			return
		default:
			fmt.Println("\n[!] Opção inválida. Tente novamente.")
		}
	}
}

func handleOfferRide(client *Client, scanner *bufio.Scanner, token string) {
	fmt.Println("\n================================================================")
	fmt.Println("                   CADASTRAR NOVA CARONA                        ")
	fmt.Println("================================================================")

	fmt.Print(" Cidades da rota (separadas por vírgula, ex: Feira, Salvador): ")
	scanner.Scan()
	rawCities := strings.Split(scanner.Text(), ",")
	var route []string
	for _, c := range rawCities {
		city := strings.TrimSpace(c)
		if city != "" {
			route = append(route, city)
		}
	}

	if len(route) < 2 {
		fmt.Println("[ ERRO] A rota deve conter pelo menos 2 cidades (Origem e Destino).")
		return
	}

	fmt.Print(" Data da viagem (AAAA-MM-DD): ")
	scanner.Scan()
	date := strings.TrimSpace(scanner.Text())

	fmt.Print(" Horário de Saída (HH:MM): ")
	scanner.Scan()
	timeStr := strings.TrimSpace(scanner.Text())

	fmt.Print("💺 Quantidade total de assentos disponíveis: ")
	scanner.Scan()
	seats, err := strconv.Atoi(strings.TrimSpace(scanner.Text()))
	if err != nil || seats <= 0 {
		fmt.Println("[ ERRO] Quantidade de assentos inválida.")
		return
	}

	numSegments := len(route) - 1
	var prices []float64
	fmt.Printf("\n Informe o preço individual para cada um dos %d trechos:\n", numSegments)

	for i := 0; i < numSegments; i++ {
		fmt.Printf("   Trecho [%s ➔ %s] (R$): ", route[i], route[i+1])
		scanner.Scan()
		price, err := strconv.ParseFloat(strings.TrimSpace(scanner.Text()), 64)
		if err != nil || price <= 0 {
			fmt.Println("[ ERRO] Preço inválido. Operação cancelada.")
			return
		}
		prices = append(prices, price)
	}

	offerPayload := protocol.OfferRideData{
		Route:         route,
		Date:          date,
		Time:          timeStr,
		TotalSeats:    seats,
		SegmentPrices: prices,
	}

	resp, err := client.Send(protocol.ActionOfferRide, token, offerPayload)
	if err != nil {
		fmt.Printf("[ ERRO TCP]: %v\n", err)
		return
	}

	if resp.Status == "SUCCESS" {
		fmt.Printf("\n[ SUCESSO] %s\n", resp.Message)
	} else {
		fmt.Printf("\n[ FALHA] [%s] %s\n", resp.Code, resp.Message)
	}
}

func handleListRides(client *Client, token string) {
	resp, err := client.Send(protocol.ActionListRides, token, nil)
	if err != nil {
		fmt.Printf("\n[ ERRO] Não foi possível consultar as suas caronas cadastradas: %v\n", err)
		return
	}
	if resp.Status != "SUCCESS" {
		fmt.Println("\n[ ERRO] Não foi possível consultar as suas caronas cadastradas.")
		return
	}

	var rides []*domain.Ride
	json.Unmarshal([]byte(resp.Data), &rides)

	if len(rides) == 0 {
		fmt.Println("\n Nenhuma carona cadastrada no seu perfil neste nó.")
		return
	}

	fmt.Println("\n==========================================================================================")
	fmt.Println("                               MINHAS CARONAS OFERTADAS                                   ")
	fmt.Println("==========================================================================================")
	for _, ride := range rides {
		fmt.Printf("\n  ID DA CARONA: %s  |   Data: %s  |   Saída: %s hrs  |   Vagas Totais: %d\n",
			ride.ID, ride.Date, ride.Time, ride.TotalSeats)

		fmt.Println("  +----+------------------------+------------------------+----------+-------------------+")
		fmt.Println("  | #  | ORIGEM                 | DESTINO                | PREÇO    | VAGAS DISPONÍVEIS |")
		fmt.Println("  +----+------------------------+------------------------+----------+-------------------+")
		for j, seg := range ride.Segments {
			fmt.Printf("  | %-2d | %-22s | %-22s | R$%-6.2f | %-17d |\n",
				j+1, truncate(seg.From, 22), truncate(seg.To, 22), seg.Price, seg.AvailableSeats)
		}
		fmt.Println("  +----+------------------------+------------------------+----------+-------------------+")
	}
}

func handleCancelRide(client *Client, scanner *bufio.Scanner, token string) {
	handleListRides(client, token)

	fmt.Print("\n Digite o ID exato da Carona que deseja CANCELAR (ex: SERVER_A_RIDE_0001): ")
	scanner.Scan()
	rideID := strings.TrimSpace(scanner.Text())

	if rideID == "" {
		return
	}

	cancelPayload := protocol.CancelRideData{RideID: rideID}
	resp, err := client.Send(protocol.ActionCancelRide, token, cancelPayload)

	if err != nil {
		fmt.Printf("\n[ ERRO TCP]: %v\n", err)
		return
	}

	if resp.Status == "SUCCESS" {
		fmt.Printf("\n[ SUCESSO] %s\n", resp.Message)
	} else {
		fmt.Printf("\n[ FALHA] [%s] %s\n", resp.Code, resp.Message)
	}
}

func truncate(str string, max int) string {
	if len(str) > max {
		return str[:max-3] + "..."
	}
	return str
}
