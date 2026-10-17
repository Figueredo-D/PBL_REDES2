package cluster

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"time"

	"github.com/Figueredo-D/PBL_REDES/internal/protocol"
)

// PeerNode representa um servidor parceiro no consórcio.
type PeerNode struct {
	ID      string
	Address string
}

// ClusterManager gere as conexões e o envio de mensagens para os outros nós.
type ClusterManager struct {
	SelfID string
	Peers  map[string]string // Mapeia ServerID -> Endereço TCP (ex: "SERVER_B": "localhost:8081")
}

// NewClusterManager inicializa o gestor do cluster com a lista de peers.
func NewClusterManager(selfID string, peers map[string]string) *ClusterManager {
	return &ClusterManager{
		SelfID: selfID,
		Peers:  peers,
	}
}

// SendMessage envia uma requisição TCP para um nó específico do consórcio e aguarda a resposta.
func (cm *ClusterManager) SendMessage(targetID string, req protocol.Request) (protocol.Response, error) {
	addr, exists := cm.Peers[targetID]
	if !exists {
		return protocol.Response{}, fmt.Errorf("peer %s não encontrado na configuração do cluster", targetID)
	}

	// Estabelece ligação TCP com o servidor parceiro com timeout de 3 segundos
	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		return protocol.Response{}, fmt.Errorf("falha ao conectar com %s em %s: %w", targetID, addr, err)
	}
	defer conn.Close()

	// Codifica e envia a requisição
	data, err := json.Marshal(req)
	if err != nil {
		return protocol.Response{}, err
	}
	data = append(data, '\n')

	_, err = conn.Write(data)
	if err != nil {
		return protocol.Response{}, fmt.Errorf("falha ao enviar dados para %s: %w", targetID, err)
	}

	// Lê a resposta do servidor parceiro
	reader := bufio.NewReader(conn)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return protocol.Response{}, fmt.Errorf("falha ao ler resposta de %s: %w", targetID, err)
	}

	var resp protocol.Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return protocol.Response{}, fmt.Errorf("resposta malformada de %s: %w", targetID, err)
	}

	return resp, nil
}