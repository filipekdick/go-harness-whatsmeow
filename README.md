# go-harness-whatsmeow

Assistente WhatsApp multi-tenant em Go, com PostgreSQL, WhatsMeow, tool calling e seleção de modelos via OpenRouter ou Anthropic direto.

## Estado atual

O happy path do MVP está implementado em código:

```text
WhatsApp CUSTOMER → identidade → histórico → LLM → search_catalog → PostgreSQL → resposta na mesma sessão
```

Ele compila e possui testes unitários do registry, das ferramentas e do adaptador OpenRouter. Ainda é necessário executar um smoke test com PostgreSQL, uma conta OpenRouter e um aparelho WhatsApp reais. Consulte [Limitações antes de produção](#limitações-antes-de-produção).

## Por que OpenRouter

OpenRouter expõe uma API única para modelos de diferentes provedores. Trocar de Gemini para Claude ou outro modelo normalmente exige apenas alterar `LLM_MODEL`, sem trocar o código do harness.

Isso reduz custo de implementação, mas não garante que todos os modelos tenham o mesmo comportamento. Escolha um modelo que declare suporte a `tools`:

- <https://openrouter.ai/models?supported_parameters=tools>
- documentação de tool calling: <https://openrouter.ai/docs/guides/features/tool-calling>

Defina um limite de crédito na chave OpenRouter para controlar gastos. Mensagens, prompts e resultados de ferramentas são enviados ao OpenRouter e ao provedor escolhido; considere isso ao tratar dados pessoais.

## Pré-requisitos

- Go 1.25 ou compatível com `go.mod`;
- PostgreSQL;
- cliente `psql` para o bootstrap inicial;
- chave OpenRouter ou Anthropic;
- uma conta WhatsApp para a linha CUSTOMER.

No Termux, PostgreSQL e `psql` fazem parte do pacote `postgresql`.

## 1. Configuração

```sh
cp .env.example .env
```

Edite `.env` e configure pelo menos:

```text
DATABASE_URL
LLM_PROVIDER=openrouter
OPENROUTER_API_KEY
LLM_MODEL
```

Carregue as variáveis no shell:

```sh
set -a
. ./.env
set +a
```

Para usar Anthropic diretamente:

```text
LLM_PROVIDER=anthropic
ANTHROPIC_API_KEY=...
LLM_MODEL=...
```

Somente a chave do provedor selecionado é obrigatória.

## 2. Migrações

O comando de migração não exige chave de LLM nem inicia o WhatsApp:

```sh
go run ./cmd/harness migrate
```

Para compilar um binário:

```sh
mkdir -p bin
go build -o bin/harness ./cmd/harness
./bin/harness migrate
```

## 3. Bootstrap da empresa e linha CUSTOMER

O script é idempotente para uma empresa ativa com o mesmo nome. Ele cria ou reativa uma linha CUSTOMER ainda não pareada:

```sh
psql "$DATABASE_URL" \
  -v company_name="Minha Empresa" \
  -v system_prompt="Você atende clientes da Minha Empresa. Consulte o catálogo antes de afirmar preço, estoque ou disponibilidade. Responda de forma curta em português." \
  -f scripts/bootstrap.sql
```

Não é necessário cadastrar clientes: o primeiro contato na linha CUSTOMER cria automaticamente um usuário CUSTOMER.

## 4. Catálogo mínimo

Cadastre ao menos um produto. O exemplo localiza a empresa pelo nome:

```sh
psql "$DATABASE_URL" <<'SQL'
WITH company AS (
    SELECT id FROM companies
    WHERE name = 'Minha Empresa' AND is_active
    ORDER BY id LIMIT 1
)
INSERT INTO products
    (company_id, name, description, category, tags, price, stock)
SELECT
    id,
    'Café 500g',
    'Café torrado e moído, pacote de 500g',
    'Alimentos',
    ARRAY['café', '500g'],
    18.90,
    25
FROM company;
SQL
```

Produtos e serviços ativos são pesquisados por nome, descrição, categoria e tags.

## 5. Executar e parear o WhatsApp

```sh
go run ./cmd/harness run
```

Ou, com o binário:

```sh
./bin/harness run
```

No primeiro uso, o terminal exibirá um QR Code. No aparelho da linha comercial:

```text
WhatsApp → Aparelhos conectados → Conectar um aparelho
```

Após o pareamento, o JID do dispositivo fica em `wa_channels` e as credenciais da sessão ficam nas tabelas do sqlstore do WhatsMeow. Reinícios normais não devem exigir um novo QR.

## 6. Smoke test

De outro número, envie para a linha pareada:

```text
Vocês têm café de 500g? Qual o preço?
```

O resultado esperado é:

1. criação automática do cliente;
2. criação da conversa;
3. chamada do modelo pelo OpenRouter;
4. execução de `search_catalog`;
5. nova chamada ao modelo com o resultado;
6. resposta enviada pela mesma sessão WhatsApp.

Os logs devem mostrar `tool executed` com `tool=search_catalog`.

## Seleção de provedor

| `LLM_PROVIDER` | Credencial | Modelo |
|---|---|---|
| `openrouter` | `OPENROUTER_API_KEY` | slug OpenRouter, por exemplo `provider/model` |
| `anthropic` | `ANTHROPIC_API_KEY` | nome de modelo Anthropic |

O cliente OpenRouter:

- usa `/api/v1/chat/completions`;
- suporta chamadas paralelas de ferramentas;
- preserva `reasoning_details` no histórico;
- normaliza chamadas para `StopReason=tool_use`;
- tenta no máximo três vezes;
- repete em timeout, 429 e 5xx;
- respeita `Retry-After` e cancelamento de contexto.

## Testes

```sh
go test ./...
go test -race ./...
go vet ./...
```

Os testes não fazem chamadas pagas nem conectam ao WhatsApp.

## Limitações antes de produção

O projeto já pode ser usado para um smoke test controlado, mas ainda não oferece entrega confiável:

- `processed_messages` é marcado antes de toda a operação; uma falha posterior pode impedir reprocessamento;
- não existe inbox/outbox durável;
- falha ou fila cheia pode perder mensagem;
- envio WhatsApp não possui retry persistente;
- as queries novas ainda precisam de teste contra PostgreSQL real;
- o runner de migração ainda possui uma janela entre aplicar SQL e registrar a versão;
- não existem scripts de supervisão/wakelock para Termux;
- ferramentas EMPLOYEE e confirmação de escrita ainda não foram implementadas.

Antes de um piloto com clientes reais, a prioridade é adicionar um teste de aceitação com PostgreSQL real e depois inbox/outbox com retry.
