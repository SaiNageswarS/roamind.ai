#!/usr/bin/env bash
# Serves one of Basic Memory's search models with vLLM on the local GPU, on
# loopback only. Started by process-compose; install with task vllm.
#
#   deploy/bin/run-vllm.sh embed    Qwen3-Embedding-0.6B on :8800 (/v1/embeddings)
#   deploy/bin/run-vllm.sh rerank   Qwen3-Reranker-0.6B  on :8801 (/v1/rerank)
#
# Both fit an 8 GB GPU: each takes 35% of it, with a 4096-token context
# (memory notes and rerank documents are short) and no CUDA graphs.
# The model names, ports and embedding size are mirrored in
# memory/basic-memory-config.json.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
common=(--runner pooling --host 127.0.0.1 --gpu-memory-utilization 0.35
  --max-model-len 4096 --enforce-eager)

case "${1:-}" in
embed)
  exec vllm serve Qwen/Qwen3-Embedding-0.6B "${common[@]}" \
    --port 8800 --served-model-name qwen3-embedding-0.6b
  ;;
rerank)
  # The original Qwen3 reranker scores the "yes" token; vLLM serves it as a
  # classifier with this template (default instruction set for our notes).
  exec vllm serve Qwen/Qwen3-Reranker-0.6B "${common[@]}" \
    --port 8801 --served-model-name qwen3-reranker-0.6b \
    --hf-overrides '{"architectures":["Qwen3ForSequenceClassification"],"classifier_from_token":["no","yes"],"is_original_qwen3_reranker":true}' \
    --chat-template "$ROOT/deploy/vllm/qwen3_reranker.jinja"
  ;;
*)
  echo "usage: $0 embed|rerank" >&2
  exit 2
  ;;
esac
