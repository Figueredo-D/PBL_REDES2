package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

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

	// Vizinhos de cada nó. Só os IDs são usados na comunicação; o endereço REST fica em rest_client.go
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

	// Cada nó tem o seu próprio arquivo: data/SERVER_A/estado.json, data/SERVER_B/estado.json, etc.
	dataPath := filepath.Join("data", serverID, "estado.json")

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
