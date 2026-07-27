#!/usr/bin/env bash
set -euo pipefail

es_url="${AUCTION_ES_URL:-http://elasticsearch:9200}"
index_name="${AUCTION_ES_INDEX_NAME:-auction-lots-v1}"
write_alias="${AUCTION_ES_WRITE_ALIAS:-auction-lots-current}"
mapping_file="${AUCTION_ES_MAPPING_FILE:-/auction-elasticsearch/index-v1.json}"
username="${AUCTION_ES_USERNAME:-}"
password="${AUCTION_ES_PASSWORD:-}"

curl_auth=()
if [[ -n "${username}" || -n "${password}" ]]; then
  if [[ -z "${username}" || -z "${password}" ]]; then
    echo "Elasticsearch username and password must be configured together" >&2
    exit 1
  fi
  curl_auth=(-u "${username}:${password}")
fi

if ! curl -fsS "${curl_auth[@]}" "${es_url}/_nodes/plugins?filter_path=nodes.*.plugins.name" | grep -q '"analysis-ik"'; then
  echo "analysis-ik is missing from Elasticsearch" >&2
  exit 1
fi

if ! curl -fsS -o /dev/null "${curl_auth[@]}" "${es_url}/${index_name}"; then
  curl -fsS "${curl_auth[@]}" -X PUT \
    -H 'Content-Type: application/json' \
    --data-binary "@${mapping_file}" \
    "${es_url}/${index_name}" >/dev/null
fi

alias_response="$(curl -fsS "${curl_auth[@]}" "${es_url}/_alias/${write_alias}" 2>/dev/null || true)"
if [[ -z "${alias_response}" ]]; then
  curl -fsS "${curl_auth[@]}" -X POST \
    -H 'Content-Type: application/json' \
    -d "{\"actions\":[{\"add\":{\"index\":\"${index_name}\",\"alias\":\"${write_alias}\",\"is_write_index\":true}}]}" \
    "${es_url}/_aliases" >/dev/null
elif ! grep -q '"is_write_index":true' <<<"${alias_response// /}"; then
  echo "Elasticsearch alias ${write_alias} exists without an explicit write index" >&2
  exit 1
fi
