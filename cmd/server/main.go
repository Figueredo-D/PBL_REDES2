package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/Figueredo-D/PBL_REDES/internal/server"
)

// formatPort garante que a porta tenha dois-pontos no início caso venha apenas como "8080".
func formatPort(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if !strings.Contains(p, ":") {
		return ":" + p
	}
	return p
}

// parsePeersFlag converte strings como "SERVER_B=http://host:9081,SERVER_C=http://host:9082" em mapa.
func parsePeersFlag(raw string) map[string]string {
	result := make(map[string]string)
	entries := strings.Split(raw, ",")
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) == 2 {
			id := strings.TrimSpace(parts[0])
			addr := strings.TrimSpace(parts[1])
			if id != "" && addr != "" {
				result[id] = addr
			}
		}
	}
	return result
}

func main() {
	flagID := flag.String("id", "", "ID do servidor no consórcio (SERVER_A, SERVER_B, SERVER_C)")
	flagPort := flag.String("port", "", "Porta TCP para conexão dos clientes (ex: 8080 ou :8080)")
	flagInternalPort := flag.String("internal-port", "", "Porta HTTP da API REST interna (ex: 9080 ou :9080)")
	flagPeers := flag.String("peers", "", "Lista de peers no formato SERVER_B=http://host:9081,SERVER_C=http://host:9082")
	flagDataDir := flag.String("data-dir", "", "Diretório base para persistência dos dados (padrão: data)")
	flag.Parse()

	// 1. Identificador do Servidor (Precedência: Flag -> os.Args[1] -> ENV SERVER_ID)
	serverID := strings.TrimSpace(*flagID)
	if serverID == "" && flag.NArg() > 0 {
		serverID = strings.TrimSpace(flag.Arg(0))
	}
	if serverID == "" {
		serverID = strings.TrimSpace(os.Getenv("SERVER_ID"))
	}
	if serverID == "" {
		fmt.Println("Erro: ID do servidor não especificado!")
		fmt.Println("\nFormas de uso:")
		fmt.Println("  go run cmd/server/main.go <SERVER_ID>")
		fmt.Println("  go run cmd/server/main.go -id <SERVER_ID> [-port 8080] [-internal-port 9080] [-peers ...]")
		fmt.Println("  SERVER_ID=<SERVER_ID> go run cmd/server/main.go")
		fmt.Println("\nExemplos:")
		fmt.Println("  go run cmd/server/main.go SERVER_A")
		fmt.Println("  go run cmd/server/main.go -id SERVER_A -port 8080 -internal-port 9080 -peers SERVER_B=http://192.168.1.11:9081,SERVER_C=http://192.168.1.12:9082")
		os.Exit(1)
	}

	serverID = strings.ToUpper(serverID)

	// Portas padrão locais por nó
	defaultTCPPorts := map[string]string{
		"SERVER_A": ":8080",
		"SERVER_B": ":8081",
		"SERVER_C": ":8082",
	}

	defaultInternalPorts := map[string]string{
		"SERVER_A": ":9080",
		"SERVER_B": ":9081",
		"SERVER_C": ":9082",
	}

	defaultPeerBases := map[string]string{
		"SERVER_A": "http://localhost:9080",
		"SERVER_B": "http://localhost:9081",
		"SERVER_C": "http://localhost:9082",
	}

	// 2. Porta TCP do Cliente (Precedência: Flag -> ENV CLIENT_PORT / TCP_PORT / PORT -> Default)
	port := formatPort(*flagPort)
	if port == "" {
		port = formatPort(os.Getenv("CLIENT_PORT"))
	}
	if port == "" {
		port = formatPort(os.Getenv("TCP_PORT"))
	}
	if port == "" {
		port = formatPort(os.Getenv("PORT"))
	}
	if port == "" {
		port = defaultTCPPorts[serverID]
	}
	if port == "" {
		port = ":8080"
	}

	// 3. Porta da API REST Interna (Precedência: Flag -> ENV INTERNAL_PORT / REST_PORT -> Default)
	internalPort := formatPort(*flagInternalPort)
	if internalPort == "" {
		internalPort = formatPort(os.Getenv("INTERNAL_PORT"))
	}
	if internalPort == "" {
		internalPort = formatPort(os.Getenv("REST_PORT"))
	}
	if internalPort == "" {
		internalPort = defaultInternalPorts[serverID]
	}
	if internalPort == "" {
		internalPort = ":9080"
	}

	// 4. Configuração dos Peers do Consórcio
	peers := make(map[string]string)

	// Inicia com os nós padrão do consórcio exceto o próprio nó
	for peerID, defaultBase := range defaultPeerBases {
		if peerID != serverID {
			peers[peerID] = defaultBase
		}
	}

	// Sobrescreve com flag -peers se fornecida
	if *flagPeers != "" {
		parsed := parsePeersFlag(*flagPeers)
		for id, addr := range parsed {
			if id != serverID {
				peers[id] = addr
			}
		}
	} else if envPeers := os.Getenv("PEERS"); envPeers != "" {
		parsed := parsePeersFlag(envPeers)
		for id, addr := range parsed {
			if id != serverID {
				peers[id] = addr
			}
		}
	}

	// Checa variáveis de ambiente individuais (ex: PEER_B_URL, SERVER_B_URL, etc.)
	allKnownNodes := []string{"SERVER_A", "SERVER_B", "SERVER_C"}
	for _, node := range allKnownNodes {
		if node == serverID {
			continue
		}
		short := strings.TrimPrefix(node, "SERVER_")
		candidates := []string{
			fmt.Sprintf("PEER_%s_URL", node),
			fmt.Sprintf("%s_URL", node),
			fmt.Sprintf("PEER_%s_URL", short),
			fmt.Sprintf("%s_URL", short),
		}
		for _, c := range candidates {
			if v := strings.TrimSpace(os.Getenv(c)); v != "" {
				peers[node] = v
				break
			}
		}
	}

	// 5. Diretório de Dados
	dataDir := strings.TrimSpace(*flagDataDir)
	if dataDir == "" {
		dataDir = strings.TrimSpace(os.Getenv("DATA_DIR"))
	}
	if dataDir == "" {
		dataDir = "data"
	}
	dataPath := filepath.Join(dataDir, serverID, "estado.json")

	fmt.Println("================================================================")
	fmt.Printf("        INICIANDO SERVIDOR DO CONSÓRCIO - [%s]\n", serverID)
	fmt.Println("================================================================")
	fmt.Printf("  • Porta TCP Clientes:     %s\n", port)
	fmt.Printf("  • API REST Interna:       %s\n", internalPort)
	fmt.Printf("  • Arquivo de Estado:      %s\n", dataPath)
	fmt.Println("  • Parceiros do Consórcio configurados:")
	for peerID, addr := range peers {
		fmt.Printf("      - %s -> %s\n", peerID, addr)
	}
	fmt.Println("----------------------------------------------------------------")

	srv, err := server.NewServer(port, serverID, peers, dataPath)
	if err != nil {
		log.Fatalf("[FATAL] Erro ao carregar o estado de %s: %v", serverID, err)
	}

	// Sobe a API REST interna antes do listener TCP, que bloqueia a execução
	if err := srv.StartInternalHTTP(internalPort); err != nil {
		log.Fatalf("[FATAL] %v", err)
	}

	// Inicia a recuperação do 2PC em segundo plano: resolve transações interrompidas
	// e consulta periodicamente o coordenador das transações pendentes deste nó
	srv.StartRecovery()

	if err := srv.Start(); err != nil {
		log.Fatalf("[FATAL] Erro ao rodar o servidor %s: %v", serverID, err)
	}
}
