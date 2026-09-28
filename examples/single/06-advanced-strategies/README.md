# 06 — Advanced Strategies

Four standalone configs demonstrating advanced orchestration strategies.

## Run

Each command below runs one strategy: Hierarchical (supervisor assigns
reviewers dynamically), Plan and Execute (plan first, then execute steps),
Pipeline Loop (iterative refinement until quality passes), and Pipeline DAG
(parallel execution with dependency ordering).

```bash
rakitsu run examples/single/06-advanced-strategies/hierarchical.yaml "Review the authentication module"

rakitsu run examples/single/06-advanced-strategies/plan-and-execute.yaml "Migrate users table to add email verification"

rakitsu run examples/single/06-advanced-strategies/pipeline-loop.yaml "Write a sorting algorithm"

rakitsu run examples/single/06-advanced-strategies/pipeline-dag.yaml "Generate a REST API from this schema"
```

## What's Here

| File | Strategy | Description |
|------|----------|-------------|
| `hierarchical.yaml` | Hierarchical | Supervisor dynamically assigns tasks to specialist reviewers |
| `plan-and-execute.yaml` | PlanAndExecute | Planner creates a plan, Executor runs each step |
| `pipeline-loop.yaml` | Pipeline + Loop | Iterative code improvement until reviewer approves |
| `pipeline-dag.yaml` | Pipeline + DAG | Parallel code generation with `depends_on` ordering |

## Demonstrates

- All 4 advanced orchestration strategies
- Loop steps with iteration limits
- DAG dependencies (`depends_on`) for parallel execution ordering
- When to use each strategy (see top-level [examples/README.md](../../README.md#strategy-guide))
