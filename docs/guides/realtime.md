# Real-time processing

The **realtime-processor** demonstrates production real-time AI patterns: it consumes
Kafka events, enriches them with online features, scores them with a model or agent,
and publishes results — all live, surfaced on the console's Real-Time panel.

Gateway examples assume `export MLAIOPS_URL=http://localhost:8080`.

## The three demos

| Demo | Input topic | Output topic | What it does |
| --- | --- | --- | --- |
| **Fraud detection** | `mlaiops.transactions` | `mlaiops.fraud.alerts` | Enriches a transaction with 5-minute stats, scores with the live fraud model (`FRAUD_MODEL_ENDPOINT`) or a built-in rule, flags suspicious ones |
| **Call-center analysis** | `mlaiops.callcenter.transcripts` | `mlaiops.callcenter.insights` | Runs a LangGraph agent (`CALLCENTER_AGENT_ID`) or keyword sentiment for intent + summary |
| **Recommendations** | `mlaiops.user.activity` | `mlaiops.recs.results` | Looks up profile features and returns a plan-ranked catalog |

## Producing events

```bash
docker compose -f deploy/compose.yaml exec jupyter \
  python -m realtime.produce --demo fraud --count 5
docker compose -f deploy/compose.yaml exec jupyter \
  python -m realtime.produce --demo callcenter --count 2
docker compose -f deploy/compose.yaml exec jupyter \
  python -m realtime.produce --demo recommendations --count 2
```

Or from the workbench terminal / any Python env with `KAFKA_REST_URL` set.

## Watching the results

=== "Console"

    Open the **Real-Time** panel. Each demo card shows events processed, average
    latency, and (for fraud) flagged counts — updating live over SSE.

=== "API"

    ```bash
    curl -s "$MLAIOPS_URL/api/v1/realtime" | python -m json.tool
    ```

    ```json
    {"demos": {"fraud": {"events": 12, "avg_latency_ms": 1.82, "flagged": 4,
                         "updated_at": "..."}}}
    ```

## How a single event flows

1. The processor polls an input topic via the Kafka REST proxy.
2. It enriches the event with **online features** from the feature gateway.
3. It scores with a live model endpoint or agent — or a deterministic fallback rule
   when none is configured.
4. It produces the result to the output topic and records latency.
5. It reports stats to `POST /api/v1/realtime/{demo}` for the console.

## Wiring the demos to live components

The demos run out of the box with built-in fallbacks. To use real components:

```bash
# .env
FRAUD_MODEL_ENDPOINT=http://mlaiops-serve-churn-classifier:5001   # a deployed model
CALLCENTER_AGENT_ID=agt-...                                       # a deployed agent
```

Then restart the processor:

```bash
docker compose -f deploy/compose.yaml up -d realtime-processor
```

## Resilience

The consumer loop tolerates transient Kafka REST timeouts (common during consumer
group rebalances): rather than crashing — which would leak a consumer instance and
trigger another rebalance, i.e. a crash loop — it recreates its consumer and
continues. Stats reporting resumes automatically.
