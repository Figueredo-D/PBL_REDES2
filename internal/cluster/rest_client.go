package cluster

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/Figueredo-D/PBL_REDES/internal/domain"
)

// httpPeerBases mapeia o ID de cada nó para a base da sua API REST interna.
// Estes endereços serão trocados por variáveis de ambiente na etapa do Docker.
var httpPeerBases = map[string]string{
	"SERVER_A": "http://localhost:9080",
	"SERVER_B": "http://localhost:9081",
	"SERVER_C": "http://localhost:9082",
}

// restClient tem timeout curto para que a queda de um nó não trave a busca.
var restClient = &http.Client{Timeout: 3 * time.Second}

// apiErrorBody é o formato de erro definido no contrato da API interna.
type apiErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ListRidesByDateREST consulta GET /internal/rides no peer informado e devolve as caronas da data.
func (cm *ClusterManager) ListRidesByDateREST(peerID, date string) ([]*domain.Ride, error) {
	base, ok := httpPeerBases[peerID]
	if !ok {
		return nil, fmt.Errorf("peer %s sem endereço REST configurado", peerID)
	}

	endpoint := base + "/internal/rides?date=" + url.QueryEscape(date)
	resp, err := restClient.Get(endpoint)
	if err != nil {
		return nil, fmt.Errorf("falha ao consultar %s: %w", peerID, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("falha ao ler resposta de %s: %w", peerID, err)
	}

	if resp.StatusCode != http.StatusOK {
		var apiErr apiErrorBody
		json.Unmarshal(body, &apiErr)
		return nil, fmt.Errorf("%s respondeu %d (%s): %s", peerID, resp.StatusCode, apiErr.Code, apiErr.Message)
	}

	var rides []*domain.Ride
	if err := json.Unmarshal(body, &rides); err != nil {
		return nil, fmt.Errorf("resposta malformada de %s: %w", peerID, err)
	}

	return rides, nil
}
