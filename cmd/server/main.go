package main

import (
	"fmt"
	"log"
	"os"

	"github.com/Figueredo-D/PBL_REDES/internal/server"
)

func main() {
	// Valida se os parâmetros obrigatórios (ID do Servidor e Porta) foram fornecidos
	// Exemplo de execução: go run cmd/server/main.go SERVER_A :8080
	if len(os.Args) < 3 {
		fmt.Println("Uso incorreto!")
		fmt.Println("Uso correto: go run cmd/server/main.go <SERVER_ID> <PORTA>")
		fmt.Println("Exemplo: go run cmd/server/main.go SERVER_A :8080")
		os.Exit(1)
	}

	// Captura os argumentos da linha de comando
	serverID := os.Args[1] // Identificador único do nó (ex: SERVER_A, SERVER_B, etc.)
	port := os.Args[2]     // Porta TCP onde o servidor irá escutar (ex: :8080)

	// Inicializa o servidor passando a porta e o ID para configurar o Relógio de Lamport e a Fila
	srv := server.NewServer(port, serverID)

	// Inicia o listener TCP e o worker loop de requisições sequenciais
	if err := srv.Start(); err != nil {
		log.Fatalf("[FATAL] Erro ao rodar o servidor %s: %v", serverID, err)
	}
}