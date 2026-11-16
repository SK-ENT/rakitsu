# Decision-only pipeline

Run a local Laya server on loopback using the [Laya setup guide](https://github.com/NandhaKishorM/laya). The default Rakitsu endpoint is `http://127.0.0.1:8000/v1/systemone`. Once the weights are available locally, the example runs without chat-model credentials or cloud calls.

```sh
rakitsu run examples/jev/10-pipeline-decision/config.yaml "I was charged twice" --no-hub
```

Change the Decision step's `backend` to run the same questions on another provider. Set the matching credential in the Rakitsu process environment.

| Backend | Decision model | Credential |
| --- | --- | --- |
| `laya` | Local Laya checkpoint | None |
| `jev` | TypeSafe Jev | `TYPESAFE_API_KEY` |
| `openai` | OpenAI Decisions with `gpt-6-luna` | `OPENAI_API_KEY` |
| `gemini` | Gemini Developer API with `gemini-decision-1` | `GEMINI_API_KEY` or `GOOGLE_API_KEY` |

The Gemini adapter calls the native [Gemini Decisions API](https://blog.google/innovation-and-ai/technology/developers-tools/gemini-decisions-api/) on the Developer API. Google Cloud support is pending. Cloud backends send the configured text and questions to the selected provider. Retrieval evidence input is supported with local Laya. Hosted endpoint overrides must use the selected provider's HTTPS host or a literal loopback address for a local test fixture. Use fixture credentials with loopback overrides. Endpoint URLs cannot include credentials, query parameters, or fragments.

The response uses `probability_true` for a predicate, `choice` for a named option, and `score` for a weighted position on an ordered rubric starting at zero. Use labeled examples to check routing thresholds when you change backends.

A Decision without routes returns named answers as JSON. To route to an agent, define the agents in the config and add routes to the Decision. The first matching route runs its agent steps in order. A valid answer that matches no route runs the explicit fallback.

```yaml
routes:
  - when: { question: department, field: choice, equals: billing }
    steps:
      - { name: handle_billing, agent: billing }
otherwise:
  - { name: manual_review, agent: review }
```
