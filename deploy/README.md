# Deploying Roamind to Azure Container Apps

This runbook deploys the Roamind personal assistant to **Azure Container
Apps** (ACA) as a single Container App with three co-located containers:

| Container | Role                             | Ports                |
|-----------|----------------------------------|----------------------|
| `gateway` | Main — gRPC ingress, Telegram    | 50051 (gRPC), 8081   |
| `agent`   | Sidecar — LangGraph worker       | none (Redis streams) |
| `redis`   | Sidecar — task broker / cache    | 6379 (loopback only) |

Because the three containers share the same revision they share a
loopback network namespace, so both the gateway and the agent reach
Redis at `redis://localhost:6379`.

---

## Prerequisites

- Docker Desktop / Docker Engine (local builds + push)
- Docker Hub account, logged in: `docker login`
- Azure CLI ≥ 2.53 with the `containerapp` extension:

  ```bash
  az extension add --name containerapp --upgrade
  az provider register -n Microsoft.App
  az provider register -n Microsoft.OperationalInsights
  ```

- MongoDB Atlas connection string (long-term memory)
- Anthropic API key

---

## 1. Build & push images

Run from the **repository root** (the gateway build needs `proto/`):

```bash
export TAG=v1

# Gateway (build context = repo root)
docker build -f gateway/Dockerfile -t sainageswar/roamind-gateway:$TAG .

# Agent (build context = ./agent)
docker build -f agent/Dockerfile -t sainageswar/roamind-agent:$TAG ./agent

docker push sainageswar/roamind-gateway:$TAG
docker push sainageswar/roamind-agent:$TAG
```

### Optional local smoke test

```bash
docker network create rm-net
docker run -d --network rm-net --name redis redis:7-alpine
docker run --rm --network rm-net \
  -e REDIS_URL=redis://redis:6379 \
  -e ACCESS-SECRET=test \
  -p 50051:50051 \
  sainageswar/roamind-gateway:$TAG
# In another shell:
docker run --rm --network rm-net \
  -e REDIS_URL=redis://redis:6379 \
  -e ANTHROPIC_API_KEY=$ANTHROPIC_API_KEY \
  sainageswar/roamind-agent:$TAG
```

---

## 2. Provision Azure resources

```bash
export SUB=b5a899fa-8813-4bd0-b643-5852e24064ee
export RG=roamind-rg
export LOC=centralindia
export ENV=roamind-env
export APP=roamind

az login
az account set --subscription $SUB

az group create -n $RG -l $LOC
az containerapp env create -n $ENV -g $RG -l $LOC
```

---

## 3. Create the Container App (gateway only, first revision)

ACA's `az containerapp create` only takes one inline container — the
sidecars are added by applying the YAML in step 4.

```bash
az containerapp create \
  --name $APP \
  --resource-group $RG \
  --environment $ENV \
  --image docker.io/sainageswar/roamind-gateway:$TAG \
  --target-port 50051 \
  --transport http2 \
  --ingress external \
  --min-replicas 1 \
  --max-replicas 3 \
  --cpu 0.5 --memory 1.0Gi
```

---

## 4. Deploy redis and agent container 

In azure portal, in containers add redis:latest and agent container with environment variables.

---

## 5. End-to-end test from the local CLI

ACA terminates TLS at port 443. The current CLI dials with
`insecure.NewCredentials()` so it cannot reach the public ingress as-is
— see **Known limitation** below.

Once the CLI supports TLS:

```bash
export GATEWAY_ADDR=$(az containerapp show -n $APP -g $RG \
  --query properties.configuration.ingress.fqdn -o tsv):443
export CLI_JWT_TOKEN=<jwt-signed-with-ACCESS-SECRET>

./build/roamind-cli -gateway $GATEWAY_ADDR -token $CLI_JWT_TOKEN
```

---
