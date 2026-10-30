package cluster

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/Figueredo-D/PBL_REDES/internal/domain"
	"github.com/Figueredo-D/PBL_REDES/internal/protocol"
)

// httpPeerBases mapeia o ID de cada nó para a base da sua API REST interna padrão.
// Estes endereços funcionam como fallback quando não forem fornecidos via flag ou variáveis de ambiente.
var httpPeerBases = map[string]string{
	"SERVER_A": "http://localhost:9080",
	"SERVER_B": "http://localhost:9081",
	"SERVER_C": "http://localhost:9082",
}

// restClient tem timeout curto para que a queda de um nó não trave a busca nem o 2PC.
var restClient = &http.Client{Timeout: 5 * time.Second}

// apiErrorBody é o formato de erro definido no contrato da API interna.
type apiErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// internalDecisionResponse é a resposta de GET /internal/transactions/decision.
type internalDecisionResponse struct {
	TransactionID string `json:"transaction_id"`
	Decision      string `json:"decision"`
	CoordinatorID string `json:"coordinator_id"`
}

// normalizeBaseURL garante que o endereço do nó tenha prefixo http:// ou https:// e remove barras finais.
func normalizeBaseURL(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimRight(raw, "/")
	if raw == "" {
		return ""
	}
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		return "http://" + raw
	}
	return raw
}

// GetPeerBase descobre a URL base da API REST interna de um nó parceiro do consórcio.
// Ordem de precedência:
// 1. Configuração explícita passada no mapa cm.Peers
// 2. Variáveis de ambiente específicas (ex: PEER_SERVER_A_URL, SERVER_A_URL, PEER_A_URL)
// 3. Fallback do mapa estático padrão (localhost)
func (cm *ClusterManager) GetPeerBase(peerID string) (string, error) {
	if cm != nil && cm.Peers != nil {
		if raw, ok := cm.Peers[peerID]; ok && strings.TrimSpace(raw) != "" {
			return normalizeBaseURL(raw), nil
		}
	}

	envKeys := []string{
		fmt.Sprintf("PEER_%s_URL", peerID),
		fmt.Sprintf("%s_URL", peerID),
	}
	if strings.HasPrefix(peerID, "SERVER_") {
		short := strings.TrimPrefix(peerID, "SERVER_")
		envKeys = append(envKeys, fmt.Sprintf("PEER_%s_URL", short), fmt.Sprintf("%s_URL", short))
	}

	for _, k := range envKeys {
		if val := strings.TrimSpace(os.Getenv(k)); val != "" {
			return normalizeBaseURL(val), nil
		}
	}

	if base, ok := httpPeerBases[peerID]; ok && base != "" {
		return normalizeBaseURL(base), nil
	}

	return "", fmt.Errorf("peer %s sem endereço REST configurado (defina via flag ou variável PEER_%s_URL)", peerID, peerID)
}

// SetPeerBase atualiza programaticamente a URL base de um peer no cluster manager.
func (cm *ClusterManager) SetPeerBase(peerID, baseURL string) {
	if cm.Peers == nil {
		cm.Peers = make(map[string]string)
	}
	cm.Peers[peerID] = normalizeBaseURL(baseURL)
}

// ListRidesByDateREST consulta GET /internal/rides no peer informado e devolve as caronas da data.
func (cm *ClusterManager) ListRidesByDateREST(peerID, date string) ([]*domain.Ride, error) {
	base, err := cm.GetPeerBase(peerID)
	if err != nil {
		return nil, fmt.Errorf("peer %s sem endereço REST: %w", peerID, err)
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
		_ = json.Unmarshal(body, &apiErr)
		return nil, fmt.Errorf("%s respondeu %d (%s): %s", peerID, resp.StatusCode, apiErr.Code, apiErr.Message)
	}

	var rides []*domain.Ride
	if err := json.Unmarshal(body, &rides); err != nil {
		return nil, fmt.Errorf("resposta malformada de %s: %w", peerID, err)
	}

	return rides, nil
}

// postJSON envia um POST com corpo JSON para o peer e devolve o status HTTP e o envelope de resposta.
func (cm *ClusterManager) postJSON(peerID, path string, payload interface{}) (int, protocol.Response, error) {
	base, err := cm.GetPeerBase(peerID)
	if err != nil {
		return 0, protocol.Response{}, fmt.Errorf("peer %s sem endereço REST: %w", peerID, err)
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return 0, protocol.Response{}, err
	}

	resp, err := restClient.Post(base+path, "application/json", bytes.NewReader(body))
	if err != nil {
		return 0, protocol.Response{}, fmt.Errorf("falha ao chamar %s em %s: %w", peerID, path, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, protocol.Response{}, fmt.Errorf("falha ao ler resposta de %s: %w", peerID, err)
	}

	var out protocol.Response
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			return resp.StatusCode, protocol.Response{}, fmt.Errorf("resposta malformada de %s: %w", peerID, err)
		}
	}

	return resp.StatusCode, out, nil
}

// PrepareREST envia POST /internal/prepare ao peer e devolve seu voto.
func (cm *ClusterManager) PrepareREST(peerID string, data protocol.TwoPCPrepareData) (protocol.Response, error) {
	status, resp, err := cm.postJSON(peerID, "/internal/prepare", data)
	if err != nil {
		return protocol.Response{}, err
	}
	if status != http.StatusOK && status != http.StatusConflict {
		return protocol.Response{}, fmt.Errorf("%s respondeu %d no prepare", peerID, status)
	}
	return resp, nil
}

// CommitREST envia POST /internal/commit ao peer para efetivar a transação.
func (cm *ClusterManager) CommitREST(peerID, transactionID string) error {
	status, _, err := cm.postJSON(peerID, "/internal/commit", map[string]string{
		"transaction_id": transactionID,
	})
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("%s respondeu %d no commit", peerID, status)
	}
	return nil
}

// AbortREST envia POST /internal/abort ao peer para desfazer a transação e devolver as vagas reservadas.
func (cm *ClusterManager) AbortREST(peerID, transactionID string) error {
	status, _, err := cm.postJSON(peerID, "/internal/abort", map[string]string{
		"transaction_id": transactionID,
	})
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("%s respondeu %d no abort", peerID, status)
	}
	return nil
}

// ReleaseREST envia POST /internal/release ao peer para devolver as vagas locais
// de uma reserva cancelada. A operação deve ser idempotente por BookingID.
func (cm *ClusterManager) ReleaseREST(peerID string, data protocol.ReleaseSeatsData) error {
	status, resp, err := cm.postJSON(peerID, "/internal/release", data)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("%s respondeu %d ao devolver vagas: %s", peerID, status, resp.Message)
	}
	return nil
}

// DecisionREST pergunta ao coordenador o desfecho de uma transação via GET /internal/transactions/decision.
// Devolve COMMIT, ABORT, IN_PROGRESS ou UNKNOWN. Se o coordenador não responder, devolve erro,
// e o participante deve manter a transação pendente e tentar novamente mais tarde.
func (cm *ClusterManager) DecisionREST(coordinatorID, transactionID string) (string, error) {
	base, err := cm.GetPeerBase(coordinatorID)
	if err != nil {
		return "", fmt.Errorf("coordenador %s sem endereço REST: %w", coordinatorID, err)
	}

	endpoint := base + "/internal/transactions/decision?transaction_id=" + url.QueryEscape(transactionID)
	resp, err := restClient.Get(endpoint)
	if err != nil {
		return "", fmt.Errorf("falha ao consultar decisão em %s: %w", coordinatorID, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("falha ao ler decisão de %s: %w", coordinatorID, err)
	}

	if resp.StatusCode != http.StatusOK {
		var apiErr apiErrorBody
		_ = json.Unmarshal(body, &apiErr)
		return "", fmt.Errorf("%s respondeu %d na decisão (%s): %s", coordinatorID, resp.StatusCode, apiErr.Code, apiErr.Message)
	}

	var out internalDecisionResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("resposta de decisão malformada de %s: %w", coordinatorID, err)
	}
	if out.Decision == "" {
		return "", fmt.Errorf("%s não informou a decisão da transação %s", coordinatorID, transactionID)
	}

	return out.Decision, nil
}
