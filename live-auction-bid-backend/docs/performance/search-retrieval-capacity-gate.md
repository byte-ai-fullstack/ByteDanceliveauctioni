# 搜索检索容量门禁

蓝图的搜索延迟 SLO 固定为：Elasticsearch 与 pgvector 两路检索阶段各自 p99 `< 1000ms`。它不包含 DashScope/其他 LLM 的答案生成时间；完整 `/api/ai/buyer/consult` 时延单独记录，不能拿模型时延掩盖检索退化。

## 执行前提

- 在隔离的容量环境运行，并将 `AUCTION_AI_PROVIDER=mock`，避免外部模型抖动和费用污染检索结论；脚本会从 `auction_ai_assistant_info` 验证运行实例的实际模式，不接受仅由操作者声明。
- Gateway 必须同时配置 Elasticsearch、Embedding 和 pgvector；缺失任一路样本直接判失败，不允许把 MySQL 降级路径当成双索引性能通过。
- `GATEWAY_METRICS_URL` 必须指向本次接收查询的 Gateway 指标端点。
- 真实容量运行必须显式设置 `CONFIRM_SEARCH_LOAD=1`。

## 命令

```bash
CONFIRM_SEARCH_LOAD=1 \
SEARCH_LOAD_AI_MODE=mock \
BASE_URL=http://127.0.0.1:18080 \
GATEWAY_METRICS_URL=http://127.0.0.1:18080/metrics \
REQUESTS=5000 \
CONCURRENCY=100 \
RETRIEVAL_P99_LIMIT_MS=1000 \
API_P99_LIMIT_MS=3000 \
REPORT_FILE=test-results/search-retrieval-5000.json \
node scripts/load-search-retrieval.mjs
```

脚本在 warmup 后抓取一次 Prometheus 基线，发送本轮查询，再抓取结束指标。它先检查 counter reset；测量窗口内没有 reset 时，再计算本轮 `auction_search_retrieval_duration_ms_bucket` 增量，并分别验证：

- `elasticsearch` 与 `pgvector` 都产生成功检索样本；
- 两路 `result="error"` 增量均为零；
- 两路检索 p99 均不超过 `1000ms`；
- mock-AI 完整接口 p99 不超过 `3000ms`，且默认不允许 HTTP/业务错误。
- Gateway 在测量窗口内没有重启，检索直方图和计数器没有 reset；否则本轮证据直接失效。

报告中的直方图 p99 使用“命中分位点的桶上界”，比 Prometheus 桶内线性插值更保守。验收时同时保存 JSON 报告和对应时间窗的 Grafana/Prometheus 快照。
