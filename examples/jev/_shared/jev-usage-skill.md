General rules for calling the `jev` tool (TypeSafe AI's System One model),
shared across every example in this directory via `skills: [jev-usage]`.
The `criteria` shape rules below matter in particular — a `score` question
fed the wrong shape fails only at Jev's API layer, not at config load.

Pick the right question type by what the answer means, not by habit:
- `noul` — probability that a yes/no condition holds. `criteria` is an
  OPTIONAL object with `true`/`false` rubric text.
- `choice` — pick exactly one of a fixed set of named options. `criteria`
  is a REQUIRED object mapping each option name to its description.
- `score` — a continuous position along a described spectrum. `criteria`
  is a REQUIRED ORDERED ARRAY (2-10 elements) of level descriptions, each
  a plain string or an object like `{"what": "...", "examples": [...]}`.
  Levels are identified by their POSITION in the array, not by any field
  inside the element — never invent a numeric `score` field per level.

Getting the `criteria` shape wrong for the type you used fails at Jev's
API layer with a 422, not at rakitsu's own config validation or
`rakitsu doctor` — get it right the first time rather than discovering it
via a live error.

Give Jev enough `state` to actually answer — the specific claim or finding
text, plus any grounding material relevant to it, not just a vague
restatement. If you have several independent judgments to make about the
same state, ask them as separate questions in one call rather than several
calls — they run in parallel and don't see each other's answers, so don't
rely on one question's phrasing referencing another's.

A `noul` near 0.5 means the model finds yes and no similarly likely, not
"medium intensity." A `choice`'s `confidence` and a `score`'s `confidence`
summarize how concentrated the probability distribution is, not whether
the underlying judgment is correct — don't treat high confidence as
permission to skip your own sanity-checking of what Jev actually said.
