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
	fmt.Println("           SISTEMA DE CARONAS DISTRIBUÍDO - PASSAGEIRO          ")
	fmt.Println("================================================================")

	fmt.Print(" Digite seu Token de Passageiro: ")
	scanner.Scan()
	passengerToken := strings.TrimSpace(scanner.Text())
	if passengerToken == "" {
		passengerToken = "passageiro_default"
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

	fmt.Printf("[ CONECTADO] Passageiro [%s] conectado com sucesso ao nó: %s\n", passengerToken, serverAddr)

	for {
		fmt.Println("\n----------------------------------------------------------------")
		fmt.Printf("   PAINEL DO PASSAGEIRO | Usuário: [%s]  |  Nó: [%s]\n", passengerToken, serverAddr)
		fmt.Println("----------------------------------------------------------------")
		fmt.Println("  [1]  Consultar e Reservar Itinerários (BFS / 2PC)")
		fmt.Println("  [2]  Minhas Reservas Efetuadas")
		fmt.Println("  [3]  Cancelar uma Reserva")
		fmt.Println("  [0]  Sair")
		fmt.Print(" Escolha uma opção: ")

		scanner.Scan()
		option := strings.TrimSpace(scanner.Text())

		switch option {
		case "1":
			handleSearchAndBook(client, scanner, passengerToken)
		case "2":
			handleListBookings(client, passengerToken)
		case "3":
			handleCancelBooking(client, scanner, passengerToken)
		case "0":
			fmt.Println("\n A encerrar sessão do passageiro...")
			return
		default:
			fmt.Println("\n[!] Opção inválida. Tente novamente.")
		}
	}
}

func handleSearchAndBook(client *Client, scanner *bufio.Scanner, token string) {
	fmt.Println("\n================================================================")
	fmt.Println("                     CONSULTAR ITINERÁRIOS                      ")
	fmt.Println("================================================================")

	fmt.Print(" Cidade de Origem: ")
	scanner.Scan()
	origin := strings.TrimSpace(scanner.Text())

	fmt.Print(" Cidade de Destino: ")
	scanner.Scan()
	destination := strings.TrimSpace(scanner.Text())

	fmt.Print(" Data da Viagem (AAAA-MM-DD): ")
	scanner.Scan()
	date := strings.TrimSpace(scanner.Text())

	searchPayload := protocol.SearchItineraryData{
		Origin:      origin,
		Destination: destination,
		Date:        date,
	}

	resp, err := client.Send(protocol.ActionSearchItinerary, token, searchPayload)
	if err != nil {
		fmt.Printf("\n[ ERRO]: %v\n", err)
		return
	}
	if resp.Status != "SUCCESS" {
		fmt.Printf("\n[ ERRO]: %s\n", resp.Message)
		return
	}

	var itineraries []domain.ItineraryResult
	json.Unmarshal([]byte(resp.Data), &itineraries)

	if len(itineraries) == 0 {
		fmt.Println("\n Nenhum itinerário direto ou combinado encontrado para esta rota nesta data.")
		return
	}

	fmt.Printf("\n========================================================================================\n")
	fmt.Printf("               ITINERÁRIOS ENCONTRADOS (%d OPÇÕES DISPONÍVEIS)                          \n", len(itineraries))
	fmt.Printf("========================================================================================\n")

	for idx, itin := range itineraries {
		tipo := "DIRETA"
		if len(itin.Drivers) > 1 {
			tipo = fmt.Sprintf("COMBINADA (%d TROCAS/TRECHOS)", len(itin.Drivers))
		}

		fmt.Printf("\n+--------------------------------------------------------------------------------------+\n")
		fmt.Printf("   OPÇÃO #%d  |  Tipo: %s\n", idx+1, tipo)
		fmt.Printf("   Saída: %s hrs  |   Vagas Livres: %d  |   PREÇO TOTAL: R$ %.2f\n",
			itin.DepartureTime, itin.AvailableSeats, itin.TotalPrice)
		fmt.Printf("   Motoristas envolvidos: %s\n", strings.Join(itin.Drivers, " ➔ "))
		fmt.Printf("+--------------------------------------------------------------------------------------+ \n")

		fmt.Println("  +----+------------------------+------------------------+-------------------+----------+---------+")
		fmt.Println("  | #  | ORIGEM                 | DESTINO                | MOTORISTA         | SAÍDA    | PREÇO   |")
		fmt.Println("  +----+------------------------+------------------------+-------------------+----------+---------+")
		for j, seg := range itin.Segments {
			fmt.Printf("  | %-2d | %-22s | %-22s | %-17s | %-8s | R$%-6.2f |\n",
				j+1, truncate(seg.From, 22), truncate(seg.To, 22), truncate(seg.DriverToken, 17), seg.DepartureTime, seg.Price)
		}
		fmt.Println("  +----+------------------------+------------------------+-------------------+----------+---------+")
	}

	fmt.Print("\n Deseja reservar alguma opção acima? (Digite o número da opção 1..N ou 0 para cancelar): ")
	scanner.Scan()
	choice, err := strconv.Atoi(strings.TrimSpace(scanner.Text()))
	if err != nil || choice < 1 || choice > len(itineraries) {
		fmt.Println(" Operação de reserva cancelada.")
		return
	}

	selectedItin := itineraries[choice-1]
	bookResp, err := client.Send(protocol.ActionBookItinerary, token, selectedItin)
	if err != nil {
		fmt.Printf("\n[ ERRO TCP]: %v\n", err)
		return
	}

	if bookResp.Status == "SUCCESS" {
		fmt.Printf("\n[ SUCESSO NO 2PC] %s\n", bookResp.Message)
	} else {
		fmt.Printf("\n[ FALHA NA TRANSAÇÃO] [%s] %s\n", bookResp.Code, bookResp.Message)
	}
}

func handleListBookings(client *Client, token string) {
	resp, err := client.Send(protocol.ActionListBookings, token, nil)
	if err != nil {
		fmt.Printf("\n[ ERRO] Não foi possível carregar as suas reservas: %v\n", err)
		return
	}
	if resp.Status != "SUCCESS" {
		fmt.Println("\n[ ERRO] Não foi possível carregar as suas reservas.")
		return
	}

	var bookings []*domain.Booking
	json.Unmarshal([]byte(resp.Data), &bookings)

	if len(bookings) == 0 {
		fmt.Println("\n Nenhuma reserva encontrada no seu histórico.")
		return
	}

	fmt.Println("\n==========================================================================================")
	fmt.Println("                                MINHAS RESERVAS EFETUADAS                                 ")
	fmt.Println("==========================================================================================")
	for _, b := range bookings {
		fmt.Printf("\n   ID DA RESERVA: %s  |   Status: %s  |   Data: %s  |   Valor Total: R$ %.2f\n",
			b.ID, b.Status, b.Date, b.Itinerary.TotalPrice)

		fmt.Println("  +----+------------------------+------------------------+-------------------+----------+---------+")
		fmt.Println("  | #  | ORIGEM                 | DESTINO                | MOTORISTA         | SAÍDA    | PREÇO   |")
		fmt.Println("  +----+------------------------+------------------------+-------------------+----------+---------+")
		for j, seg := range b.Itinerary.Segments {
			fmt.Printf("  | %-2d | %-22s | %-22s | %-17s | %-8s | R$%-6.2f |\n",
				j+1, truncate(seg.From, 22), truncate(seg.To, 22), truncate(seg.DriverToken, 17), seg.DepartureTime, seg.Price)
		}
		fmt.Println("  +----+------------------------+------------------------+-------------------+----------+---------+")
	}
}

func handleCancelBooking(client *Client, scanner *bufio.Scanner, token string) {
	handleListBookings(client, token)

	fmt.Print("\n Digite o ID exato da Reserva que deseja CANCELAR (ex: BOOK_0001): ")
	scanner.Scan()
	bookingID := strings.TrimSpace(scanner.Text())

	if bookingID == "" {
		return
	}

	cancelPayload := protocol.CancelBookingData{BookingID: bookingID}
	resp, err := client.Send(protocol.ActionCancelBooking, token, cancelPayload)

	if err != nil {
		fmt.Printf("\n[ ERRO TCP]: %v\n", err)
		return
	}

	if resp.Status == "SUCCESS" {
		fmt.Printf("\n[ SUCESSO] %s\n", resp.Message)
	} else {
		fmt.Printf("\n[ FALHA] [%s] %s\n", bookRespCodeOrGeneric(resp.Code), resp.Message)
	}
}

func bookRespCodeOrGeneric(code string) string {
	if code == "" {
		return "ERROR"
	}
	return code
}

func truncate(str string, max int) string {
	if len(str) > max {
		return str[:max-3] + "..."
	}
	return str
}
