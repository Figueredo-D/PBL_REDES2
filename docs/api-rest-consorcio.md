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

<svg id="mermaid-1793133010306" width="100%" xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" style="max-width: 1203px;" viewBox="-50 -10 1203 899" role="graphics-document document" aria-roledescription="sequence"><g><rect x="953" y="813" fill="#eaeaea" stroke="#666" width="150" height="65" name="B" rx="3" ry="3" class="actor actor-bottom"/><text x="1028" y="845.5" dominant-baseline="central" alignment-baseline="central" class="actor actor-box" style="text-anchor: middle; font-size: 16px; font-weight: 400;"><tspan x="1028" dy="0">Servidor B</tspan></text></g><g><rect x="607" y="813" fill="#eaeaea" stroke="#666" width="150" height="65" name="A" rx="3" ry="3" class="actor actor-bottom"/><text x="682" y="845.5" dominant-baseline="central" alignment-baseline="central" class="actor actor-box" style="text-anchor: middle; font-size: 16px; font-weight: 400;"><tspan x="682" dy="0">Servidor A</tspan></text></g><g><rect x="333.5" y="813" fill="#eaeaea" stroke="#666" width="211" height="65" name="C" rx="3" ry="3" class="actor actor-bottom"/><text x="439" y="845.5" dominant-baseline="central" alignment-baseline="central" class="actor actor-box" style="text-anchor: middle; font-size: 16px; font-weight: 400;"><tspan x="439" dy="0">Servidor C (coordenador)</tspan></text></g><g><rect x="0" y="813" fill="#eaeaea" stroke="#666" width="150" height="65" name="P" rx="3" ry="3" class="actor actor-bottom"/><text x="75" y="845.5" dominant-baseline="central" alignment-baseline="central" class="actor actor-box" style="text-anchor: middle; font-size: 16px; font-weight: 400;"><tspan x="75" dy="0">Passageiro (TCP)</tspan></text></g><g><line id="actor7" x1="1028" y1="65" x2="1028" y2="813" class="actor-line 200" stroke-width="0.5px" stroke="#999" name="B"/><g id="root-7"><rect x="953" y="0" fill="#eaeaea" stroke="#666" width="150" height="65" name="B" rx="3" ry="3" class="actor actor-top"/><text x="1028" y="32.5" dominant-baseline="central" alignment-baseline="central" class="actor actor-box" style="text-anchor: middle; font-size: 16px; font-weight: 400;"><tspan x="1028" dy="0">Servidor B</tspan></text></g></g><g><line id="actor6" x1="682" y1="65" x2="682" y2="813" class="actor-line 200" stroke-width="0.5px" stroke="#999" name="A"/><g id="root-6"><rect x="607" y="0" fill="#eaeaea" stroke="#666" width="150" height="65" name="A" rx="3" ry="3" class="actor actor-top"/><text x="682" y="32.5" dominant-baseline="central" alignment-baseline="central" class="actor actor-box" style="text-anchor: middle; font-size: 16px; font-weight: 400;"><tspan x="682" dy="0">Servidor A</tspan></text></g></g><g><line id="actor5" x1="439" y1="65" x2="439" y2="813" class="actor-line 200" stroke-width="0.5px" stroke="#999" name="C"/><g id="root-5"><rect x="333.5" y="0" fill="#eaeaea" stroke="#666" width="211" height="65" name="C" rx="3" ry="3" class="actor actor-top"/><text x="439" y="32.5" dominant-baseline="central" alignment-baseline="central" class="actor actor-box" style="text-anchor: middle; font-size: 16px; font-weight: 400;"><tspan x="439" dy="0">Servidor C (coordenador)</tspan></text></g></g><g><line id="actor4" x1="75" y1="65" x2="75" y2="813" class="actor-line 200" stroke-width="0.5px" stroke="#999" name="P"/><g id="root-4"><rect x="0" y="0" fill="#eaeaea" stroke="#666" width="150" height="65" name="P" rx="3" ry="3" class="actor actor-top"/><text x="75" y="32.5" dominant-baseline="central" alignment-baseline="central" class="actor actor-box" style="text-anchor: middle; font-size: 16px; font-weight: 400;"><tspan x="75" dy="0">Passageiro (TCP)</tspan></text></g></g><style>#mermaid-1793133010306{font-family:"trebuchet ms",verdana,arial,sans-serif;font-size:16px;fill:#333;}@keyframes edge-animation-frame{from{stroke-dashoffset:0;}}@keyframes dash{to{stroke-dashoffset:0;}}#mermaid-1793133010306 .edge-animation-slow{stroke-dasharray:9,5!important;stroke-dashoffset:900;animation:dash 50s linear infinite;stroke-linecap:round;}#mermaid-1793133010306 .edge-animation-fast{stroke-dasharray:9,5!important;stroke-dashoffset:900;animation:dash 20s linear infinite;stroke-linecap:round;}#mermaid-1793133010306 .error-icon{fill:#552222;}#mermaid-1793133010306 .error-text{fill:#552222;stroke:#552222;}#mermaid-1793133010306 .edge-thickness-normal{stroke-width:1px;}#mermaid-1793133010306 .edge-thickness-thick{stroke-width:3.5px;}#mermaid-1793133010306 .edge-pattern-solid{stroke-dasharray:0;}#mermaid-1793133010306 .edge-thickness-invisible{stroke-width:0;fill:none;}#mermaid-1793133010306 .edge-pattern-dashed{stroke-dasharray:3;}#mermaid-1793133010306 .edge-pattern-dotted{stroke-dasharray:2;}#mermaid-1793133010306 .marker{fill:#333333;stroke:#333333;}#mermaid-1793133010306 .marker.cross{stroke:#333333;}#mermaid-1793133010306 svg{font-family:"trebuchet ms",verdana,arial,sans-serif;font-size:16px;}#mermaid-1793133010306 p{margin:0;}#mermaid-1793133010306 .actor{stroke:hsl(259.6261682243, 59.7765363128%, 87.9019607843%);fill:#ECECFF;}#mermaid-1793133010306 text.actor&gt;tspan{fill:black;stroke:none;}#mermaid-1793133010306 .actor-line{stroke:hsl(259.6261682243, 59.7765363128%, 87.9019607843%);}#mermaid-1793133010306 .innerArc{stroke-width:1.5;stroke-dasharray:none;}#mermaid-1793133010306 .messageLine0{stroke-width:1.5;stroke-dasharray:none;stroke:#333;}#mermaid-1793133010306 .messageLine1{stroke-width:1.5;stroke-dasharray:2,2;stroke:#333;}#mermaid-1793133010306 #arrowhead path{fill:#333;stroke:#333;}#mermaid-1793133010306 .sequenceNumber{fill:white;}#mermaid-1793133010306 #sequencenumber{fill:#333;}#mermaid-1793133010306 #crosshead path{fill:#333;stroke:#333;}#mermaid-1793133010306 .messageText{fill:#333;stroke:none;}#mermaid-1793133010306 .labelBox{stroke:hsl(259.6261682243, 59.7765363128%, 87.9019607843%);fill:#ECECFF;}#mermaid-1793133010306 .labelText,#mermaid-1793133010306 .labelText&gt;tspan{fill:black;stroke:none;}#mermaid-1793133010306 .loopText,#mermaid-1793133010306 .loopText&gt;tspan{fill:black;stroke:none;}#mermaid-1793133010306 .loopLine{stroke-width:2px;stroke-dasharray:2,2;stroke:hsl(259.6261682243, 59.7765363128%, 87.9019607843%);fill:hsl(259.6261682243, 59.7765363128%, 87.9019607843%);}#mermaid-1793133010306 .note{stroke:#aaaa33;fill:#fff5ad;}#mermaid-1793133010306 .noteText,#mermaid-1793133010306 .noteText&gt;tspan{fill:black;stroke:none;}#mermaid-1793133010306 .activation0{fill:#f4f4f4;stroke:#666;}#mermaid-1793133010306 .activation1{fill:#f4f4f4;stroke:#666;}#mermaid-1793133010306 .activation2{fill:#f4f4f4;stroke:#666;}#mermaid-1793133010306 .actorPopupMenu{position:absolute;}#mermaid-1793133010306 .actorPopupMenuPanel{position:absolute;fill:#ECECFF;box-shadow:0px 8px 16px 0px rgba(0,0,0,0.2);filter:drop-shadow(3px 5px 2px rgb(0 0 0 / 0.4));}#mermaid-1793133010306 .actor-man line{stroke:hsl(259.6261682243, 59.7765363128%, 87.9019607843%);fill:#ECECFF;}#mermaid-1793133010306 .actor-man circle,#mermaid-1793133010306 line{stroke:hsl(259.6261682243, 59.7765363128%, 87.9019607843%);fill:#ECECFF;stroke-width:2px;}#mermaid-1793133010306 :root{--mermaid-font-family:"trebuchet ms",verdana,arial,sans-serif;}</style><g/><defs><symbol id="computer" width="24" height="24"><path transform="scale(.5)" d="M2 2v13h20v-13h-20zm18 11h-16v-9h16v9zm-10.228 6l.466-1h3.524l.467 1h-4.457zm14.228 3h-24l2-6h2.104l-1.33 4h18.45l-1.297-4h2.073l2 6zm-5-10h-14v-7h14v7z"/></symbol></defs><defs><symbol id="database" fill-rule="evenodd" clip-rule="evenodd"><path transform="scale(.5)" d="M12.258.001l.256.004.255.005.253.008.251.01.249.012.247.015.246.016.242.019.241.02.239.023.236.024.233.027.231.028.229.031.225.032.223.034.22.036.217.038.214.04.211.041.208.043.205.045.201.046.198.048.194.05.191.051.187.053.183.054.18.056.175.057.172.059.168.06.163.061.16.063.155.064.15.066.074.033.073.033.071.034.07.034.069.035.068.035.067.035.066.035.064.036.064.036.062.036.06.036.06.037.058.037.058.037.055.038.055.038.053.038.052.038.051.039.05.039.048.039.047.039.045.04.044.04.043.04.041.04.04.041.039.041.037.041.036.041.034.041.033.042.032.042.03.042.029.042.027.042.026.043.024.043.023.043.021.043.02.043.018.044.017.043.015.044.013.044.012.044.011.045.009.044.007.045.006.045.004.045.002.045.001.045v17l-.001.045-.002.045-.004.045-.006.045-.007.045-.009.044-.011.045-.012.044-.013.044-.015.044-.017.043-.018.044-.02.043-.021.043-.023.043-.024.043-.026.043-.027.042-.029.042-.03.042-.032.042-.033.042-.034.041-.036.041-.037.041-.039.041-.04.041-.041.04-.043.04-.044.04-.045.04-.047.039-.048.039-.05.039-.051.039-.052.038-.053.038-.055.038-.055.038-.058.037-.058.037-.06.037-.06.036-.062.036-.064.036-.064.036-.066.035-.067.035-.068.035-.069.035-.07.034-.071.034-.073.033-.074.033-.15.066-.155.064-.16.063-.163.061-.168.06-.172.059-.175.057-.18.056-.183.054-.187.053-.191.051-.194.05-.198.048-.201.046-.205.045-.208.043-.211.041-.214.04-.217.038-.22.036-.223.034-.225.032-.229.031-.231.028-.233.027-.236.024-.239.023-.241.02-.242.019-.246.016-.247.015-.249.012-.251.01-.253.008-.255.005-.256.004-.258.001-.258-.001-.256-.004-.255-.005-.253-.008-.251-.01-.249-.012-.247-.015-.245-.016-.243-.019-.241-.02-.238-.023-.236-.024-.234-.027-.231-.028-.228-.031-.226-.032-.223-.034-.22-.036-.217-.038-.214-.04-.211-.041-.208-.043-.204-.045-.201-.046-.198-.048-.195-.05-.19-.051-.187-.053-.184-.054-.179-.056-.176-.057-.172-.059-.167-.06-.164-.061-.159-.063-.155-.064-.151-.066-.074-.033-.072-.033-.072-.034-.07-.034-.069-.035-.068-.035-.067-.035-.066-.035-.064-.036-.063-.036-.062-.036-.061-.036-.06-.037-.058-.037-.057-.037-.056-.038-.055-.038-.053-.038-.052-.038-.051-.039-.049-.039-.049-.039-.046-.039-.046-.04-.044-.04-.043-.04-.041-.04-.04-.041-.039-.041-.037-.041-.036-.041-.034-.041-.033-.042-.032-.042-.03-.042-.029-.042-.027-.042-.026-.043-.024-.043-.023-.043-.021-.043-.02-.043-.018-.044-.017-.043-.015-.044-.013-.044-.012-.044-.011-.045-.009-.044-.007-.045-.006-.045-.004-.045-.002-.045-.001-.045v-17l.001-.045.002-.045.004-.045.006-.045.007-.045.009-.044.011-.045.012-.044.013-.044.015-.044.017-.043.018-.044.02-.043.021-.043.023-.043.024-.043.026-.043.027-.042.029-.042.03-.042.032-.042.033-.042.034-.041.036-.041.037-.041.039-.041.04-.041.041-.04.043-.04.044-.04.046-.04.046-.039.049-.039.049-.039.051-.039.052-.038.053-.038.055-.038.056-.038.057-.037.058-.037.06-.037.061-.036.062-.036.063-.036.064-.036.066-.035.067-.035.068-.035.069-.035.07-.034.072-.034.072-.033.074-.033.151-.066.155-.064.159-.063.164-.061.167-.06.172-.059.176-.057.179-.056.184-.054.187-.053.19-.051.195-.05.198-.048.201-.046.204-.045.208-.043.211-.041.214-.04.217-.038.22-.036.223-.034.226-.032.228-.031.231-.028.234-.027.236-.024.238-.023.241-.02.243-.019.245-.016.247-.015.249-.012.251-.01.253-.008.255-.005.256-.004.258-.001.258.001zm-9.258 20.499v.01l.001.021.003.021.004.022.005.021.006.022.007.022.009.023.01.022.011.023.012.023.013.023.015.023.016.024.017.023.018.024.019.024.021.024.022.025.023.024.024.025.052.049.056.05.061.051.066.051.07.051.075.051.079.052.084.052.088.052.092.052.097.052.102.051.105.052.11.052.114.051.119.051.123.051.127.05.131.05.135.05.139.048.144.049.147.047.152.047.155.047.16.045.163.045.167.043.171.043.176.041.178.041.183.039.187.039.19.037.194.035.197.035.202.033.204.031.209.03.212.029.216.027.219.025.222.024.226.021.23.02.233.018.236.016.24.015.243.012.246.01.249.008.253.005.256.004.259.001.26-.001.257-.004.254-.005.25-.008.247-.011.244-.012.241-.014.237-.016.233-.018.231-.021.226-.021.224-.024.22-.026.216-.027.212-.028.21-.031.205-.031.202-.034.198-.034.194-.036.191-.037.187-.039.183-.04.179-.04.175-.042.172-.043.168-.044.163-.045.16-.046.155-.046.152-.047.148-.048.143-.049.139-.049.136-.05.131-.05.126-.05.123-.051.118-.052.114-.051.11-.052.106-.052.101-.052.096-.052.092-.052.088-.053.083-.051.079-.052.074-.052.07-.051.065-.051.06-.051.056-.05.051-.05.023-.024.023-.025.021-.024.02-.024.019-.024.018-.024.017-.024.015-.023.014-.024.013-.023.012-.023.01-.023.01-.022.008-.022.006-.022.006-.022.004-.022.004-.021.001-.021.001-.021v-4.127l-.077.055-.08.053-.083.054-.085.053-.087.052-.09.052-.093.051-.095.05-.097.05-.1.049-.102.049-.105.048-.106.047-.109.047-.111.046-.114.045-.115.045-.118.044-.12.043-.122.042-.124.042-.126.041-.128.04-.13.04-.132.038-.134.038-.135.037-.138.037-.139.035-.142.035-.143.034-.144.033-.147.032-.148.031-.15.03-.151.03-.153.029-.154.027-.156.027-.158.026-.159.025-.161.024-.162.023-.163.022-.165.021-.166.02-.167.019-.169.018-.169.017-.171.016-.173.015-.173.014-.175.013-.175.012-.177.011-.178.01-.179.008-.179.008-.181.006-.182.005-.182.004-.184.003-.184.002h-.37l-.184-.002-.184-.003-.182-.004-.182-.005-.181-.006-.179-.008-.179-.008-.178-.01-.176-.011-.176-.012-.175-.013-.173-.014-.172-.015-.171-.016-.17-.017-.169-.018-.167-.019-.166-.02-.165-.021-.163-.022-.162-.023-.161-.024-.159-.025-.157-.026-.156-.027-.155-.027-.153-.029-.151-.03-.15-.03-.148-.031-.146-.032-.145-.033-.143-.034-.141-.035-.14-.035-.137-.037-.136-.037-.134-.038-.132-.038-.13-.04-.128-.04-.126-.041-.124-.042-.122-.042-.12-.044-.117-.043-.116-.045-.113-.045-.112-.046-.109-.047-.106-.047-.105-.048-.102-.049-.1-.049-.097-.05-.095-.05-.093-.052-.09-.051-.087-.052-.085-.053-.083-.054-.08-.054-.077-.054v4.127zm0-5.654v.011l.001.021.003.021.004.021.005.022.006.022.007.022.009.022.01.022.011.023.012.023.013.023.015.024.016.023.017.024.018.024.019.024.021.024.022.024.023.025.024.024.052.05.056.05.061.05.066.051.07.051.075.052.079.051.084.052.088.052.092.052.097.052.102.052.105.052.11.051.114.051.119.052.123.05.127.051.131.05.135.049.139.049.144.048.147.048.152.047.155.046.16.045.163.045.167.044.171.042.176.042.178.04.183.04.187.038.19.037.194.036.197.034.202.033.204.032.209.03.212.028.216.027.219.025.222.024.226.022.23.02.233.018.236.016.24.014.243.012.246.01.249.008.253.006.256.003.259.001.26-.001.257-.003.254-.006.25-.008.247-.01.244-.012.241-.015.237-.016.233-.018.231-.02.226-.022.224-.024.22-.025.216-.027.212-.029.21-.03.205-.032.202-.033.198-.035.194-.036.191-.037.187-.039.183-.039.179-.041.175-.042.172-.043.168-.044.163-.045.16-.045.155-.047.152-.047.148-.048.143-.048.139-.05.136-.049.131-.05.126-.051.123-.051.118-.051.114-.052.11-.052.106-.052.101-.052.096-.052.092-.052.088-.052.083-.052.079-.052.074-.051.07-.052.065-.051.06-.05.056-.051.051-.049.023-.025.023-.024.021-.025.02-.024.019-.024.018-.024.017-.024.015-.023.014-.023.013-.024.012-.022.01-.023.01-.023.008-.022.006-.022.006-.022.004-.021.004-.022.001-.021.001-.021v-4.139l-.077.054-.08.054-.083.054-.085.052-.087.053-.09.051-.093.051-.095.051-.097.05-.1.049-.102.049-.105.048-.106.047-.109.047-.111.046-.114.045-.115.044-.118.044-.12.044-.122.042-.124.042-.126.041-.128.04-.13.039-.132.039-.134.038-.135.037-.138.036-.139.036-.142.035-.143.033-.144.033-.147.033-.148.031-.15.03-.151.03-.153.028-.154.028-.156.027-.158.026-.159.025-.161.024-.162.023-.163.022-.165.021-.166.02-.167.019-.169.018-.169.017-.171.016-.173.015-.173.014-.175.013-.175.012-.177.011-.178.009-.179.009-.179.007-.181.007-.182.005-.182.004-.184.003-.184.002h-.37l-.184-.002-.184-.003-.182-.004-.182-.005-.181-.007-.179-.007-.179-.009-.178-.009-.176-.011-.176-.012-.175-.013-.173-.014-.172-.015-.171-.016-.17-.017-.169-.018-.167-.019-.166-.02-.165-.021-.163-.022-.162-.023-.161-.024-.159-.025-.157-.026-.156-.027-.155-.028-.153-.028-.151-.03-.15-.03-.148-.031-.146-.033-.145-.033-.143-.033-.141-.035-.14-.036-.137-.036-.136-.037-.134-.038-.132-.039-.13-.039-.128-.04-.126-.041-.124-.042-.122-.043-.12-.043-.117-.044-.116-.044-.113-.046-.112-.046-.109-.046-.106-.047-.105-.048-.102-.049-.1-.049-.097-.05-.095-.051-.093-.051-.09-.051-.087-.053-.085-.052-.083-.054-.08-.054-.077-.054v4.139zm0-5.666v.011l.001.02.003.022.004.021.005.022.006.021.007.022.009.023.01.022.011.023.012.023.013.023.015.023.016.024.017.024.018.023.019.024.021.025.022.024.023.024.024.025.052.05.056.05.061.05.066.051.07.051.075.052.079.051.084.052.088.052.092.052.097.052.102.052.105.051.11.052.114.051.119.051.123.051.127.05.131.05.135.05.139.049.144.048.147.048.152.047.155.046.16.045.163.045.167.043.171.043.176.042.178.04.183.04.187.038.19.037.194.036.197.034.202.033.204.032.209.03.212.028.216.027.219.025.222.024.226.021.23.02.233.018.236.017.24.014.243.012.246.01.249.008.253.006.256.003.259.001.26-.001.257-.003.254-.006.25-.008.247-.01.244-.013.241-.014.237-.016.233-.018.231-.02.226-.022.224-.024.22-.025.216-.027.212-.029.21-.03.205-.032.202-.033.198-.035.194-.036.191-.037.187-.039.183-.039.179-.041.175-.042.172-.043.168-.044.163-.045.16-.045.155-.047.152-.047.148-.048.143-.049.139-.049.136-.049.131-.051.126-.05.123-.051.118-.052.114-.051.11-.052.106-.052.101-.052.096-.052.092-.052.088-.052.083-.052.079-.052.074-.052.07-.051.065-.051.06-.051.056-.05.051-.049.023-.025.023-.025.021-.024.02-.024.019-.024.018-.024.017-.024.015-.023.014-.024.013-.023.012-.023.01-.022.01-.023.008-.022.006-.022.006-.022.004-.022.004-.021.001-.021.001-.021v-4.153l-.077.054-.08.054-.083.053-.085.053-.087.053-.09.051-.093.051-.095.051-.097.05-.1.049-.102.048-.105.048-.106.048-.109.046-.111.046-.114.046-.115.044-.118.044-.12.043-.122.043-.124.042-.126.041-.128.04-.13.039-.132.039-.134.038-.135.037-.138.036-.139.036-.142.034-.143.034-.144.033-.147.032-.148.032-.15.03-.151.03-.153.028-.154.028-.156.027-.158.026-.159.024-.161.024-.162.023-.163.023-.165.021-.166.02-.167.019-.169.018-.169.017-.171.016-.173.015-.173.014-.175.013-.175.012-.177.01-.178.01-.179.009-.179.007-.181.006-.182.006-.182.004-.184.003-.184.001-.185.001-.185-.001-.184-.001-.184-.003-.182-.004-.182-.006-.181-.006-.179-.007-.179-.009-.178-.01-.176-.01-.176-.012-.175-.013-.173-.014-.172-.015-.171-.016-.17-.017-.169-.018-.167-.019-.166-.02-.165-.021-.163-.023-.162-.023-.161-.024-.159-.024-.157-.026-.156-.027-.155-.028-.153-.028-.151-.03-.15-.03-.148-.032-.146-.032-.145-.033-.143-.034-.141-.034-.14-.036-.137-.036-.136-.037-.134-.038-.132-.039-.13-.039-.128-.041-.126-.041-.124-.041-.122-.043-.12-.043-.117-.044-.116-.044-.113-.046-.112-.046-.109-.046-.106-.048-.105-.048-.102-.048-.1-.05-.097-.049-.095-.051-.093-.051-.09-.052-.087-.052-.085-.053-.083-.053-.08-.054-.077-.054v4.153zm8.74-8.179l-.257.004-.254.005-.25.008-.247.011-.244.012-.241.014-.237.016-.233.018-.231.021-.226.022-.224.023-.22.026-.216.027-.212.028-.21.031-.205.032-.202.033-.198.034-.194.036-.191.038-.187.038-.183.04-.179.041-.175.042-.172.043-.168.043-.163.045-.16.046-.155.046-.152.048-.148.048-.143.048-.139.049-.136.05-.131.05-.126.051-.123.051-.118.051-.114.052-.11.052-.106.052-.101.052-.096.052-.092.052-.088.052-.083.052-.079.052-.074.051-.07.052-.065.051-.06.05-.056.05-.051.05-.023.025-.023.024-.021.024-.02.025-.019.024-.018.024-.017.023-.015.024-.014.023-.013.023-.012.023-.01.023-.01.022-.008.022-.006.023-.006.021-.004.022-.004.021-.001.021-.001.021.001.021.001.021.004.021.004.022.006.021.006.023.008.022.01.022.01.023.012.023.013.023.014.023.015.024.017.023.018.024.019.024.02.025.021.024.023.024.023.025.051.05.056.05.06.05.065.051.07.052.074.051.079.052.083.052.088.052.092.052.096.052.101.052.106.052.11.052.114.052.118.051.123.051.126.051.131.05.136.05.139.049.143.048.148.048.152.048.155.046.16.046.163.045.168.043.172.043.175.042.179.041.183.04.187.038.191.038.194.036.198.034.202.033.205.032.21.031.212.028.216.027.22.026.224.023.226.022.231.021.233.018.237.016.241.014.244.012.247.011.25.008.254.005.257.004.26.001.26-.001.257-.004.254-.005.25-.008.247-.011.244-.012.241-.014.237-.016.233-.018.231-.021.226-.022.224-.023.22-.026.216-.027.212-.028.21-.031.205-.032.202-.033.198-.034.194-.036.191-.038.187-.038.183-.04.179-.041.175-.042.172-.043.168-.043.163-.045.16-.046.155-.046.152-.048.148-.048.143-.048.139-.049.136-.05.131-.05.126-.051.123-.051.118-.051.114-.052.11-.052.106-.052.101-.052.096-.052.092-.052.088-.052.083-.052.079-.052.074-.051.07-.052.065-.051.06-.05.056-.05.051-.05.023-.025.023-.024.021-.024.02-.025.019-.024.018-.024.017-.023.015-.024.014-.023.013-.023.012-.023.01-.023.01-.022.008-.022.006-.023.006-.021.004-.022.004-.021.001-.021.001-.021-.001-.021-.001-.021-.004-.021-.004-.022-.006-.021-.006-.023-.008-.022-.01-.022-.01-.023-.012-.023-.013-.023-.014-.023-.015-.024-.017-.023-.018-.024-.019-.024-.02-.025-.021-.024-.023-.024-.023-.025-.051-.05-.056-.05-.06-.05-.065-.051-.07-.052-.074-.051-.079-.052-.083-.052-.088-.052-.092-.052-.096-.052-.101-.052-.106-.052-.11-.052-.114-.052-.118-.051-.123-.051-.126-.051-.131-.05-.136-.05-.139-.049-.143-.048-.148-.048-.152-.048-.155-.046-.16-.046-.163-.045-.168-.043-.172-.043-.175-.042-.179-.041-.183-.04-.187-.038-.191-.038-.194-.036-.198-.034-.202-.033-.205-.032-.21-.031-.212-.028-.216-.027-.22-.026-.224-.023-.226-.022-.231-.021-.233-.018-.237-.016-.241-.014-.244-.012-.247-.011-.25-.008-.254-.005-.257-.004-.26-.001-.26.001z"/></symbol></defs><defs><symbol id="clock" width="24" height="24"><path transform="scale(.5)" d="M12 2c5.514 0 10 4.486 10 10s-4.486 10-10 10-10-4.486-10-10 4.486-10 10-10zm0-2c-6.627 0-12 5.373-12 12s5.373 12 12 12 12-5.373 12-12-5.373-12-12-12zm5.848 12.459c.202.038.202.333.001.372-1.907.361-6.045 1.111-6.547 1.111-.719 0-1.301-.582-1.301-1.301 0-.512.77-5.447 1.125-7.445.034-.192.312-.181.343.014l.985 6.238 5.394 1.011z"/></symbol></defs><defs><marker id="arrowhead" refX="7.9" refY="5" markerUnits="userSpaceOnUse" markerWidth="12" markerHeight="12" orient="auto-start-reverse"><path d="M -1 0 L 10 5 L 0 10 z"/></marker></defs><defs><marker id="crosshead" markerWidth="15" markerHeight="8" orient="auto" refX="4" refY="4.5"><path fill="none" stroke="#000000" stroke-width="1pt" d="M 1,2 L 6,7 M 6,2 L 1,7" style="stroke-dasharray: 0, 0;"/></marker></defs><defs><marker id="filled-head" refX="15.5" refY="7" markerWidth="20" markerHeight="28" orient="auto"><path d="M 18,7 L9,13 L14,7 L9,1 Z"/></marker></defs><defs><marker id="sequencenumber" refX="15" refY="15" markerWidth="60" markerHeight="40" orient="auto"><circle cx="15" cy="15" r="6"/></marker></defs><g><rect x="414" y="735" fill="#EDF2AE" stroke="#666" width="639" height="58" class="note"/><text x="734" y="740" text-anchor="middle" dominant-baseline="middle" alignment-baseline="middle" class="noteText" dy="1em" style="font-size: 16px; font-weight: 400;"><tspan x="734">Caminho de abort: se algum voto for 409 ou houver timeout,</tspan></text><text x="734" y="759" text-anchor="middle" dominant-baseline="middle" alignment-baseline="middle" class="noteText" dy="1em" style="font-size: 16px; font-weight: 400;"><tspan x="734">o coordenador chama POST /internal/abort nos participantes que já votaram</tspan></text></g><text x="256" y="80" text-anchor="middle" dominant-baseline="middle" alignment-baseline="middle" class="messageText" dy="1em" style="font-size: 16px; font-weight: 400;">BOOK_ITINERARY (itinerário escolhido)</text><line x1="76" y1="115" x2="435" y2="115" class="messageLine0" stroke-width="2" stroke="none" marker-end="url(#arrowhead)" style="fill: none;"/><text x="440" y="130" text-anchor="middle" dominant-baseline="middle" alignment-baseline="middle" class="messageText" dy="1em" style="font-size: 16px; font-weight: 400;">Gera transaction_id e reserva os trechos locais</text><path d="M 440,165 C 500,155 500,195 440,185" class="messageLine0" stroke-width="2" stroke="none" marker-end="url(#arrowhead)" style="fill: none;"/><text x="559" y="210" text-anchor="middle" dominant-baseline="middle" alignment-baseline="middle" class="messageText" dy="1em" style="font-size: 16px; font-weight: 400;">POST /internal/prepare</text><line x1="440" y1="245" x2="678" y2="245" class="messageLine0" stroke-width="2" stroke="none" marker-end="url(#arrowhead)" style="fill: none;"/><text x="562" y="260" text-anchor="middle" dominant-baseline="middle" alignment-baseline="middle" class="messageText" dy="1em" style="font-size: 16px; font-weight: 400;">200 VOTE_COMMIT</text><line x1="681" y1="295" x2="443" y2="295" class="messageLine1" stroke-width="2" stroke="none" marker-end="url(#arrowhead)" style="stroke-dasharray: 3, 3; fill: none;"/><text x="732" y="310" text-anchor="middle" dominant-baseline="middle" alignment-baseline="middle" class="messageText" dy="1em" style="font-size: 16px; font-weight: 400;">POST /internal/prepare</text><line x1="440" y1="345" x2="1024" y2="345" class="messageLine0" stroke-width="2" stroke="none" marker-end="url(#arrowhead)" style="fill: none;"/><text x="735" y="360" text-anchor="middle" dominant-baseline="middle" alignment-baseline="middle" class="messageText" dy="1em" style="font-size: 16px; font-weight: 400;">200 VOTE_COMMIT</text><line x1="1027" y1="395" x2="443" y2="395" class="messageLine1" stroke-width="2" stroke="none" marker-end="url(#arrowhead)" style="stroke-dasharray: 3, 3; fill: none;"/><text x="440" y="410" text-anchor="middle" dominant-baseline="middle" alignment-baseline="middle" class="messageText" dy="1em" style="font-size: 16px; font-weight: 400;">Decisão global COMMIT, registra no log</text><path d="M 440,445 C 500,435 500,475 440,465" class="messageLine0" stroke-width="2" stroke="none" marker-end="url(#arrowhead)" style="fill: none;"/><text x="559" y="490" text-anchor="middle" dominant-baseline="middle" alignment-baseline="middle" class="messageText" dy="1em" style="font-size: 16px; font-weight: 400;">POST /internal/commit</text><line x1="440" y1="525" x2="678" y2="525" class="messageLine0" stroke-width="2" stroke="none" marker-end="url(#arrowhead)" style="fill: none;"/><text x="562" y="540" text-anchor="middle" dominant-baseline="middle" alignment-baseline="middle" class="messageText" dy="1em" style="font-size: 16px; font-weight: 400;">200 COMMITTED</text><line x1="681" y1="575" x2="443" y2="575" class="messageLine1" stroke-width="2" stroke="none" marker-end="url(#arrowhead)" style="stroke-dasharray: 3, 3; fill: none;"/><text x="732" y="590" text-anchor="middle" dominant-baseline="middle" alignment-baseline="middle" class="messageText" dy="1em" style="font-size: 16px; font-weight: 400;">POST /internal/commit</text><line x1="440" y1="625" x2="1024" y2="625" class="messageLine0" stroke-width="2" stroke="none" marker-end="url(#arrowhead)" style="fill: none;"/><text x="735" y="640" text-anchor="middle" dominant-baseline="middle" alignment-baseline="middle" class="messageText" dy="1em" style="font-size: 16px; font-weight: 400;">200 COMMITTED</text><line x1="1027" y1="675" x2="443" y2="675" class="messageLine1" stroke-width="2" stroke="none" marker-end="url(#arrowhead)" style="stroke-dasharray: 3, 3; fill: none;"/><text x="259" y="690" text-anchor="middle" dominant-baseline="middle" alignment-baseline="middle" class="messageText" dy="1em" style="font-size: 16px; font-weight: 400;">SUCCESS com booking_id</text><line x1="438" y1="725" x2="79" y2="725" class="messageLine1" stroke-width="2" stroke="none" marker-end="url(#arrowhead)" style="stroke-dasharray: 3, 3; fill: none;"/></svg>