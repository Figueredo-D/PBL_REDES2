# Contrato da API REST interna entre os servidores do consórcio

Nota: este documento é a especificação alvo da API REST entre os servidores. A implementação segue nos próximos passos do projeto, e o código atual ainda usa TCP entre os nós.

Este documento descreve a API REST que os servidores do consórcio expõem uns aos outros. Ela não inclui a comunicação com os clientes de motorista e passageiro, que continua em TCP e não faz parte deste contrato.

## Convenções gerais

Cada servidor expõe a API em `http://{host}:{porta}`. Todas as requisições e respostas usam JSON em UTF-8, com o cabeçalho `Content-Type: application/json`.

Todos os endpoints ficam sob o prefixo `/internal/`, que é a área de comunicação entre empresas.

Os identificadores seguem estes formatos. Caronas são `{SERVER_ID}_RIDE_{NNNN}`, por exemplo `SERVER_A_RIDE_0001`. Transações são `{SERVER_ID}_TX_{UNIX_NANO}`, geradas pelo coordenador, por exemplo `SERVER_C_TX_1730000000000000000`. O prefixo garante unicidade entre os nós.

As respostas de erro seguem sempre o mesmo formato:


json

{
  "code": "NO_SEATS",
  "message": "sem vagas no trecho Feira -> Conquista"
}




Os valores `code` estão listados na seção de códigos de retorno.

## Precedência entre transações

Toda requisição de `prepare` carrega `lamport_ts` (relógio lógico do coordenador no momento da transação) e `coordinator_id` (ID do servidor coordenador). Quando duas transações disputam o mesmo assento, vence a de menor par `(lamport_ts, coordinator_id)`, comparado lexicograficamente no ID. O participante que já tiver um `prepare` de maior prioridade para o mesmo trecho responde `VOTE_ABORT`. Esta regra é aplicada na etapa de precedência do projeto, e os campos já estão presentes no contrato para que a implementação não exija mudança de formato.

## Recursos

### GET /internal/rides

Lista as caronas cadastradas no servidor consultado, filtradas pela data.

Parâmetro obrigatório na query string: `date`, no formato `AAAA-MM-DD`.

Resposta de sucesso, código 200, com um array de caronas:


json

[
  {
    "id": "SERVER_A_RIDE_0001",
    "company_id": "SERVER_A",
    "driver_token": "motorista_A",
    "route": ["Salvador", "Feira"],
    "date": "2026-10-25",
    "time": "08:00",
    "total_seats": 3,
    "segments": [
      {
        "from": "Salvador",
        "to": "Feira",
        "price": 30,
        "available_seats": 3
      }
    ]
  }
]




Se a data não for informada ou estiver em formato inválido, a resposta é 400 com o código `INVALID_DATA`. Se não houver caronas na data, a resposta é 200 com um array vazio.

### POST /internal/prepare

Fase 1 do 2PC. O coordenador envia o itinerário completo a cada participante. Cada participante verifica e reserva apenas os trechos cujo `company_id` é o seu próprio ID, e vota.

Corpo da requisição:


json


{
  "transaction_id": "SERVER_C_TX_1730000000000000000",
  "coordinator_id": "SERVER_C",
  "lamport_ts": 42,
  "itinerary": {
    "itinerary_id": "SERVER_A_RIDE_0001",
    "total_price": 70,
    "departure_time": "08:00",
    "available_seats": 3,
    "drivers": ["motorista_A", "motorista_B"],
    "segments": [
      {
        "ride_id": "SERVER_A_RIDE_0001",
        "company_id": "SERVER_A",
        "driver_token": "motorista_A",
        "from": "Salvador",
        "to": "Feira",
        "departure_time": "08:00",
        "price": 30,
        "available_seats": 3
      },
      {
        "ride_id": "SERVER_B_RIDE_0001",
        "company_id": "SERVER_B",
        "driver_token": "motorista_B",
        "from": "Feira",
        "to": "Conquista",
        "departure_time": "10:00",
        "price": 40,
        "available_seats": 3
      }
    ]
  }
}




Resposta quando o participante vota a favor, código 200:


json

{
  "transaction_id": "SERVER_C_TX_1730000000000000000",
  "vote": "VOTE_COMMIT"
}




Resposta quando o participante vota contra, código 409:


json

{
  "transaction_id": "SERVER_C_TX_1730000000000000000",
  "vote": "VOTE_ABORT",
  "code": "NO_SEATS",
  "message": "sem vagas no trecho Feira -> Conquista"
}




Se o trecho não existir neste servidor, a resposta é 409 com o código `RIDE_NOT_FOUND`. Se o corpo for inválido, a resposta é 400 com `INVALID_DATA`.

Este endpoint é idempotente. Um segundo `prepare` com o mesmo `transaction_id` devolve o mesmo voto e não decrementa as vagas novamente.

### POST /internal/commit

Fase 2 do 2PC, decisão de commit. O participante confirma as vagas reservadas na transação.

Corpo da requisição:


json

{
  "transaction_id": "SERVER_C_TX_1730000000000000000",
  "coordinator_id": "SERVER_C"
}




Resposta de sucesso, código 200:


json

{
  "transaction_id": "SERVER_C_TX_1730000000000000000",
  "status": "COMMITTED"
}




Este endpoint é idempotente. Repetir um commit já efetivado devolve 200 com o mesmo status.

### POST /internal/abort

Fase 2 do 2PC, decisão de abort. O participante libera as vagas que havia reservado para a transação.

Corpo da requisição:


json

{
  "transaction_id": "SERVER_C_TX_1730000000000000000",
  "coordinator_id": "SERVER_C"
}




Resposta de sucesso, código 200:


json

{
  "transaction_id": "SERVER_C_TX_1730000000000000000",
  "status": "ABORTED"
}




Se a transação não for conhecida pelo participante, a resposta ainda é 200, porque não há nada a liberar. Este endpoint também é idempotente.

### GET /internal/transactions/{transaction_id}

Consulta o estado de uma transação no participante. Serve para o mecanismo de recuperação de falhas: um nó que ficou com a transação pendente pergunta aos demais qual foi a decisão.

Resposta de sucesso, código 200:


json

{
  "transaction_id": "SERVER_C_TX_1730000000000000000",
  "state": "PREPARED"
}




Os valores possíveis de `state` são `PREPARED`, `COMMITTED` e `ABORTED`. Se a transação não for conhecida, a resposta é 404 com o código `TRANSACTION_NOT_FOUND`.

## Códigos de retorno

| Código HTTP | `code` | Significado |
|---|---|---|
| 200 | — | Operação concluída, ou voto `VOTE_COMMIT`, ou repetição idempotente |
| 400 | `INVALID_DATA` | Parâmetro ausente ou corpo malformado |
| 404 | `TRANSACTION_NOT_FOUND` | Transação desconhecida pelo participante (apenas em consulta) |
| 409 | `NO_SEATS` | Trecho sem vagas, voto `VOTE_ABORT` |
| 409 | `RIDE_NOT_FOUND` | Carona do trecho não existe neste servidor |
| 409 | `LOWER_PRIORITY` | Existe transação de maior prioridade para o mesmo trecho, voto `VOTE_ABORT` (aplicado na etapa de precedência) |
| 500 | `INTERNAL_ERROR` | Falha inesperada no servidor |

Quando o coordenador não recebe resposta dentro do timeout, trata a chamada como voto `VOTE_ABORT`. Esse tempo limite é aplicado do lado do cliente HTTP, e não aparece como código de retorno.

## Diagrama de sequência: confirmação de itinerário entre empresas

O passageiro se conecta ao servidor C, que atua como coordenador. O itinerário combina um trecho do servidor A e um trecho do servidor B.