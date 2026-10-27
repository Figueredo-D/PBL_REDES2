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

	// Porta TCP usada pelos clientes (motorista e passageiro)
	ports := map[string]string{
		"SERVER_A": ":8080",
		"SERVER_B": ":8081",
		"SERVER_C": ":8082",
	}

	// Porta da API REST interna, usada pelos outros servidores do consórcio
	internalPorts := map[string]string{
		"SERVER_A": ":9080",
		"SERVER_B": ":9081",
		"SERVER_C": ":9082",
	}

	// Mapeamento fixo dos vizinhos (peers) para cada nó (TCP, ainda usado no 2PC nesta etapa)
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
	internalPort, internalExists := internalPorts[serverID]
	peers, peersExist := allPeers[serverID]

	if !portExists || !internalExists || !peersExist {
		log.Fatalf("[FATAL] ID de servidor inválido: '%s'. Utilize SERVER_A, SERVER_B ou SERVER_C.", serverID)
	}

	srv := server.NewServer(port, serverID, peers)

	// Sobe a API REST interna antes do listener TCP, que bloqueia a execução
	if err := srv.StartInternalHTTP(internalPort); err != nil {
		log.Fatalf("[FATAL] %v", err)
	}

	if err := srv.Start(); err != nil {
		log.Fatalf("[FATAL] Erro ao rodar o servidor %s: %v", serverID, err)
	}
}
