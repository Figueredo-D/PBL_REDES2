package protocol

const (
	ActionAuthenticate    = "AUTHENTICATE"
	ActionOfferRide       = "OFFER_RIDE"
	ActionListRides       = "LIST_RIDES"
	ActionCancelRide      = "CANCEL_RIDE"
	ActionSearchItinerary = "SEARCH_ITINERARY"
	ActionBookItinerary   = "BOOK_ITINERARY"
	ActionCancelBooking   = "CANCEL_BOOKING"
	ActionListBookings    = "LIST_BOOKINGS"

	// Ações do protocolo Two-Phase Commit (2PC) entre os nós do consórcio
	Action2PCPrepare = "2PC_PREPARE"
	Action2PCCommit  = "2PC_COMMIT"
	Action2PCAbort   = "2PC_ABORT"
)

type Request struct {
	Action string `json:"action"`
	Token  string `json:"token,omitempty"`
	Data   string `json:"data,omitempty"`
}

type Response struct {
	Status  string `json:"status"` // "SUCCESS", "ERROR", "VOTE_COMMIT", "VOTE_ABORT"
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
	Data    string `json:"data,omitempty"`
}

type OfferRideData struct {
	Route         []string  `json:"route"`
	Date          string    `json:"date"`
	Time          string    `json:"time"`
	TotalSeats    int       `json:"total_seats"`
	SegmentPrices []float64 `json:"segment_prices"`
}

type CancelRideData struct {
	RideID string `json:"ride_id"`
}

type SearchItineraryData struct {
	Origin      string `json:"origin"`
	Destination string `json:"destination"`
	Date        string `json:"date"`
}

type BookItineraryData struct {
	ItineraryID string   `json:"itinerary_id"`
	SegmentIDs  []string `json:"segment_ids,omitempty"`
}

type CancelBookingData struct {
	BookingID string `json:"booking_id"`
}

// Payload da Fase 1 do 2PC (Prepare). LamportTime e CoordinatorID definem a precedência
// da transação na fila de cada participante: menor timestamp primeiro, desempate pelo ID do coordenador.
type TwoPCPrepareData struct {
	TransactionID string          `json:"transaction_id"`
	LamportTime   int64           `json:"lamport_time"`
	CoordinatorID string          `json:"coordinator_id"`
	Itinerary     ItineraryOption `json:"itinerary"`
}

type SegmentInfo struct {
	RideID         string  `json:"ride_id"`
	CompanyID      string  `json:"company_id"` // Empresa (nó) dona deste trecho, usada pelo peer para decidir se o trecho é local
	DriverToken    string  `json:"driver_token"`
	From           string  `json:"from"`
	To             string  `json:"to"`
	DepartureTime  string  `json:"departure_time"`
	Price          float64 `json:"price"`
	AvailableSeats int     `json:"available_seats"`
}

type ItineraryOption struct {
	ItineraryID    string        `json:"itinerary_id"`
	Segments       []SegmentInfo `json:"segments"`
	TotalPrice     float64       `json:"total_price"`
	DepartureTime  string        `json:"departure_time"`
	AvailableSeats int           `json:"available_seats"`
	Drivers        []string      `json:"drivers"`
}

type Booking struct {
	ID             string          `json:"id"`
	PassengerToken string          `json:"passenger_token"`
	Itinerary      ItineraryOption `json:"itinerary"`
	Date           string          `json:"date"`
	Status         string          `json:"status"` // "CONFIRMED", "CANCELLED"
}
