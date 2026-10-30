# VaiJunto — Consórcio de Caronas Compartilhadas

> **Disciplina:** TEC502 — Problema 2  
> **Linguagem:** Go 1.22 (stdlib apenas)  

Sistema distribuído de reservas de caronas onde **três empresas independentes** cooperam para oferecer itinerários combinados. Cada empresa mantém seu próprio servidor, e a reserva de um itinerário que cruza várias empresas é feita de forma **atômica** usando Two-Phase Commit (2PC) com precedência de Lamport para resolução de conflitos e recuperação automática após falhas.

---

## Sumário

1. [Visão Geral da Arquitetura](#visão-geral-da-arquitetura)
2. [Pré-requisitos](#pré-requisitos)
3. [Opção A — Rodando Localmente (Go direto)](#opção-a--rodando-localmente-go-direto)
4. [Opção B — Rodando com Docker Compose (mesma máquina)](#opção-b--rodando-com-docker-compose-mesma-máquina)
5. [Opção C — Rodando em Máquinas Distintas do Laboratório](#opção-c--rodando-em-máquinas-distintas)
6. [Usando o Cliente Motorista](#usando-o-cliente-motorista)
7. [Usando o Cliente Passageiro](#usando-o-cliente-passageiro)
8. [Simulador de Carga](#simulador-de-carga)
9. [Teste Automatizado com Injeção de Falhas](#teste-automatizado-com-injeção-de-falhas)
10. [Testes Unitários](#testes-unitários)
11. [Estrutura do Projeto](#estrutura-do-projeto)
12. [Mecanismos Técnicos](#mecanismos-técnicos)

---

## Visão Geral da Arquitetura

```
                    CONSÓRCIO VAIJUNTO
  ┌─────────────────────────────────────────────────────────┐
  │                                                         │
  │  [Motorista A] ──TCP──► [SERVER_A :8080]                │
  │  [Passageiro A]──TCP──►  API Interna:9080               │
  │                                                         │
  │                                                         │
  │  [Motorista B] ──TCP──► [SERVER_B :8081]                │
  │  [Passageiro B]──TCP──►  API Interna:9081               │
  │                                                         │
  │  [Motorista C] ──TCP──► [SERVER_C :8082]                │
  │  [Passageiro C]──TCP──►  API Interna:9082               │
  └─────────────────────────────────────────────────────────┘
```

- **Clientes → Servidor:** TCP persistente com protocolo JSON por linha (`\n`)
- **Servidor → Servidor:** HTTP REST (API interna sob `/internal/`)
- **Cada servidor** mantém seus próprios dados em disco (`data/SERVER_X/`)
- **Nenhum servidor central** — cada nó pode coordenar reservas para seus passageiros

### Portas Padrão

| Servidor  | TCP (Clientes) | REST Interna (Consórcio) |
|-----------|---------------|--------------------------|
| SERVER_A  | 8080          | 9080                     |
| SERVER_B  | 8081          | 9081                     |
| SERVER_C  | 8082          | 9082                     |

---

## Pré-requisitos

### Para rodar localmente (Opção A)

- **Go 1.22+** instalado → [golang.org/dl](https://golang.org/dl/)
- Verificar: `go version`

### Para rodar com Docker (Opções B e C)

- **Docker Desktop** (Windows/Mac) ou **Docker Engine** (Linux)
- **Docker Compose** v2+ (já incluído no Docker Desktop)
- Verificar: `docker --version` e `docker compose version`

---

## Opção A — Rodando Localmente (Go direto)

Ideal para desenvolvimento e debug. Todos os servidores rodam na mesma máquina.

### 1. Clonar e entrar no repositório

```bash
git clone <url-do-repositorio>
cd PBL_REDES2
```

### 2. Subir os 3 servidores (cada um em um terminal separado)

**Terminal 1 — SERVER_A:**
```bash
go run cmd/server/main.go SERVER_A
```

**Terminal 2 — SERVER_B:**
```bash
go run cmd/server/main.go SERVER_B
```

**Terminal 3 — SERVER_C:**
```bash
go run cmd/server/main.go SERVER_C
```

Você verá uma saída assim em cada terminal:
```
================================================================
        INICIANDO SERVIDOR DO CONSÓRCIO - [SERVER_A]
================================================================
  • Porta TCP Clientes:     :8080
  • API REST Interna:       :9080
  • Arquivo de Estado:      data/SERVER_A/estado.json
  • Parceiros do Consórcio configurados:
      - SERVER_B -> http://localhost:9081
      - SERVER_C -> http://localhost:9082
----------------------------------------------------------------
```

> **Nota:** Os três servidores se conectam automaticamente entre si pelos defaults. Não é necessário passar flags extras para rodar tudo na mesma máquina.

### 3. Verificar que os servidores estão prontos

```bash
curl http://localhost:9080/internal/rides
curl http://localhost:9081/internal/rides
curl http://localhost:9082/internal/rides
```

Cada um deve retornar `[]` (lista vazia) se ainda não há caronas, ou a lista de caronas existentes.

---

## Opção B — Rodando com Docker Compose (mesma máquina)

Simula o ambiente de laboratório com containers isolados rodando na mesma máquina.

### 1. Construir e subir todos os containers

```bash
docker compose up --build
```

Isso vai:
- Compilar o binário do servidor em Go dentro de um container builder
- Criar 3 containers: `server_a`, `server_b`, `server_c`
- Conectá-los na rede interna `consorcio-net`
- Expor as portas nas mesmas do modo local (8080-8082 TCP, 9080-9082 REST)

### 2. Rodar em segundo plano (opcional)

```bash
docker compose up --build -d
```

Para ver os logs:
```bash
docker compose logs -f
docker compose logs -f server_a   # só um nó
```

### 3. Parar os containers

```bash
docker compose down
```

### 4. Testar a queda de um servidor (simulação de falha)

```bash
# Derruba o server_b
docker compose stop server_b

# Verifica que A e C continuam respondendo
curl http://localhost:9080/internal/rides
curl http://localhost:9082/internal/rides

# Reinicia server_b (recovery automático ao subir)
docker compose start server_b
```

> **Dados persistidos:** Os dados de cada servidor ficam em `./data/SERVER_A/`, `./data/SERVER_B/` e `./data/SERVER_C/`. Para começar do zero, apague esses diretórios antes de subir.

---

## Opção C — Rodando em Máquinas Distintas

### Pré-requisito: descobrir os IPs das máquinas

Em cada máquina, rode:
```bash
# Linux/Mac
hostname -I

# Windows (PowerShell)
ipconfig
```

Exemplo hipotético:
- **Máquina 1** (SERVER_A): `192.168.1.10`
- **Máquina 2** (SERVER_B): `192.168.1.11`
- **Máquina 3** (SERVER_C): `192.168.1.12`

### Opção C.1 — Go direto em cada máquina

**Máquina 1 (SERVER_A):**
```bash
go run cmd/server/main.go -id SERVER_A -port 8080 -internal-port 9080 \
  -peers "SERVER_B=http://192.168.1.11:9081,SERVER_C=http://192.168.1.12:9082"
```

**Máquina 2 (SERVER_B):**
```bash
go run cmd/server/main.go -id SERVER_B -port 8081 -internal-port 9081 \
  -peers "SERVER_A=http://192.168.1.10:9080,SERVER_C=http://192.168.1.12:9082"
```

**Máquina 3 (SERVER_C):**
```bash
go run cmd/server/main.go -id SERVER_C -port 8082 -internal-port 9082 \
  -peers "SERVER_A=http://192.168.1.10:9080,SERVER_B=http://192.168.1.11:9081"
```

### Opção C.2 — Docker em cada máquina

Em cada máquina, copie o repositório e crie um arquivo `.env` no diretório do projeto. Depois rode apenas o container daquela empresa:

**Máquina 1 (SERVER_A):**
```bash
docker build --target server -t vaijunto-server .

docker run -d \
  --name server_a \
  -p 8080:8080 \
  -p 9080:9080 \
  -e SERVER_ID=SERVER_A \
  -e CLIENT_PORT=8080 \
  -e INTERNAL_PORT=9080 \
  -e PEERS="SERVER_B=http://192.168.1.11:9081,SERVER_C=http://192.168.1.12:9082" \
  -v "$(pwd)/data/SERVER_A:/app/data/SERVER_A" \
  vaijunto-server
```

**Máquina 2 (SERVER_B):**
```bash
docker build --target server -t vaijunto-server .

docker run -d \
  --name server_b \
  -p 8081:8081 \
  -p 9081:9081 \
  -e SERVER_ID=SERVER_B \
  -e CLIENT_PORT=8081 \
  -e INTERNAL_PORT=9081 \
  -e PEERS="SERVER_A=http://192.168.1.10:9080,SERVER_C=http://192.168.1.12:9082" \
  -v "$(pwd)/data/SERVER_B:/app/data/SERVER_B" \
  vaijunto-server
```

**Máquina 3 (SERVER_C):**
```bash
docker build --target server -t vaijunto-server .

docker run -d \
  --name server_c \
  -p 8082:8082 \
  -p 9082:9082 \
  -e SERVER_ID=SERVER_C \
  -e CLIENT_PORT=8082 \
  -e INTERNAL_PORT=9082 \
  -e PEERS="SERVER_A=http://192.168.1.10:9080,SERVER_B=http://192.168.1.11:9081" \
  -v "$(pwd)/data/SERVER_C:/app/data/SERVER_C" \
  vaijunto-server
```

> **Atenção com firewall:** As portas TCP (8080-8082) e REST (9080-9082) precisam estar liberadas entre as máquinas do laboratório. No Windows, pode ser necessário criar uma regra de entrada no Firewall do Windows.

### Verificar conectividade entre os nós

Da máquina 1, teste se ela consegue falar com as demais:
```bash
curl http://192.168.1.11:9081/internal/rides
curl http://192.168.1.12:9082/internal/rides
```

---

## Usando o Cliente Motorista

O cliente motorista se conecta ao servidor da sua empresa e pode publicar caronas.

### Modo interativo (menu)

```bash
# Conectar ao servidor local
go run cmd/driver-client/main.go

# Conectar a um servidor específico
go run cmd/driver-client/main.go localhost:8080

# Conectar a um servidor remoto (laboratório)
go run cmd/driver-client/main.go 192.168.1.10:8080
```

Ou via variável de ambiente:
```bash
SERVER_HOST=192.168.1.10:8080 go run cmd/driver-client/main.go
```

### Fluxo básico no menu

```
====================================================
        CLIENTE MOTORISTA — VaiJunto
====================================================
[1] Publicar carona
[2] Ver minhas caronas
[3] Encerrar conexão
[4] Outro endereço (Host:Porta remoto)
====================================================
```

1. **Publicar carona** → informe rota (ex: `Salvador,Feira`), data (`2025-12-01`), horário (`08:00`), total de assentos e preços por trecho
2. **Ver minhas caronas** → lista as caronas já publicadas com assentos disponíveis

---

## Usando o Cliente Passageiro

O cliente passageiro se conecta ao servidor da sua empresa e pode buscar e reservar itinerários — inclusive trechos de outras empresas.

### Modo interativo (menu)

```bash
# Conectar ao servidor local
go run cmd/passenger-client/main.go

# Conectar a um servidor específico
go run cmd/passenger-client/main.go localhost:8081

# Conectar a um servidor remoto
go run cmd/passenger-client/main.go 192.168.1.11:8081
```

Ou via variável de ambiente:
```bash
SERVER_HOST=192.168.1.11:8081 go run cmd/passenger-client/main.go
```

### Fluxo básico no menu

```
====================================================
        CLIENTE PASSAGEIRO — VaiJunto
====================================================
[1] Buscar itinerário
[2] Reservar itinerário
[3] Ver minhas reservas
[4] Outro endereço (Host:Porta remoto)
====================================================
```

1. **Buscar itinerário** → informe origem, destino e data. O servidor busca em todos os parceiros do consórcio e retorna opções de rotas (diretas e combinadas).
2. **Reservar itinerário** → escolha um dos itinerários listados. O servidor coordena o 2PC com todos os parceiros envolvidos. A reserva é **tudo ou nada**.
3. **Ver minhas reservas** → lista as reservas confirmadas para seu token.

### Exemplo de uso completo

```
Origem: Salvador
Destino: Montes Claros
Data: 2025-12-01

Itinerários encontrados:
  [1] Salvador → Feira (SERVER_A) + Feira → Conquista (SERVER_B) + Conquista → Montes Claros (SERVER_C)
      Preço total: R$ 120,00 | Saída: 08:00

Escolha o itinerário [1]: 1
→ Reservando via 2PC entre SERVER_A, SERVER_B e SERVER_C...
✔ RESERVA CONFIRMADA! Código: SERVER_A_BOOKING_0001
```

---

## Simulador de Carga

Gera um grande volume de buscas e reservas simultâneas de forma autônoma, sem interface gráfica. Ideal para estressar o sistema e verificar que nenhum assento é confirmado duas vezes.

### Rodando com servidores já no ar (modo padrão)

```bash
# Os servidores devem estar rodando (Opção A, B ou C)
go run cmd/simulator/main.go
```

### Parâmetros disponíveis

```bash
go run cmd/simulator/main.go \
  -passengers 30 \       # número de passageiros concorrentes (padrão: 20)
  -rounds 5 \            # rodadas de busca+reserva por passageiro (padrão: 5)
  -seats 15 \            # assentos por trecho (gera disputa acirrada, padrão: 12)
  -setup true \          # publica caronas de teste automaticamente (padrão: true)
  -timeout 10s \         # timeout por requisição (padrão: 10s)
  -servers "localhost:8080,localhost:8081,localhost:8082" \
  -rest-bases "http://localhost:9080,http://localhost:9081,http://localhost:9082"
```

### Rodando contra servidores remotos (laboratório)

```bash
go run cmd/simulator/main.go \
  -passengers 50 \
  -rounds 10 \
  -seats 20 \
  -servers "192.168.1.10:8080,192.168.1.11:8081,192.168.1.12:8082" \
  -rest-bases "http://192.168.1.10:9080,http://192.168.1.11:9081,http://192.168.1.12:9082"
```

### Saída esperada

```
================================================================
     SIMULADOR DE CARGA — VaiJunto Consórcio de Caronas
================================================================
[SETUP] Publicando caronas de teste em todos os servidores...
[SIM] Iniciando 30 passageiros × 5 rodadas...

================================================================
                    RELATÓRIO FINAL
================================================================
  Buscas:    150 total | 147 sucesso | 3 falha
  Reservas:  150 total | 12 commits | 138 sem vaga | 0 erros
  Latência:  avg=142ms min=23ms max=891ms

  Validação de Overbooking:
    SERVER_A: 15 vagas → 3 consumidas ✔
    SERVER_B: 15 vagas → 3 consumidas ✔
    SERVER_C: 15 vagas → 3 consumidas ✔
  ✔ ZERO OVERBOOKING CONFIRMADO!
================================================================
```

---

## Teste Automatizado com Injeção de Falhas

Verifica automaticamente os 3 invariantes de segurança do sistema sob condições adversas (concorrência + queda de servidor).

> **Atenção:** Este teste sobe instâncias dos servidores diretamente no processo (sem Docker). **Certifique-se de que as portas 8080-8082 e 9080-9082 estão livres** antes de rodar.

```bash
go run cmd/fault-test/main.go
```

### Com parâmetros

```bash
go run cmd/fault-test/main.go \
  -seats 10 \      # assentos por trecho (padrão: 10)
  -workers 15      # passageiros concorrentes (padrão: 15)
```

### O que o teste verifica

O teste executa automaticamente a seguinte sequência:

1. **Setup** — Sobe 3 servidores em processo, publica 1 carona em cada (Salvador→Feira→Conquista→Montes Claros)
2. **Carga + Falha** — Dispara N reservas simultâneas do itinerário completo e **derruba o SERVER_B no meio da execução**
3. **Liveness** — Verifica que SERVER_A continua atendendo requisições locais enquanto B está fora
4. **Recuperação** — Reinicia SERVER_B e aguarda a thread de recovery resolver transações pendentes
5. **Verificação dos invariantes:**

| # | Invariante | Verificação |
|---|-----------|-------------|
| 1 | **Zero Overbooking** | Nenhum servidor tem vagas negativas |
| 2 | **Atomicidade 2PC** | Para cada reserva confirmada, todos os trechos foram consumidos |
| 3 | **Zero Assentos Bloqueados** | Nenhuma transação permanece em estado `PREPARED` após recovery |

### Saída esperada (sucesso)

```
================================================================
   TESTE AUTOMATIZADO SOB CARGA COM INJEÇÃO DE FALHAS (2PC)
================================================================
[INICIALIZAÇÃO] Subindo instâncias dos servidores A, B e C...
[SETUP] Ofertando caronas com 10 assentos em cada empresa...

[EXECUÇÃO] Disparando reservas simultâneas e INJETANDO QUEDA do Servidor B...
  💥 [INJEÇÃO DE FALHA] CRASH NO SERVIDOR B! Parando porta TCP e API REST interna...
[EXECUÇÃO] Rodada sob falha concluída: 3 confirmadas, 12 falharam/recusadas.

[LIVENESS] Testando se o Servidor A continua operando normalmente...
  ✔ PASSOU: Servidor A continua atendendo requisições independentes do nó que falhou!

[RECUPERAÇÃO] Reiniciando Servidor B...
  ✔ Servidor B reiniciado. Aguardando reconciliação da thread de recovery...

================================================================
               VERIFICAÇÃO FORMAL DOS INVARIANTES
================================================================
1. ✔ PASSOU: Nenhum assento foi confirmado para mais de um passageiro!
2. ✔ PASSOU: Atomicidade 2PC respeitada!
3. ✔ PASSOU: Todas as transações pendentes foram resolvidas pelo Recovery!

RESULTADO FINAL: TODOS OS INVARIANTES DO EDITAL FORAM CONFIRMADOS COM SUCESSO! 🎉
================================================================
```

---

## Testes Unitários

```bash
# Rodar todos os testes
go test ./...

# Rodar apenas os testes de domínio (Lamport + 2PC) com saída detalhada
go test -v ./internal/domain/...
```

Os testes cobrem:
- `TestLamportPriorityOrdering` — Ordenação total por tupla (timestamp, ID do coordenador)
- `TestLamportSeatContentionAndPreemption` — Preempção de transação de menor prioridade quando chega uma de maior prioridade disputando o mesmo assento

```
=== RUN   TestLamportPriorityOrdering
--- PASS: TestLamportPriorityOrdering (0.00s)
=== RUN   TestLamportSeatContentionAndPreemption
[TXLOG] Precedência de Lamport: Preemptando transação TX_1 (Lamport=10, Coord=SERVER_B)
        em favor de (Lamport=5, Coord=SERVER_A) no trecho Salvador → Feira
--- PASS: TestLamportSeatContentionAndPreemption (0.00s)
PASS
```

---

## Estrutura do Projeto

```
PBL_REDES2/
├── cmd/
│   ├── server/            # Binário do servidor de empresa
│   ├── driver-client/     # Cliente interativo do motorista
│   ├── passenger-client/  # Cliente interativo do passageiro
│   ├── simulator/         # Simulador de carga autônomo
│   └── fault-test/        # Teste automatizado com injeção de falhas
│
├── internal/
│   ├── cluster/
│   │   ├── rest_client.go    # Cliente HTTP para a API REST interna do consórcio
│   │   └── node_client.go    # Abstração de nó parceiro
│   ├── concurrency/
│   │   └── lock_manager.go   # Relógio lógico de Lamport + fila de requisições
│   ├── domain/
│   │   ├── ride.go           # Modelo de carona e trechos
│   │   ├── repository.go     # Repositório de caronas com persistência em JSON
│   │   ├── itinerary.go      # Algoritmo BFS para descoberta de itinerários combinados
│   │   ├── transactions.go   # Log de transações 2PC + precedência de Lamport
│   │   ├── persistence.go    # Serialização/deserialização de estado em disco
│   │   └── transactions_test.go
│   ├── protocol/
│   │   ├── messages.go       # Estruturas de mensagens TCP (Request/Response)
│   │   └── encoder.go        # Serialização JSON + delimitador de linha
│   └── server/
│       ├── listener.go       # Loop TCP principal + handlers de todas as ações
│       ├── internal_http.go  # Servidor HTTP da API REST interna (/internal/*)
│       └── recovery.go       # Thread de recuperação pós-falha (2PC recovery)
│
├── docs/
│   └── api-rest-consorcio.md  # Especificação completa da API REST interna
│
├── data/                      # Persistência em disco (criado automaticamente)
│   ├── SERVER_A/
│   │   ├── estado.json        # Caronas e reservas do SERVER_A
│   │   └── txlog.json         # Log de transações 2PC do SERVER_A
│   ├── SERVER_B/
│   └── SERVER_C/
│
├── Dockerfile                 # Multi-stage build: server, driver-client, passenger-client
├── docker-compose.yml         # Orquestração dos 3 servidores em containers isolados
├── go.mod
└── README.md
```

---

## Mecanismos Técnicos

### Two-Phase Commit (2PC)

Cada reserva de itinerário multi-empresa segue o protocolo 2PC coordenado pelo servidor ao qual o passageiro está conectado:

1. **Fase 1 (Prepare):** O coordenador envia `POST /internal/prepare` para todos os participantes. Cada um tenta reservar (bloquear) o assento localmente e responde `VOTE_COMMIT` ou `VOTE_ABORT`.
2. **Fase 2 (Commit/Abort):** Se todos votaram `VOTE_COMMIT`, o coordenador envia `POST /internal/commit` para todos. Se qualquer um votou `VOTE_ABORT`, envia `POST /internal/abort` para todos os que já tinham preparado.

### Precedência de Lamport

Quando dois passageiros de servidores diferentes disputam o último assento simultaneamente:

- Cada transação recebe um **timestamp lógico de Lamport** gerado pelo coordenador
- A prioridade é determinada pela tupla **(lamport_time, coordinator_id)** — menor tupla vence
- Se uma transação de **maior prioridade** chegar enquanto uma de **menor prioridade** está em estado `PREPARED`, a de menor prioridade é **preemptada** (assento liberado) e o participante aceita a nova

### Recuperação Pós-Falha

Uma **thread de recovery** roda periodicamente em segundo plano em cada servidor:

- Ao reiniciar, o servidor carrega seu `txlog.json` do disco
- Para cada transação em estado `PREPARED` sem decisão registrada, consulta o coordenador via `GET /internal/transactions/decision`
- Se o coordenador decidiu `COMMIT` → aplica o commit localmente
- Se o coordenador decidiu `ABORT` → desfaz o prepare e libera o assento
- Se o coordenador não for alcançável → aguarda e tenta novamente no próximo ciclo
- Garante que **nenhum assento fique bloqueado indefinidamente** após uma queda

### Persistência

Cada servidor salva seu estado em dois arquivos JSON:
- `estado.json` — catálogo de caronas, assentos disponíveis e reservas confirmadas
- `txlog.json` — log de transações 2PC em andamento (necessário para recovery)

Toda operação que modifica o estado é salva em disco antes de responder ao cliente.

