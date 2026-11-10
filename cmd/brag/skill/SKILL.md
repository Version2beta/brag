---
name: brag
description: Produce evidence-focused repository contribution and engineering summaries using BRAG's static Git analysis. Use when asked to analyze contribution history, repository changes, or engineering evidence with brag.
---

# BRAG repository evidence

Run `brag all` for the repository-wide history, or `brag --base REF` to analyze the branch relative to a baseline. The default mode is static: it reads Git and repository evidence without executing repository tests.

Do not infer contributor identity from context. Use only identities explicitly configured or selected by the user. Preserve the report's denominators, unavailable statuses, and warnings; do not convert missing evidence into zero or omit denominator context.

Present results as a compact Markdown infographic/dashboard, not an accountant-style narrative or metric dump. Begin with a concise at-a-glance band for scope, history boundary and base/merge-base (explicitly unavailable/not provided when absent), identity availability, and current tracked-worktree state. Follow with exactly six compact cards: Contributions, Complexity, Testing, Documentation, Security, and Engineering usability. Prefer metric labels, compact tables or bullets, supplied statuses, and short citations to JSON Pointer paths and measured snapshots; avoid paragraphs.

Keep repository-wide measures distinct from developer-attributable measures and never infer identity. Preserve supplied ratios, denominators, units, statuses, methods, truncation, and HEAD versus current-tracked-worktree distinctions. Tests are inventory only, not run; security indicators are not findings and the scanner was not run. Disclose Go-only evidence and non-Go unsupported status; unavailable evidence stays explicit. Do not fabricate scores, rankings, quality labels, composite/progress bars, or unsupported derivations.

Describe measured evidence neutrally. Do not make rankings or claims about code quality, security, productivity, or overall product quality from these measures.

Dynamic tests and mutation analysis execute repository code without sandboxing, with host privileges. Do not use dynamic or mutation flags unless the user explicitly requests execution; explain this unsandboxed risk before running them. Never present execution as safe merely because BRAG uses disposable snapshots.
