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
	"unicode/utf8"

	"github.com/Figueredo-D/PBL_REDES/internal/domain"
	"github.com/Figueredo-D/PBL_REDES/internal/protocol"
)

const (
	colorReset  = "\033[0m"
	colorBold   = "\033[1m"
	colorDim    = "\033[2m"
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorBlue   = "\033[34m"
	colorCyan   = "\033[36m"
	colorWhite  = "\033[97m"
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
		_ = c.conn.Close()
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
	_ = c.conn.SetDeadline(time.Now().Add(15 * time.Second))

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
		_ = c.conn.Close()
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

// defaultNodeAddr retorna o endereço configurado para um dos 3 servidores do consórcio
// (suportando tanto execução em container Docker quanto execução direta no host).
func defaultNodeAddr(envKey, localFallback string) string {
	if val := strings.TrimSpace(os.Getenv(envKey)); val != "" {
		return resolveServerAddr(val)
	}
	return localFallback
}

// selectAndConnect apresenta o seletor de servidor e estabelece a conexão TCP.
func selectAndConnect(scanner *bufio.Scanner, initialAddr string) (*Client, string, string) {
	addrA := defaultNodeAddr("SERVER_A_ADDR", "localhost:8080")
	addrB := defaultNodeAddr("SERVER_B_ADDR", "localhost:8081")
	addrC := defaultNodeAddr("SERVER_C_ADDR", "localhost:8082")

	serverAddr := initialAddr
	nodeLabel := "Personalizado"

	for {
		if serverAddr == "" {
			fmt.Println()
			fmt.Printf("%s+==============================================================================+%s\n", colorCyan, colorReset)
			fmt.Printf("%s|%s                 %sSELECIONE O SERVIDOR DO CONSORCIO (EMPRESA)%s                  %s|%s\n", colorCyan, colorReset, colorBold, colorReset, colorCyan, colorReset)
			fmt.Printf("%s+------------------------------------------------------------------------------+%s\n", colorCyan, colorReset)
			fmt.Printf("%s|%s   %s[1]%s  Empresa A  -  Servidor A   %s(%-22s)%s                   %s|%s\n", colorCyan, colorReset, colorBold+colorGreen, colorReset, colorDim, addrA, colorReset, colorCyan, colorReset)
			fmt.Printf("%s|%s   %s[2]%s  Empresa B  -  Servidor B   %s(%-22s)%s                   %s|%s\n", colorCyan, colorReset, colorBold+colorBlue, colorReset, colorDim, addrB, colorReset, colorCyan, colorReset)
			fmt.Printf("%s|%s   %s[3]%s  Empresa C  -  Servidor C   %s(%-22s)%s                   %s|%s\n", colorCyan, colorReset, colorBold+colorYellow, colorReset, colorDim, addrC, colorReset, colorCyan, colorReset)
			fmt.Printf("%s|%s   %s[4]%s  Outro endereco (Host:Porta remoto)                                    %s|%s\n", colorCyan, colorReset, colorBold, colorReset, colorCyan, colorReset)
			fmt.Printf("%s+==============================================================================+%s\n", colorCyan, colorReset)
			fmt.Printf(" %s>%s Escolha o servidor (1-4) [padrao: 1]: ", colorCyan+colorBold, colorReset)

			if !scanner.Scan() {
				os.Exit(0)
			}
			nodeChoice := strings.TrimSpace(scanner.Text())

			switch nodeChoice {
			case "2":
				serverAddr = addrB
				nodeLabel = "SERVER_B"
			case "3":
				serverAddr = addrC
				nodeLabel = "SERVER_C"
			case "4":
				fmt.Printf(" %s>%s Digite o endereco do servidor (ex: 192.168.1.10:8080): ", colorCyan+colorBold, colorReset)
				if !scanner.Scan() {
					os.Exit(0)
				}
				customAddr := resolveServerAddr(scanner.Text())
				if customAddr == "" {
					serverAddr = addrA
					nodeLabel = "SERVER_A"
				} else {
					serverAddr = customAddr
					nodeLabel = "REMOTO"
				}
			case "1", "":
				serverAddr = addrA
				nodeLabel = "SERVER_A"
			default:
				fmt.Printf("\n %s[!] Opcao invalida. Selecionando Servidor A por padrao.%s\n", colorYellow, colorReset)
				serverAddr = addrA
				nodeLabel = "SERVER_A"
			}
		} else {
			switch serverAddr {
			case addrA, "localhost:8080":
				nodeLabel = "SERVER_A"
			case addrB, "localhost:8081":
				nodeLabel = "SERVER_B"
			case addrC, "localhost:8082":
				nodeLabel = "SERVER_C"
			default:
				nodeLabel = "REMOTO"
			}
		}

		client, err := NewClient(serverAddr)
		if err == nil {
			return client, serverAddr, nodeLabel
		}

		fmt.Printf("\n %s[ERRO DE CONEXAO]%s Nao foi possivel conectar em %s(%s)%s: %v\n",
			colorRed+colorBold, colorReset, colorBold, serverAddr, colorReset, err)
		serverAddr = ""
	}
}

func printWelcomeBanner() {
	fmt.Println()
	fmt.Printf("%s+==============================================================================+%s\n", colorCyan, colorReset)
	fmt.Printf("%s|%s              %sVAIJUNTO - CONSORCIO DE CARONAS COMPARTILHADAS%s                  %s|%s\n", colorCyan, colorReset, colorBold+colorWhite, colorReset, colorCyan, colorReset)
	fmt.Printf("%s|%s                     %sPORTAL INTERATIVO DO PASSAGEIRO%s                          %s|%s\n", colorCyan, colorReset, colorCyan+colorBold, colorReset, colorCyan, colorReset)
	fmt.Printf("%s+==============================================================================+%s\n", colorCyan, colorReset)
}

func promptPassengerToken(scanner *bufio.Scanner, current string) string {
	if current != "" {
		fmt.Printf("\n %s>%s Digite o novo Token de Passageiro (Enter para manter [%s]): ", colorCyan+colorBold, colorReset, current)
	} else {
		fmt.Printf("\n %s>%s Digite seu Token/Identificador de Passageiro: ", colorCyan+colorBold, colorReset)
	}
	if !scanner.Scan() {
		os.Exit(0)
	}
	token := strings.TrimSpace(scanner.Text())
	if token == "" {
		if current != "" {
			return current
		}
		return "passageiro_default"
	}
	return token
}

func printMainMenu(passengerToken, nodeLabel, serverAddr string) {
	statusLine := fmt.Sprintf(" Passageiro: [%s]   |   Empresa: [%s (%s)]", passengerToken, nodeLabel, serverAddr)
	fmt.Println()
	fmt.Printf("%s+==============================================================================+%s\n", colorCyan, colorReset)
	fmt.Printf("%s|%s  %sPAINEL DO PASSAGEIRO%s                                                        %s|%s\n", colorCyan, colorReset, colorBold+colorWhite, colorReset, colorCyan, colorReset)
	fmt.Printf("%s|%s  %s%-74s%s  %s|%s\n", colorCyan, colorReset, colorDim, padRight(statusLine, 74), colorReset, colorCyan, colorReset)
	fmt.Printf("%s+------------------------------------------------------------------------------+%s\n", colorCyan, colorReset)
	fmt.Printf("%s|%s                                                                              %s|%s\n", colorCyan, colorReset, colorCyan, colorReset)
	fmt.Printf("%s|%s    %s[1]%s  Consultar e Reservar Itinerarios (Busca BFS / Reserva 2PC)           %s|%s\n", colorCyan, colorReset, colorBold+colorGreen, colorReset, colorCyan, colorReset)
	fmt.Printf("%s|%s    %s[2]%s  Minhas Reservas Efetuadas                                            %s|%s\n", colorCyan, colorReset, colorBold+colorCyan, colorReset, colorCyan, colorReset)
	fmt.Printf("%s|%s    %s[3]%s  Cancelar uma Reserva                                                 %s|%s\n", colorCyan, colorReset, colorBold+colorYellow, colorReset, colorCyan, colorReset)
	fmt.Printf("%s|%s    %s[4]%s  Trocar de Servidor / Trocar Usuario                                  %s|%s\n", colorCyan, colorReset, colorBold+colorBlue, colorReset, colorCyan, colorReset)
	fmt.Printf("%s|%s    %s[0]%s  Encerrar Sessao                                                      %s|%s\n", colorCyan, colorReset, colorBold+colorRed, colorReset, colorCyan, colorReset)
	fmt.Printf("%s|%s                                                                              %s|%s\n", colorCyan, colorReset, colorCyan, colorReset)
	fmt.Printf("%s+==============================================================================+%s\n", colorCyan, colorReset)
	fmt.Printf(" %s>%s Escolha uma opcao (0-4): ", colorCyan+colorBold, colorReset)
}

func main() {
	scanner := bufio.NewScanner(os.Stdin)

	printWelcomeBanner()
	passengerToken := promptPassengerToken(scanner, "")

	var initialAddr string
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-") {
		initialAddr = resolveServerAddr(os.Args[1])
	} else if env := strings.TrimSpace(os.Getenv("SERVER_HOST")); env != "" {
		initialAddr = resolveServerAddr(env)
	} else if env := strings.TrimSpace(os.Getenv("SERVER_ADDR")); env != "" {
		initialAddr = resolveServerAddr(env)
	}

	client, serverAddr, nodeLabel := selectAndConnect(scanner, initialAddr)
	defer func() {
		if client != nil {
			client.Close()
		}
	}()

	fmt.Printf("\n %s[CONECTADO]%s Passageiro %s[%s]%s autenticado em %s%s (%s)%s\n",
		colorGreen+colorBold, colorReset, colorBold, passengerToken, colorReset, colorCyan+colorBold, nodeLabel, serverAddr, colorReset)

	for {
		printMainMenu(passengerToken, nodeLabel, serverAddr)

		if !scanner.Scan() {
			fmt.Println("\n Encerrando sessao do passageiro...")
			return
		}
		option := strings.TrimSpace(scanner.Text())

		switch option {
		case "1":
			handleSearchAndBook(client, scanner, passengerToken)
		case "2":
			handleListBookings(client, passengerToken)
		case "3":
			handleCancelBooking(client, scanner, passengerToken)
		case "4":
			passengerToken = promptPassengerToken(scanner, passengerToken)
			client.Close()
			client, serverAddr, nodeLabel = selectAndConnect(scanner, "")
			fmt.Printf("\n %s[CONECTADO]%s Passageiro %s[%s]%s conectado a %s%s (%s)%s\n",
				colorGreen+colorBold, colorReset, colorBold, passengerToken, colorReset, colorCyan+colorBold, nodeLabel, serverAddr, colorReset)
		case "0":
			fmt.Printf("\n %sSessao do passageiro encerrada com seguranca. Boa viagem!%s\n\n", colorCyan+colorBold, colorReset)
			return
		default:
			fmt.Printf("\n %s[!] Opcao invalida. Digite um numero entre 0 e 4.%s\n", colorYellow, colorReset)
		}
	}
}

func handleSearchAndBook(client *Client, scanner *bufio.Scanner, token string) {
	fmt.Println()
	fmt.Printf("%s+------------------------------------------------------------------------------+%s\n", colorGreen, colorReset)
	fmt.Printf("%s|%s                   %sBUSCAR E RESERVAR ITINERARIOS NO CONSORCIO%s                 %s|%s\n", colorGreen, colorReset, colorBold+colorWhite, colorReset, colorGreen, colorReset)
	fmt.Printf("%s+------------------------------------------------------------------------------+%s\n", colorGreen, colorReset)

	fmt.Printf(" %s[1/3]%s Cidade de Origem: ", colorCyan+colorBold, colorReset)
	if !scanner.Scan() {
		return
	}
	origin := strings.TrimSpace(scanner.Text())

	fmt.Printf(" %s[2/3]%s Cidade de Destino: ", colorCyan+colorBold, colorReset)
	if !scanner.Scan() {
		return
	}
	destination := strings.TrimSpace(scanner.Text())

	fmt.Printf(" %s[3/3]%s Data da Viagem (AAAA-MM-DD): ", colorCyan+colorBold, colorReset)
	if !scanner.Scan() {
		return
	}
	date := strings.TrimSpace(scanner.Text())

	searchPayload := protocol.SearchItineraryData{
		Origin:      origin,
		Destination: destination,
		Date:        date,
	}

	resp, err := client.Send(protocol.ActionSearchItinerary, token, searchPayload)
	if err != nil {
		fmt.Printf("\n %s[ERRO DE COMUNICACAO]%s %v\n", colorRed+colorBold, colorReset, err)
		return
	}
	if resp.Status != "SUCCESS" {
		fmt.Printf("\n %s[ERRO]%s %s\n", colorRed+colorBold, colorReset, resp.Message)
		return
	}

	var itineraries []domain.ItineraryResult
	_ = json.Unmarshal([]byte(resp.Data), &itineraries)

	if len(itineraries) == 0 {
		fmt.Printf("\n %s[!] Nenhum itinerario direto ou combinado encontrado de [%s] para [%s] em %s.%s\n",
			colorYellow, origin, destination, date, colorReset)
		return
	}

	fmt.Println()
	fmt.Printf("%s+==============================================================================+%s\n", colorCyan, colorReset)
	fmt.Printf("%s|%s               %sITINERARIOS DISPONIVEIS ENCONTRADOS (%2d OPCOES)%s                %s|%s\n", colorCyan, colorReset, colorBold+colorWhite, len(itineraries), colorReset, colorCyan, colorReset)
	fmt.Printf("%s+==============================================================================+%s\n", colorCyan, colorReset)

	for idx, itin := range itineraries {
		tipoBadge := fmt.Sprintf("%s[VIAGEM DIRETA]%s", colorGreen+colorBold, colorReset)
		if len(itin.Segments) > 1 {
			tipoBadge = fmt.Sprintf("%s[CONEXAO COMBINADA - %d TRECHOS]%s", colorYellow+colorBold, len(itin.Segments), colorReset)
		}

		var cities []string
		for i, seg := range itin.Segments {
			if i == 0 {
				cities = append(cities, seg.From)
			}
			cities = append(cities, seg.To)
		}
		routeChain := strings.Join(cities, " -> ")

		fmt.Println()
		fmt.Printf(" %s+----------------------------------------------------------------------------+%s\n", colorCyan, colorReset)
		fmt.Printf(" %s|%s %sOPCAO #%d%s  %s  |  Saida: %s%sh%s  |  Vagas: %s%d%s  |  Total: %sR$ %.2f%s\n",
			colorCyan, colorReset,
			colorBold+colorWhite, idx+1, colorReset,
			tipoBadge,
			colorBold, itin.DepartureTime, colorReset,
			colorGreen+colorBold, itin.AvailableSeats, colorReset,
			colorGreen+colorBold, itin.TotalPrice, colorReset)
		fmt.Printf(" %s|%s Trajeto: %s%s%s\n", colorCyan, colorReset, colorCyan+colorBold, routeChain, colorReset)
		fmt.Printf(" %s+----+------------------+------------------+----------+-------------+--------+%s\n", colorCyan, colorReset)
		fmt.Printf(" %s|%s %s# %s %s|%s %sORIGEM          %s %s|%s %sDESTINO         %s %s|%s %sEMPRESA %s %s|%s %sMOTORISTA  %s %s|%s %sPRECO %s %s|%s\n",
			colorCyan, colorReset, colorBold, colorReset,
			colorCyan, colorReset, colorBold, colorReset,
			colorCyan, colorReset, colorBold, colorReset,
			colorCyan, colorReset, colorBold, colorReset,
			colorCyan, colorReset, colorBold, colorReset,
			colorCyan, colorReset, colorBold, colorReset,
			colorCyan, colorReset)
		fmt.Printf(" %s+----+------------------+------------------+----------+-------------+--------+%s\n", colorCyan, colorReset)

		for j, seg := range itin.Segments {
			comp := seg.CompanyID
			if comp == "" {
				comp = "LOCAL"
			}
			precoStr := fmt.Sprintf("R$%5.2f", seg.Price)
			fmt.Printf(" %s|%s %-2d %s|%s %-16s %s|%s %-16s %s|%s %-8s %s|%s %-11s %s|%s %-6s %s|%s\n",
				colorCyan, colorReset, j+1,
				colorCyan, colorReset, padRight(seg.From, 16),
				colorCyan, colorReset, padRight(seg.To, 16),
				colorCyan, colorReset, padRight(comp, 8),
				colorCyan, colorReset, padRight(seg.DriverToken, 11),
				colorCyan, colorReset, padRight(precoStr, 6),
				colorCyan, colorReset)
		}
		fmt.Printf(" %s+----+------------------+------------------+----------+-------------+--------+%s\n", colorCyan, colorReset)
	}

	fmt.Printf("\n %s>%s Digite o numero da %sOPCAO (1..%d)%s para reservar via 2PC [0 = voltar]: ",
		colorGreen+colorBold, colorReset, colorBold, len(itineraries), colorReset)
	if !scanner.Scan() {
		return
	}
	choice, err := strconv.Atoi(strings.TrimSpace(scanner.Text()))
	if err != nil || choice < 1 || choice > len(itineraries) {
		fmt.Printf(" %sOperacao de reserva cancelada.%s\n", colorDim, colorReset)
		return
	}

	selectedItin := itineraries[choice-1]
	fmt.Printf(" %s[...] Coordenando reserva atomica distribuida (Two-Phase Commit)...%s\n", colorCyan, colorReset)
	bookResp, err := client.Send(protocol.ActionBookItinerary, token, selectedItin)
	if err != nil {
		fmt.Printf("\n %s[ERRO DE COMUNICACAO]%s %v\n", colorRed+colorBold, colorReset, err)
		return
	}

	if bookResp.Status == "SUCCESS" {
		fmt.Printf("\n %s[OK - RESERVA CONFIRMADA VIA 2PC]%s %s\n", colorGreen+colorBold, colorReset, bookResp.Message)
	} else {
		fmt.Printf("\n %s[TRANSACAO ABORTADA]%s [%s] %s\n", colorRed+colorBold, colorReset, bookResp.Code, bookResp.Message)
	}
}

func formatBookingStatusBadge(status string) string {
	switch status {
	case "CONFIRMED":
		return colorGreen + colorBold + "[CONFIRMADA]" + colorReset
	case "CANCELLED":
		return colorYellow + colorBold + "[CANCELADA PELO PASSAGEIRO]" + colorReset
	case "CANCELLED_BY_DRIVER":
		return colorRed + colorBold + "[CANCELADA PELO MOTORISTA]" + colorReset
	default:
		return colorWhite + colorBold + "[" + status + "]" + colorReset
	}
}

func handleListBookings(client *Client, token string) []*domain.Booking {
	resp, err := client.Send(protocol.ActionListBookings, token, nil)
	if err != nil {
		fmt.Printf("\n %s[ERRO]%s Nao foi possivel carregar suas reservas: %v\n", colorRed+colorBold, colorReset, err)
		return nil
	}
	if resp.Status != "SUCCESS" {
		fmt.Printf("\n %s[ERRO]%s Nao foi possivel carregar suas reservas.\n", colorRed+colorBold, colorReset)
		return nil
	}

	var bookings []*domain.Booking
	_ = json.Unmarshal([]byte(resp.Data), &bookings)

	fmt.Println()
	fmt.Printf("%s+==============================================================================+%s\n", colorCyan, colorReset)
	fmt.Printf("%s|%s                         %sMINHAS RESERVAS EFETUADAS%s                            %s|%s\n", colorCyan, colorReset, colorBold+colorWhite, colorReset, colorCyan, colorReset)
	fmt.Printf("%s+==============================================================================+%s\n", colorCyan, colorReset)

	if len(bookings) == 0 {
		fmt.Printf("\n   %sNenhuma reserva encontrada para o passageiro [%s] neste servidor.%s\n", colorYellow, token, colorReset)
		return nil
	}

	for idx, b := range bookings {
		var cities []string
		for i, seg := range b.Itinerary.Segments {
			if i == 0 {
				cities = append(cities, seg.From)
			}
			cities = append(cities, seg.To)
		}
		routeChain := strings.Join(cities, " -> ")

		fmt.Println()
		fmt.Printf(" %s+----------------------------------------------------------------------------+%s\n", colorCyan, colorReset)
		fmt.Printf(" %s|%s %sRESERVA #%d%s  |  ID: %s%-18s%s  |  Status: %s\n",
			colorCyan, colorReset,
			colorBold+colorWhite, idx+1, colorReset,
			colorCyan+colorBold, b.ID, colorReset,
			formatBookingStatusBadge(b.Status))
		fmt.Printf(" %s|%s Trajeto: %s%s%s   |   Data: %s%s%s   |   Total: %sR$ %.2f%s\n",
			colorCyan, colorReset,
			colorBold, routeChain, colorReset,
			colorBold, b.Date, colorReset,
			colorGreen+colorBold, b.Itinerary.TotalPrice, colorReset)
		fmt.Printf(" %s+----+------------------+------------------+----------+-------------+--------+%s\n", colorCyan, colorReset)
		fmt.Printf(" %s|%s %s# %s %s|%s %sORIGEM          %s %s|%s %sDESTINO         %s %s|%s %sEMPRESA %s %s|%s %sMOTORISTA  %s %s|%s %sSAIDA %s %s|%s\n",
			colorCyan, colorReset, colorBold, colorReset,
			colorCyan, colorReset, colorBold, colorReset,
			colorCyan, colorReset, colorBold, colorReset,
			colorCyan, colorReset, colorBold, colorReset,
			colorCyan, colorReset, colorBold, colorReset,
			colorCyan, colorReset, colorBold, colorReset,
			colorCyan, colorReset)
		fmt.Printf(" %s+----+------------------+------------------+----------+-------------+--------+%s\n", colorCyan, colorReset)

		for j, seg := range b.Itinerary.Segments {
			comp := seg.CompanyID
			if comp == "" {
				comp = "LOCAL"
			}
			fmt.Printf(" %s|%s %-2d %s|%s %-16s %s|%s %-16s %s|%s %-8s %s|%s %-11s %s|%s %-6s %s|%s\n",
				colorCyan, colorReset, j+1,
				colorCyan, colorReset, padRight(seg.From, 16),
				colorCyan, colorReset, padRight(seg.To, 16),
				colorCyan, colorReset, padRight(comp, 8),
				colorCyan, colorReset, padRight(seg.DriverToken, 11),
				colorCyan, colorReset, padRight(seg.DepartureTime, 6),
				colorCyan, colorReset)
		}
		fmt.Printf(" %s+----+------------------+------------------+----------+-------------+--------+%s\n", colorCyan, colorReset)
	}

	return bookings
}

func handleCancelBooking(client *Client, scanner *bufio.Scanner, token string) {
	bookings := handleListBookings(client, token)
	if len(bookings) == 0 {
		return
	}

	fmt.Printf("\n %s>%s Digite o %sID da Reserva%s (ex: %s) ou o %snumero # da lista%s (1..%d) para cancelar [0 = voltar]: ",
		colorYellow+colorBold, colorReset, colorBold, colorReset, bookings[0].ID, colorBold, colorReset, len(bookings))
	if !scanner.Scan() {
		return
	}
	input := strings.TrimSpace(scanner.Text())
	if input == "" || input == "0" {
		fmt.Printf(" %sOperacao de cancelamento abortada.%s\n", colorDim, colorReset)
		return
	}

	bookingID := input
	if idx, err := strconv.Atoi(input); err == nil && idx >= 1 && idx <= len(bookings) {
		bookingID = bookings[idx-1].ID
	}

	cancelPayload := protocol.CancelBookingData{BookingID: bookingID}
	resp, err := client.Send(protocol.ActionCancelBooking, token, cancelPayload)
	if err != nil {
		fmt.Printf("\n %s[ERRO DE COMUNICACAO]%s %v\n", colorRed+colorBold, colorReset, err)
		return
	}

	if resp.Status == "SUCCESS" {
		fmt.Printf("\n %s[OK - RESERVA CANCELADA]%s %s\n", colorGreen+colorBold, colorReset, resp.Message)
	} else {
		fmt.Printf("\n %s[FALHA]%s [%s] %s\n", colorRed+colorBold, colorReset, bookRespCodeOrGeneric(resp.Code), resp.Message)
	}
}

func bookRespCodeOrGeneric(code string) string {
	if code == "" {
		return "ERROR"
	}
	return code
}

func padRight(str string, width int) string {
	runeLen := utf8.RuneCountInString(str)
	if runeLen > width {
		runes := []rune(str)
		if width > 3 {
			return string(runes[:width-3]) + "..."
		}
		return string(runes[:width])
	}
	return str + strings.Repeat(" ", width-runeLen)
}
