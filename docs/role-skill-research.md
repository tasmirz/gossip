# Research: reusable coding-agent roles and skills

**Research date:** 2026-11-08  
**Scope:** Primary-source guidance for Gossip's coordinator, researcher, implementer, reviewer, verifier, and integrator presets; skill packaging, evaluation, and provenance. This is guidance research, not a performance evaluation of Gossip's current presets.

## Findings

### Current first-party guidance checked on 2026-11-08

OpenAI's current plugin documentation describes skills as reusable workflow instructions and an MCP server as the place for live data, authentication, authorization, and controlled actions. Skill instructions can guide how an agent uses the coordination MCP, but must never grant project access or override the server's permission checks. OpenAI also recommends keeping skill descriptions short because all metadata participates in routing, loading detailed instructions only when selected, and evaluating changes against captured runs for outcome, process, style, and efficiency. [OpenAI Skills](https://developers.openai.com/plugins/concepts/skills), [Rethinking skills and prompts](https://developers.openai.com/blog/rethinking-skills-and-prompts-for-gpt-6-astra), [Testing Agent Skills systematically](https://developers.openai.com/blog/eval-skills).

OpenAI's Agents SDK documentation distinguishes manager-style orchestration from handoffs to a specialist. That supports Gossip's coordinator/researcher/reviewer split, while local instructions remain advisory and the MCP service enforces access. Its current maintenance example stores repository-specific skills beside the code, uses them for repeatable workflows, and runs them through evaluations. [Multi-agent orchestration](https://openai.github.io/openai-agents-python/multi_agent/), [Skills to accelerate OSS maintenance](https://developers.openai.com/blog/skills-agents-sdk).

The OpenAI skills catalog repository is now explicitly marked deprecated and points readers to the OpenAI Plugins repository for current examples. Do not use its old curated directory as a live, authoritative catalog. [Deprecated catalog notice](https://github.com/openai/skills).

Anthropic's current platform docs similarly describe progressive disclosure and warn that repository skills can be loaded without review and should be treated as trusted executable instructions. This is a platform-specific implementation detail, not a universal guarantee that every coding-agent host reads the same skill locations or file formats. Gossip should show source, version, and host compatibility and require an explicit owner review before a third-party skill is trusted. [Agent Skills overview](https://platform.claude.com/docs/en/agents-and-tools/agent-skills/overview), [Skills for managed agents](https://platform.claude.com/docs/en/managed-agents/skills).

The six shipped Gossip presets remain original, editable duty templates. They are not copies of those external skills and are not labeled “best”; each preset now exposes its HTTPS guidance sources so users can inspect the material behind it. Representative task evaluations and cross-host compatibility checks remain open before changing defaults or importing third-party skill content.

### Role boundaries should describe work, not authority

The OpenAI Agents SDK documents two useful collaboration patterns: a manager can keep control and call specialists for bounded work, or it can hand off control when a specialist should own the remaining turn. It recommends specialization, clear tool constraints, monitoring, and evaluations. Gossip's coordinator role should therefore keep ownership of the user-facing plan and synthesis; research, implementation, review, verification, and integration should be assigned as bounded work with explicit inputs and deliverables. A duty preset must not grant project or system permissions. [Agent orchestration](https://openai.github.io/openai-agents-python/multi_agent/), [agent definition and handoffs](https://openai.github.io/openai-agents-python/agents/)

Anthropic's published research system reports that subagents can explore independent directions with separate context windows and return compressed findings to a lead agent. The same source reports substantially higher token use for multi-agent runs, and cautions that highly dependent work is a poorer fit. These findings support parallel research when questions are independent, with compact reports and a synthesis step; they do not support making every coding task multi-agent by default. The performance figures are Anthropic's internal evaluation results, not a prediction for Gossip. [Multi-agent research system](https://www.anthropic.com/research/multi-agent-research-system)

The sources do not establish one authoritative set of “best” job descriptions for Gossip's six coding roles. Use the project's task contracts, review requirements, and observed corrections as the primary source for role duties. Treat role names as user-editable starting presets, not fixed identities.

### Skills are compact workflow packages

The Agent Skills specification defines a portable directory with a required `SKILL.md` and optional scripts, references, and assets; frontmatter supports license and metadata such as version. OpenAI's plugin guidance distinguishes the MCP server's authenticated data/actions from a skill's reusable workflow, examples, and resources. Keep tools/policy authoritative in the service and use skills to explain when and how to use them. [Agent Skills specification](https://agentskills.io/specification), [OpenAI skills in plugins](https://developers.openai.com/plugins/concepts/skills)

Official skill-creation guidance recommends grounding skills in real expertise and task artifacts, keeping coherent units, using progressive disclosure, and preferring useful procedures over generic declarations. OpenAI's current prompt guidance also warns that long or overlapping skill descriptions consume context and make invocation less reliable. [Agent Skills best practices](https://agentskills.io/skill-creation/best-practices), [Rethinking skills and prompts](https://developers.openai.com/blog/rethinking-skills-and-prompts-for-gpt-6-astra)

### Evaluate changes instead of declaring a preset “best”

OpenAI's skill-evaluation guidance recommends defining success before writing a skill and comparing captured runs with a small set of outcome, process, style, and efficiency checks. For Gossip, each preset/skill should be evaluated on whether the right role was selected, the requested deliverable and evidence were produced, policy boundaries were respected, and unnecessary model/tool calls were avoided. [Testing agent skills systematically](https://developers.openai.com/blog/eval-skills)

## Curation and licensing recommendation

1. Keep Gossip's role duties original and project-specific. Summarize public guidance in this report; do not copy a third-party role pack into a preset without a use case, review, and recorded source.
2. When importing any third-party skill, inspect the license in that skill's own directory, retain required notices, record source URL, repository commit, license, and review date, and pin the imported version. Do not infer the license of every skill from its parent repository.
3. Treat bundled instructions as untrusted content: they may guide workflow, but cannot grant permissions, change project policy, or introduce tools/endpoints without explicit administrator review.
4. Give each role duty a small acceptance rubric and compare it against representative tasks before changing the default. Track quality, tool/model calls, context size, and estimated/observed cost separately; do not label a template “best” without that evidence.
5. Use parallel delegation for independent research or review questions. Keep dependent implementation and integration work sequenced, and return a compact result with sources, uncertainties, and next questions to the coordinator.

The older [OpenAI skills catalog](https://github.com/openai/skills) is marked deprecated; it directs users to the current plugins repository. It also says an individual skill's license is specified in that skill's directory. The [OpenAI plugins repository](https://github.com/openai/plugins) is the better current example for packaged Codex skills, but its individual skill licenses and contents still require review before reuse.

## Practical preset guidance for Gossip

| Preset | Duty boundary | Compact handoff |
| --- | --- | --- |
| Coordinator | Maintain the current user-approved plan, split independent questions, choose sequential vs parallel work, reconcile conflicting reports, and ask for user decisions when needed. | Decisions, assignments, dependencies, unresolved questions, and plan revision. |
| Researcher | Investigate only assigned questions; cite source paths or URLs; distinguish observation, inference, and uncertainty; avoid changing the plan. | Findings, evidence index, uncertainties, and recommendation. |
| Implementer | Change the assigned scope in the selected workspace; follow the accepted interfaces and baseline; run relevant checks when requested or required by the task contract. | Changed files, baseline, checks, remaining risks, and candidate/report reference. |
| Reviewer | Apply the planned review type and depth to the exact submitted revision; report actionable findings and uncovered scope; request targeted evidence when needed. | Verdict, findings by severity, exact revision/artifact, coverage, and limitations. |
| Verifier | Check each acceptance criterion against reproducible evidence and the recorded revision; state unavailable checks explicitly. | Criterion-to-evidence matrix, commands/results, environment, and gaps. |
| Integrator | Reconcile accepted changes in the integration workspace, resolve conflicts, verify the combined revision, and preserve provenance for each input. | Combined revision, included inputs, conflict decisions, checks, and residual risks. |

These are candidate duty boundaries distilled from Gossip's own plan plus the cited orchestration and skill-authoring guidance. They are not tested defaults and do not replace the existing permission model.
