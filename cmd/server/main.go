package main

import (
	"fmt"
	"log"
	"os"

	"github.com/Figueredo-D/PBL_REDES/internal/server"
)

func main() {
	// Verifica se o ID do servidor foi passado por argumento
	if len(os.Args) < 2 {
		fmt.Println("Uso incorreto!")
		fmt.Println("Uso correto: go run cmd/server/main.go <SERVER_ID>")
		fmt.Println("Exemplos: ")
		fmt.Println("  go run cmd/server/main.go SERVER_A")
		fmt.Println("  go run cmd/server/main.go SERVER_B")
		fmt.Println("  go run cmd/server/main.go SERVER_C")
		os.Exit(1)
	}

	serverID := os.Args[1]

	// Mapeamento fixo de portas para cada nó do consórcio
	ports := map[string]string{
		"SERVER_A": ":8080",
		"SERVER_B": ":8081",
		"SERVER_C": ":8082",
	}

	// Mapeamento fixo dos vizinhos (peers) para cada nó
	allPeers := map[string]map[string]string{
		"SERVER_A": {
			"SERVER_B": "localhost:8081",
			"SERVER_C": "localhost:8082",
		},
		"SERVER_B": {
			"SERVER_A": "localhost:8080",
			"SERVER_C": "localhost:8082",
		},
		"SERVER_C": {
			"SERVER_A": "localhost:8080",
			"SERVER_B": "localhost:8081",
		},
	}

	port, portExists := ports[serverID]
	peers, peersExist := allPeers[serverID]

	if !portExists || !peersExist {
		log.Fatalf("[FATAL] ID de servidor inválido: '%s'. Utilize SERVER_A, SERVER_B ou SERVER_C.", serverID)
	}

	// Inicializa o servidor com a porta fixa, o ID e o mapa de peers configurados
	srv := server.NewServer(port, serverID, peers)

	if err := srv.Start(); err != nil {
		log.Fatalf("[FATAL] Erro ao rodar o servidor %s: %v", serverID, err)
	}
}