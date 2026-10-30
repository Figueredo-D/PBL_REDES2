package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/Figueredo-D/PBL_REDES/internal/domain"
	"github.com/Figueredo-D/PBL_REDES/internal/protocol"
)

type tcpClient struct {
	conn   net.Conn
	reader *bufio.Reader
}

type rideListItem struct {
	ID       string `json:"id"`
	Segments []struct {
		From           string `json:"from"`
		To             string `json:"to"`
		AvailableSeats int    `json:"available_seats"`
	} `json:"segments"`
}

var httpClient = &http.Client{Timeout: 5 * time.Second}

func main() {
	fmt.Println("=== Teste automatizado de cancelamento de reservas ===")
	fmt.Println("O teste usa os servidores A e B já em execução.")
	fmt.Println()

	if err := runTests(); err != nil {
		fmt.Printf("\nFALHA NO TESTE: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("\nRESULTADO: todos os testes passaram.")
}

func runTests() error {
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	date := time.Now().AddDate(0, 0, 30).Format("2006-01-02")
	driverA := "stress_driver_A_" + suffix
	driverB := "stress_driver_B_" + suffix
	passenger := "stress_passenger_" + suffix
	otherPassenger := "stress_other_passenger_" + suffix

	fmt.Println("1. Testando reserva e cancelamento de carona local no Servidor A...")
	localBookingID, localRideID, err := testLocalBooking(
		date,
		suffix,
		driverA,
		passenger,
		otherPassenger,
	)
	if err != nil {
		return fmt.Errorf("teste local: %w", err)
	}
	fmt.Printf("   PASSOU: carona %s, reserva %s\n", localRideID, localBookingID)

	fmt.Println("2. Testando reserva e cancelamento de itinerário entre A e B...")
	combinedBookingID, rideAID, rideBID, err := testCombinedBooking(
		date,
		suffix,
		driverA,
		driverB,
		passenger,
	)
	if err != nil {
		return fmt.Errorf("teste combinado: %w", err)
	}
	fmt.Printf("   PASSOU: caronas %s e %s, reserva %s\n", rideAID, rideBID, combinedBookingID)

	return nil
}

func testLocalBooking(
	date string,
	suffix string,
	driverToken string,
	passengerToken string,
	otherPassengerToken string,
) (bookingID, rideID string, err error) {
	client, err := dialTCP("localhost:8080")
	if err != nil {
		return "", "", fmt.Errorf("conectando ao Servidor A: %w", err)
	}
	defer client.Close()

	origin := "StressLocalOrigin" + suffix
	destination := "StressLocalDestination" + suffix

	offer := protocol.OfferRideData{
		Route:         []string{origin, destination},
		Date:          date,
		Time:          "10:00",
		TotalSeats:    2,
		SegmentPrices: []float64{10},
	}

	resp, err := client.send(protocol.ActionOfferRide, driverToken, offer)
	if err != nil {
		return "", "", fmt.Errorf("ofertando carona local: %w", err)
	}
	if resp.Status != "SUCCESS" {
		return "", "", fmt.Errorf("oferta local recusada: %s (%s)", resp.Message, resp.Code)
	}

	rideID, err = rideIDFromResponse(resp)
	if err != nil {
		return "", "", err
	}

	if err := assertSeats("SERVER_A", rideID, origin, destination, 2); err != nil {
		return "", "", fmt.Errorf("vagas iniciais da carona local: %w", err)
	}

	itinerary, err := searchItinerary(client, passengerToken, origin, destination, date)
	if err != nil {
		return "", "", fmt.Errorf("buscando carona local: %w", err)
	}

	resp, err = client.send(protocol.ActionBookItinerary, passengerToken, itinerary)
	if err != nil {
		return "", "", fmt.Errorf("reservando carona local: %w", err)
	}
	if resp.Status != "SUCCESS" {
		return "", "", fmt.Errorf("reserva local recusada: %s (%s)", resp.Message, resp.Code)
	}

	bookingID, err = bookingIDFromResponse(resp)
	if err != nil {
		return "", "", err
	}

	if err := assertSeats("SERVER_A", rideID, origin, destination, 1); err != nil {
		return "", "", fmt.Errorf("vagas após reserva local: %w", err)
	}

	if err := cancelBooking(client, passengerToken, bookingID); err != nil {
		return "", "", fmt.Errorf("cancelando reserva local: %w", err)
	}

	if err := assertBookingStatus(client, passengerToken, bookingID, "CANCELLED"); err != nil {
		return "", "", fmt.Errorf("status da reserva local: %w", err)
	}
	if err := assertSeats("SERVER_A", rideID, origin, destination, 2); err != nil {
		return "", "", fmt.Errorf("vagas após cancelar reserva local: %w", err)
	}

	// Um segundo cancelamento deve falhar e não pode devolver vaga novamente.
	resp, err = client.send(
		protocol.ActionCancelBooking,
		passengerToken,
		protocol.CancelBookingData{BookingID: bookingID},
	)
	if err != nil {
		return "", "", fmt.Errorf("tentando cancelar novamente: %w", err)
	}
	if resp.Status == "SUCCESS" {
		return "", "", fmt.Errorf("o segundo cancelamento foi aceito; esperava-se erro")
	}
	if err := assertSeats("SERVER_A", rideID, origin, destination, 2); err != nil {
		return "", "", fmt.Errorf("vagas após segundo cancelamento: %w", err)
	}

	// Outro passageiro não pode cancelar a reserva.
	resp, err = client.send(
		protocol.ActionCancelBooking,
		otherPassengerToken,
		protocol.CancelBookingData{BookingID: bookingID},
	)
	if err != nil {
		return "", "", fmt.Errorf("tentativa de cancelamento por outro passageiro: %w", err)
	}
	if resp.Status == "SUCCESS" {
		return "", "", fmt.Errorf("outro passageiro conseguiu cancelar a reserva")
	}
	if err := assertSeats("SERVER_A", rideID, origin, destination, 2); err != nil {
		return "", "", fmt.Errorf("vagas após tentativa não autorizada: %w", err)
	}

	return bookingID, rideID, nil
}

func testCombinedBooking(
	date string,
	suffix string,
	driverAToken string,
	driverBToken string,
	passengerToken string,
) (bookingID, rideAID, rideBID string, err error) {
	clientA, err := dialTCP("localhost:8080")
	if err != nil {
		return "", "", "", fmt.Errorf("conectando ao Servidor A: %w", err)
	}
	defer clientA.Close()

	clientB, err := dialTCP("localhost:8081")
	if err != nil {
		return "", "", "", fmt.Errorf("conectando ao Servidor B: %w", err)
	}
	defer clientB.Close()

	origin := "StressComboOrigin" + suffix
	connection := "StressComboConnection" + suffix
	destination := "StressComboDestination" + suffix

	offerA := protocol.OfferRideData{
		Route:         []string{origin, connection},
		Date:          date,
		Time:          "10:00",
		TotalSeats:    2,
		SegmentPrices: []float64{20},
	}
	resp, err := clientA.send(protocol.ActionOfferRide, driverAToken, offerA)
	if err != nil {
		return "", "", "", fmt.Errorf("ofertando trecho no Servidor A: %w", err)
	}
	if resp.Status != "SUCCESS" {
		return "", "", "", fmt.Errorf("oferta no Servidor A recusada: %s (%s)", resp.Message, resp.Code)
	}
	rideAID, err = rideIDFromResponse(resp)
	if err != nil {
		return "", "", "", err
	}

	offerB := protocol.OfferRideData{
		Route:         []string{connection, destination},
		Date:          date,
		Time:          "11:00",
		TotalSeats:    2,
		SegmentPrices: []float64{25},
	}
	resp, err = clientB.send(protocol.ActionOfferRide, driverBToken, offerB)
	if err != nil {
		return "", "", "", fmt.Errorf("ofertando trecho no Servidor B: %w", err)
	}
	if resp.Status != "SUCCESS" {
		return "", "", "", fmt.Errorf("oferta no Servidor B recusada: %s (%s)", resp.Message, resp.Code)
	}
	rideBID, err = rideIDFromResponse(resp)
	if err != nil {
		return "", "", "", err
	}

	if err := assertSeats("SERVER_A", rideAID, origin, connection, 2); err != nil {
		return "", "", "", fmt.Errorf("vagas iniciais no Servidor A: %w", err)
	}
	if err := assertSeats("SERVER_B", rideBID, connection, destination, 2); err != nil {
		return "", "", "", fmt.Errorf("vagas iniciais no Servidor B: %w", err)
	}

	// A busca é feita pelo Servidor A; ela deve encontrar também a carona publicada em B.
	itinerary, err := searchItinerary(clientA, passengerToken, origin, destination, date)
	if err != nil {
		return "", "", "", fmt.Errorf("buscando itinerário combinado: %w", err)
	}
	if len(itinerary.Segments) != 2 {
		return "", "", "", fmt.Errorf("esperava 2 trechos no itinerário combinado, recebeu %d", len(itinerary.Segments))
	}

	resp, err = clientA.send(protocol.ActionBookItinerary, passengerToken, itinerary)
	if err != nil {
		return "", "", "", fmt.Errorf("reservando itinerário combinado: %w", err)
	}
	if resp.Status != "SUCCESS" {
		return "", "", "", fmt.Errorf("reserva combinada recusada: %s (%s)", resp.Message, resp.Code)
	}

	bookingID, err = bookingIDFromResponse(resp)
	if err != nil {
		return "", "", "", err
	}

	if err := assertSeats("SERVER_A", rideAID, origin, connection, 1); err != nil {
		return "", "", "", fmt.Errorf("vagas no Servidor A após reserva: %w", err)
	}
	if err := assertSeats("SERVER_B", rideBID, connection, destination, 1); err != nil {
		return "", "", "", fmt.Errorf("vagas no Servidor B após reserva: %w", err)
	}

	if err := cancelBooking(clientA, passengerToken, bookingID); err != nil {
		return "", "", "", fmt.Errorf("cancelando reserva combinada: %w", err)
	}

	if err := assertBookingStatus(clientA, passengerToken, bookingID, "CANCELLED"); err != nil {
		return "", "", "", fmt.Errorf("status da reserva combinada: %w", err)
	}
	if err := assertSeats("SERVER_A", rideAID, origin, connection, 2); err != nil {
		return "", "", "", fmt.Errorf("vagas no Servidor A após cancelamento: %w", err)
	}
	if err := assertSeats("SERVER_B", rideBID, connection, destination, 2); err != nil {
		return "", "", "", fmt.Errorf("vagas no Servidor B após cancelamento: %w", err)
	}

	return bookingID, rideAID, rideBID, nil
}

func dialTCP(address string) (*tcpClient, error) {
	conn, err := net.DialTimeout("tcp", address, 5*time.Second)
	if err != nil {
		return nil, err
	}

	return &tcpClient{
		conn:   conn,
		reader: bufio.NewReader(conn),
	}, nil
}

func (c *tcpClient) Close() {
	if c.conn != nil {
		_ = c.conn.Close()
	}
}

func (c *tcpClient) send(action, token string, payload interface{}) (protocol.Response, error) {
	var payloadJSON string
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return protocol.Response{}, err
		}
		payloadJSON = string(data)
	}

	request := protocol.Request{
		Action: action,
		Token:  token,
		Data:   payloadJSON,
	}

	data, err := json.Marshal(request)
	if err != nil {
		return protocol.Response{}, err
	}
	data = append(data, '\n')

	_ = c.conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := c.conn.Write(data); err != nil {
		return protocol.Response{}, err
	}

	line, err := c.reader.ReadBytes('\n')
	if err != nil {
		return protocol.Response{}, err
	}

	var response protocol.Response
	if err := json.Unmarshal(line, &response); err != nil {
		return protocol.Response{}, err
	}

	return response, nil
}

func searchItinerary(
	client *tcpClient,
	passengerToken string,
	origin string,
	destination string,
	date string,
) (domain.ItineraryResult, error) {
	payload := protocol.SearchItineraryData{
		Origin:      origin,
		Destination: destination,
		Date:        date,
	}

	resp, err := client.send(protocol.ActionSearchItinerary, passengerToken, payload)
	if err != nil {
		return domain.ItineraryResult{}, err
	}
	if resp.Status != "SUCCESS" {
		return domain.ItineraryResult{}, fmt.Errorf("busca recusada: %s (%s)", resp.Message, resp.Code)
	}

	var itineraries []domain.ItineraryResult
	if err := json.Unmarshal([]byte(resp.Data), &itineraries); err != nil {
		return domain.ItineraryResult{}, fmt.Errorf("resposta de busca inválida: %w", err)
	}

	for _, itinerary := range itineraries {
		if len(itinerary.Segments) > 0 &&
			itinerary.Segments[0].From == origin &&
			itinerary.Segments[len(itinerary.Segments)-1].To == destination {
			return itinerary, nil
		}
	}

	return domain.ItineraryResult{}, fmt.Errorf("nenhum itinerário encontrado de %s até %s", origin, destination)
}

func cancelBooking(client *tcpClient, passengerToken string, bookingID string) error {
	resp, err := client.send(
		protocol.ActionCancelBooking,
		passengerToken,
		protocol.CancelBookingData{BookingID: bookingID},
	)
	if err != nil {
		return err
	}
	if resp.Status != "SUCCESS" {
		return fmt.Errorf("cancelamento recusado: %s (%s)", resp.Message, resp.Code)
	}
	return nil
}

func assertBookingStatus(
	client *tcpClient,
	passengerToken string,
	bookingID string,
	expectedStatus string,
) error {
	resp, err := client.send(protocol.ActionListBookings, passengerToken, nil)
	if err != nil {
		return err
	}
	if resp.Status != "SUCCESS" {
		return fmt.Errorf("consulta de reservas falhou: %s", resp.Message)
	}

	var bookings []domain.Booking
	if err := json.Unmarshal([]byte(resp.Data), &bookings); err != nil {
		return fmt.Errorf("lista de reservas inválida: %w", err)
	}

	for _, booking := range bookings {
		if booking.ID == bookingID {
			if booking.Status != expectedStatus {
				return fmt.Errorf("reserva %s está com status %q; esperado %q",
					bookingID, booking.Status, expectedStatus)
			}
			return nil
		}
	}

	return fmt.Errorf("reserva %s não encontrada na lista do passageiro", bookingID)
}

func assertSeats(
	serverID string,
	rideID string,
	from string,
	to string,
	expected int,
) error {
	baseURL, ok := map[string]string{
		"SERVER_A": "http://localhost:9080",
		"SERVER_B": "http://localhost:9081",
		"SERVER_C": "http://localhost:9082",
	}[serverID]
	if !ok {
		return fmt.Errorf("servidor desconhecido: %s", serverID)
	}

	date := time.Now().AddDate(0, 0, 30).Format("2006-01-02")
	endpoint := baseURL + "/internal/rides?date=" + url.QueryEscape(date)

	resp, err := httpClient.Get(endpoint)
	if err != nil {
		return fmt.Errorf("consultando API REST de %s: %w", serverID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("API REST de %s respondeu HTTP %d", serverID, resp.StatusCode)
	}

	var rides []rideListItem
	if err := json.NewDecoder(resp.Body).Decode(&rides); err != nil {
		return fmt.Errorf("resposta de caronas inválida de %s: %w", serverID, err)
	}

	for _, ride := range rides {
		if ride.ID != rideID {
			continue
		}
		for _, segment := range ride.Segments {
			if segment.From == from && segment.To == to {
				if segment.AvailableSeats != expected {
					return fmt.Errorf(
						"carona %s (%s -> %s) tem %d vagas; esperado %d",
						rideID,
						from,
						to,
						segment.AvailableSeats,
						expected,
					)
				}
				return nil
			}
		}
		return fmt.Errorf("trecho %s -> %s não encontrado na carona %s", from, to, rideID)
	}

	return fmt.Errorf("carona %s não encontrada em %s", rideID, serverID)
}

func rideIDFromResponse(resp protocol.Response) (string, error) {
	var result struct {
		RideID string `json:"ride_id"`
	}
	if err := json.Unmarshal([]byte(resp.Data), &result); err != nil {
		return "", fmt.Errorf("resposta de oferta inválida: %w", err)
	}
	if strings.TrimSpace(result.RideID) == "" {
		return "", fmt.Errorf("a resposta de oferta não contém ride_id")
	}
	return result.RideID, nil
}

func bookingIDFromResponse(resp protocol.Response) (string, error) {
	var result struct {
		BookingID string `json:"booking_id"`
	}
	if err := json.Unmarshal([]byte(resp.Data), &result); err != nil {
		return "", fmt.Errorf("resposta de reserva inválida: %w", err)
	}
	if strings.TrimSpace(result.BookingID) == "" {
		return "", fmt.Errorf("a resposta de reserva não contém booking_id")
	}
	return result.BookingID, nil
}
