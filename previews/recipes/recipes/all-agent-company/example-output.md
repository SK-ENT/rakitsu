# Captured order decision

```json
{
  "id": "WHOLESALE-120",
  "decision": "HOLD",
  "shortage": 40,
  "discount_percent": 10,
  "quotes": [
    {
      "name": "Fast Supply",
      "on_time": true,
      "shipping_cents": 6000,
      "quote_cents": 27600
    },
    {
      "name": "Budget Supply",
      "on_time": false,
      "shipping_cents": 2000,
      "quote_cents": 23600
    }
  ],
  "blockers": [
    "No supply option meets both the requested date and budget.",
    "Requested discount exceeds policy.",
    "Approval is missing for a quote at or above the threshold."
  ],
  "selected_quote": null
}
```

## Customer reply draft

On hold due to supply constraints and discount policy limits. Please confirm if timing or budget adjustments are possible.
