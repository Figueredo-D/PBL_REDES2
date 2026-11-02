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
	fmt.Printf("%s|%s                      %sPORTAL INTERATIVO DO MOTORISTA%s                          %s|%s\n", colorCyan, colorReset, colorCyan+colorBold, colorReset, colorCyan, colorReset)
	fmt.Printf("%s+==============================================================================+%s\n", colorCyan, colorReset)
}

func promptDriverToken(scanner *bufio.Scanner, current string) string {
	if current != "" {
		fmt.Printf("\n %s>%s Digite o novo Token de Motorista (Enter para manter [%s]): ", colorCyan+colorBold, colorReset, current)
	} else {
		fmt.Printf("\n %s>%s Digite seu Token/Identificador de Motorista: ", colorCyan+colorBold, colorReset)
	}
	if !scanner.Scan() {
		os.Exit(0)
	}
	token := strings.TrimSpace(scanner.Text())
	if token == "" {
		if current != "" {
			return current
		}
		return "motorista_default"
	}
	return token
}

func printMainMenu(driverToken, nodeLabel, serverAddr string) {
	statusLine := fmt.Sprintf(" Motorista: [%s]   |   Empresa: [%s (%s)]", driverToken, nodeLabel, serverAddr)
	fmt.Println()
	fmt.Printf("%s+==============================================================================+%s\n", colorCyan, colorReset)
	fmt.Printf("%s|%s  %sPAINEL DO MOTORISTA%s                                                         %s|%s\n", colorCyan, colorReset, colorBold+colorWhite, colorReset, colorCyan, colorReset)
	fmt.Printf("%s|%s  %s%-74s%s  %s|%s\n", colorCyan, colorReset, colorDim, padRight(statusLine, 74), colorReset, colorCyan, colorReset)
	fmt.Printf("%s+------------------------------------------------------------------------------+%s\n", colorCyan, colorReset)
	fmt.Printf("%s|%s                                                                              %s|%s\n", colorCyan, colorReset, colorCyan, colorReset)
	fmt.Printf("%s|%s    %s[1]%s  Ofertar Nova Carona                                                  %s|%s\n", colorCyan, colorReset, colorBold+colorGreen, colorReset, colorCyan, colorReset)
	fmt.Printf("%s|%s    %s[2]%s  Minhas Caronas Cadastradas                                           %s|%s\n", colorCyan, colorReset, colorBold+colorCyan, colorReset, colorCyan, colorReset)
	fmt.Printf("%s|%s    %s[3]%s  Cancelar uma Carona                                                  %s|%s\n", colorCyan, colorReset, colorBold+colorYellow, colorReset, colorCyan, colorReset)
	fmt.Printf("%s|%s    %s[4]%s  Trocar de Servidor / Trocar Usuario                                  %s|%s\n", colorCyan, colorReset, colorBold+colorBlue, colorReset, colorCyan, colorReset)
	fmt.Printf("%s|%s    %s[0]%s  Encerrar Sessao                                                      %s|%s\n", colorCyan, colorReset, colorBold+colorRed, colorReset, colorCyan, colorReset)
	fmt.Printf("%s|%s                                                                              %s|%s\n", colorCyan, colorReset, colorCyan, colorReset)
	fmt.Printf("%s+==============================================================================+%s\n", colorCyan, colorReset)
	fmt.Printf(" %s>%s Escolha uma opcao (0-4): ", colorCyan+colorBold, colorReset)
}

func main() {
	scanner := bufio.NewScanner(os.Stdin)

	printWelcomeBanner()
	driverToken := promptDriverToken(scanner, "")

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

	fmt.Printf("\n %s[CONECTADO]%s Motorista %s[%s]%s autenticado em %s%s (%s)%s\n",
		colorGreen+colorBold, colorReset, colorBold, driverToken, colorReset, colorCyan+colorBold, nodeLabel, serverAddr, colorReset)

	for {
		printMainMenu(driverToken, nodeLabel, serverAddr)

		if !scanner.Scan() {
			fmt.Println("\n Encerrando sessao do motorista...")
			return
		}
		option := strings.TrimSpace(scanner.Text())

		switch option {
		case "1":
			handleOfferRide(client, scanner, driverToken)
		case "2":
			handleListRides(client, driverToken)
		case "3":
			handleCancelRide(client, scanner, driverToken)
		case "4":
			driverToken = promptDriverToken(scanner, driverToken)
			client.Close()
			client, serverAddr, nodeLabel = selectAndConnect(scanner, "")
			fmt.Printf("\n %s[CONECTADO]%s Motorista %s[%s]%s conectado a %s%s (%s)%s\n",
				colorGreen+colorBold, colorReset, colorBold, driverToken, colorReset, colorCyan+colorBold, nodeLabel, serverAddr, colorReset)
		case "0":
			fmt.Printf("\n %sSessao do motorista encerrada com seguranca. Ate logo!%s\n\n", colorCyan+colorBold, colorReset)
			return
		default:
			fmt.Printf("\n %s[!] Opcao invalida. Digite um numero entre 0 e 4.%s\n", colorYellow, colorReset)
		}
	}
}

func handleOfferRide(client *Client, scanner *bufio.Scanner, token string) {
	fmt.Println()
	fmt.Printf("%s+------------------------------------------------------------------------------+%s\n", colorGreen, colorReset)
	fmt.Printf("%s|%s                       %sCADASTRAR NOVA OFERTA DE CARONA%s                        %s|%s\n", colorGreen, colorReset, colorBold+colorWhite, colorReset, colorGreen, colorReset)
	fmt.Printf("%s+------------------------------------------------------------------------------+%s\n", colorGreen, colorReset)

	fmt.Printf(" %s[1/4]%s Cidades da rota (separadas por virgula, ex: Salvador, Feira, Conquista):\n", colorCyan+colorBold, colorReset)
	fmt.Printf("       %s>%s ", colorCyan+colorBold, colorReset)
	if !scanner.Scan() {
		return
	}
	rawCities := strings.Split(scanner.Text(), ",")
	var route []string
	for _, c := range rawCities {
		city := strings.TrimSpace(c)
		if city != "" {
			route = append(route, city)
		}
	}

	if len(route) < 2 {
		fmt.Printf("\n %s[ERRO]%s A rota deve conter pelo menos 2 cidades (Origem e Destino).\n", colorRed+colorBold, colorReset)
		return
	}

	fmt.Printf(" %s[2/4]%s Data da viagem (formato AAAA-MM-DD, ex: 2026-12-15): ", colorCyan+colorBold, colorReset)
	if !scanner.Scan() {
		return
	}
	date := strings.TrimSpace(scanner.Text())

	fmt.Printf(" %s[3/4]%s Horario de partida (formato HH:MM, ex: 08:00): ", colorCyan+colorBold, colorReset)
	if !scanner.Scan() {
		return
	}
	timeStr := strings.TrimSpace(scanner.Text())

	fmt.Printf(" %s[4/4]%s Quantidade de assentos disponiveis no veiculo: ", colorCyan+colorBold, colorReset)
	if !scanner.Scan() {
		return
	}
	seats, err := strconv.Atoi(strings.TrimSpace(scanner.Text()))
	if err != nil || seats <= 0 {
		fmt.Printf("\n %s[ERRO]%s Quantidade de assentos invalida (deve ser maior que zero).\n", colorRed+colorBold, colorReset)
		return
	}

	numSegments := len(route) - 1
	var prices []float64
	fmt.Printf("\n %sInforme o valor individual para cada um dos %d trecho(s):%s\n", colorBold, numSegments, colorReset)

	for i := 0; i < numSegments; i++ {
		fmt.Printf("    - Trecho #%d  %s%-18s -> %-18s%s (R$): ", i+1, colorCyan, route[i], route[i+1], colorReset)
		if !scanner.Scan() {
			return
		}
		priceStr := strings.ReplaceAll(strings.TrimSpace(scanner.Text()), ",", ".")
		price, err := strconv.ParseFloat(priceStr, 64)
		if err != nil || price <= 0 {
			fmt.Printf("\n %s[ERRO]%s Preco invalido. Operacao cancelada.\n", colorRed+colorBold, colorReset)
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
		fmt.Printf("\n %s[ERRO DE COMUNICACAO]%s %v\n", colorRed+colorBold, colorReset, err)
		return
	}

	if resp.Status == "SUCCESS" {
		fmt.Printf("\n %s[OK - CARONA PUBLICADA]%s %s\n", colorGreen+colorBold, colorReset, resp.Message)
	} else {
		fmt.Printf("\n %s[FALHA]%s [%s] %s\n", colorRed+colorBold, colorReset, resp.Code, resp.Message)
	}
}

func handleListRides(client *Client, token string) []*domain.Ride {
	resp, err := client.Send(protocol.ActionListRides, token, nil)
	if err != nil {
		fmt.Printf("\n %s[ERRO]%s Nao foi possivel consultar suas caronas: %v\n", colorRed+colorBold, colorReset, err)
		return nil
	}
	if resp.Status != "SUCCESS" {
		fmt.Printf("\n %s[ERRO]%s %s\n", colorRed+colorBold, colorReset, resp.Message)
		return nil
	}

	var rides []*domain.Ride
	_ = json.Unmarshal([]byte(resp.Data), &rides)

	fmt.Println()
	fmt.Printf("%s+==============================================================================+%s\n", colorCyan, colorReset)
	fmt.Printf("%s|%s                        %sMINHAS CARONAS CADASTRADAS%s                            %s|%s\n", colorCyan, colorReset, colorBold+colorWhite, colorReset, colorCyan, colorReset)
	fmt.Printf("%s+==============================================================================+%s\n", colorCyan, colorReset)

	if len(rides) == 0 {
		fmt.Printf("\n   %sNenhuma carona ativa cadastrada para o motorista [%s] neste servidor.%s\n", colorYellow, token, colorReset)
		return nil
	}

	for idx, ride := range rides {
		routeSummary := strings.Join(ride.Route, " -> ")
		fmt.Println()
		fmt.Printf(" %s+----------------------------------------------------------------------------+%s\n", colorCyan, colorReset)
		fmt.Printf(" %s|%s %sCARONA #%d%s  |  ID: %s%-20s%s  |  Data: %s%s%s  |  Saida: %s%sh%s\n",
			colorCyan, colorReset,
			colorBold+colorWhite, idx+1, colorReset,
			colorGreen+colorBold, ride.ID, colorReset,
			colorBold, ride.Date, colorReset,
			colorBold, ride.Time, colorReset)
		fmt.Printf(" %s|%s Rota Completa: %s%s%s  (Capacidade: %d assentos)\n",
			colorCyan, colorReset, colorCyan+colorBold, routeSummary, colorReset, ride.TotalSeats)
		fmt.Printf(" %s+----+------------------------+------------------------+------------+--------+%s\n", colorCyan, colorReset)
		fmt.Printf(" %s|%s %s# %s %s|%s %sORIGEM                %s %s|%s %sDESTINO               %s %s|%s %sPRECO     %s %s|%s %sVAGAS %s %s|%s\n",
			colorCyan, colorReset, colorBold, colorReset,
			colorCyan, colorReset, colorBold, colorReset,
			colorCyan, colorReset, colorBold, colorReset,
			colorCyan, colorReset, colorBold, colorReset,
			colorCyan, colorReset, colorBold, colorReset,
			colorCyan, colorReset)
		fmt.Printf(" %s+----+------------------------+------------------------+------------+--------+%s\n", colorCyan, colorReset)

		for j, seg := range ride.Segments {
			seatColor := colorGreen
			if seg.AvailableSeats == 0 {
				seatColor = colorRed
			} else if seg.AvailableSeats == 1 {
				seatColor = colorYellow
			}
			vagasStr := fmt.Sprintf("%d/%d", seg.AvailableSeats, ride.TotalSeats)
			precoStr := fmt.Sprintf("R$ %6.2f", seg.Price)

			fmt.Printf(" %s|%s %-2d %s|%s %-22s %s|%s %-22s %s|%s %-10s %s|%s %s%-6s%s %s|%s\n",
				colorCyan, colorReset, j+1,
				colorCyan, colorReset, padRight(seg.From, 22),
				colorCyan, colorReset, padRight(seg.To, 22),
				colorCyan, colorReset, padRight(precoStr, 10),
				colorCyan, colorReset, seatColor+colorBold, padRight(vagasStr, 6), colorReset,
				colorCyan, colorReset)
		}
		fmt.Printf(" %s+----+------------------------+------------------------+------------+--------+%s\n", colorCyan, colorReset)
	}
	return rides
}

func handleCancelRide(client *Client, scanner *bufio.Scanner, token string) {
	rides := handleListRides(client, token)
	if len(rides) == 0 {
		return
	}

	fmt.Printf("\n %s>%s Digite o %sID da Carona%s (ex: %s) ou o %snumero # da lista%s (1..%d) para cancelar [0 = voltar]: ",
		colorYellow+colorBold, colorReset, colorBold, colorReset, rides[0].ID, colorBold, colorReset, len(rides))
	if !scanner.Scan() {
		return
	}
	input := strings.TrimSpace(scanner.Text())
	if input == "" || input == "0" {
		fmt.Printf(" %sOperacao de cancelamento abortada.%s\n", colorDim, colorReset)
		return
	}

	rideID := input
	if idx, err := strconv.Atoi(input); err == nil && idx >= 1 && idx <= len(rides) {
		rideID = rides[idx-1].ID
	}

	cancelPayload := protocol.CancelRideData{RideID: rideID}
	resp, err := client.Send(protocol.ActionCancelRide, token, cancelPayload)
	if err != nil {
		fmt.Printf("\n %s[ERRO DE COMUNICACAO]%s %v\n", colorRed+colorBold, colorReset, err)
		return
	}

	if resp.Status == "SUCCESS" {
		fmt.Printf("\n %s[OK - CARONA CANCELADA]%s %s (Todas as reservas associadas no consorcio foram notificadas!)\n",
			colorGreen+colorBold, colorReset, resp.Message)
	} else {
		fmt.Printf("\n %s[FALHA]%s [%s] %s\n", colorRed+colorBold, colorReset, resp.Code, resp.Message)
	}
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
