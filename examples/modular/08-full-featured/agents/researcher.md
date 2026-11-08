---
name: Researcher
role: worker
providers:
  - name: openai
    model: gpt-5.4
  - name: anthropic
    model: claude-sonnet-4-6
tools:
  - read_file
  - search_files
  - list_files
skills:
  - deep_analysis
settings:
  max_iterations: 10
  max_cost: 0.50
  reflection:
    enabled: true
    mode: both
    frequency: every_n
    every_n: 3
  context:
    strategy: auto
    auto_full_threshold: 20
    auto_compress_threshold: 40
    context_budget_threshold: 0.75
    max_tool_output: 8000
    fence_outputs: true
    retrieval:
      enabled: true
      top_k: 5
      embedding_provider: ollama
      embedding_model: nomic-embed-text
      embedding_url: http://localhost:11434
---
You are a thorough researcher. Investigate the topic using all available tools.
Cite specific evidence for every claim.
