# 📜 Open-Source Attributions, Papers & References

Gossip synthesizes state-of-the-art breakthroughs across multi-agent coordination, knowledge graph RAG, dialectic deliberation, and AI security. We gratefully acknowledge and attribute the following open-source projects, research papers, and authoring teams:

---

## 1. Multi-Agent Orchestration & Standard Operating Procedures (SOPs)

### MetaGPT
- **Repository:** [`geekan/MetaGPT`](https://github.com/geekan/MetaGPT)
- **Authors:** Sirui Hong, Mingchen Zhuge, Jonathan Chen, Xiawu Zheng, Cheng Hao, Jinlin Wang, Ceyao Zhang, Zili Wang, Steven Ka Shing Yau, Deyu Lin, Liyang Zhou, Chenglin Wu, et al.
- **Paper:** *"MetaGPT: Meta Programming for A Multi-Agent Collaborative Framework"* (arXiv:2308.00352)
- **License:** MIT License
- **Key Concept Adopted:** The formulation $\text{Code} = \text{SOP}(\text{Team})$, enforcing sequential assembly-line phase gates (Requirements $\to$ Architecture $\to$ Task DAG $\to$ Implementation $\to$ QA Review $\to$ Security Audit $\to$ Release) and exchanging structured, versioned Markdown artifacts instead of loose conversational chatter.

### Microsoft AutoGen
- **Repository:** [`microsoft/autogen`](https://github.com/microsoft/autogen)
- **Authors:** Qingyun Wu, Gagan Bansal, Jieyu Zhang, Yiran Wu, Beibin Li, Erkang Zhu, Li Jiang, Xiaoyun Zhang, Chi Wang
- **Paper:** *"AutoGen: Enabling Next-Gen LLM Applications via Multi-Agent Conversation"* (arXiv:2308.08155)
- **License:** Creative Commons / MIT License
- **Key Concept Adopted:** Role-based multi-agent message routing, group chat managers, and event-driven asynchronous communication.

### CrewAI
- **Repository:** [`joaomdmoura/crewAI`](https://github.com/joaomdmoura/crewAI)
- **Author:** João Moura
- **License:** MIT License
- **Key Concept Adopted:** Role taxonomy, behavioral directives, and goal-driven task delegation across specialist agents.

### ChatDev
- **Repository:** [`OpenBMB/ChatDev`](https://github.com/OpenBMB/ChatDev)
- **Authors:** Chen Qian, Xin Cong, Wei Liu, Cheng Yang, Weize Chen, Yusheng Su, Yufan Dang, Jiahao Li, Juyuan Xu, Dahai Li, Zhiyuan Liu, Maosong Sun
- **Paper:** *"Communicative Agents for Software Development"* (arXiv:2307.07924)
- **License:** Apache License 2.0
- **Key Concept Adopted:** Multi-turn collaborative software development and pair-programming handoffs.

### CAMEL
- **Repository:** [`camel-ai/camel`](https://github.com/camel-ai/camel)
- **Authors:** Guohao Li, Hasan Abed Al Kader Hammoud, Hani Itani, Dmitrii Khizbullin, Bernard Ghanem
- **Paper:** *"CAMEL: Communicative Agents for 'Mind' Exploration of Large Scale Language Model Society"* (NeurIPS 2023)
- **License:** Apache License 2.0
- **Key Concept Adopted:** Inception prompting and mutual role-playing task alignment.

---

## 2. Knowledge Graph RAG & Codebase Architecture Exploration

### Graphify
- **Repository:** [`Graphify-Labs/graphify`](https://github.com/Graphify-Labs/graphify)
- **Authors:** Graphify Labs & Contributors
- **License:** MIT License
- **Key Concepts Adopted:**
  - Deterministic knowledge graph RAG with zero embedding cost or vector DB dependencies.
  - Relational provenance distinction (`EXTRACTED` explicit code links vs `INFERRED` specialist deductions).
  - Reverse traversal blast-radius analysis (`affected_nodes` / `affected.py`) to discover direct and indirect dependents when components change.
  - Connected community cluster detection (`cluster.py`) to uncover modular subsystems and architectural hubs.

### Aider
- **Repository:** [`Aider-AI/aider`](https://github.com/Aider-AI/aider)
- **Author:** Paul Gauthier
- **License:** Apache License 2.0
- **Key Concepts Adopted:**
  - Tree-sitter symbol graph representation with definitions and references.
  - Personalized PageRank centrality algorithm to identify core utilities and heavily-depended modules.
  - Token-budgeted compact repo-map formatting for prompt context injection.

---

## 3. Multi-Agent Debate (MAD), Deliberation & Consensus

### LLM Multi-Agent Debate
- **Repository:** [`composable-models/llm_multiagent_debate`](https://github.com/composable-models/llm_multiagent_debate)
- **Authors:** Yilun Du, Shuang Li, Antonio Torralba, Joshua B. Tenenbaum, Igor Mordatch (MIT CSAIL & Google DeepMind)
- **Paper:** *"Improving Factuality and Reasoning in Language Models through Multiagent Debate"* (ICML 2024 / arXiv:2305.14325)
- **License:** MIT License
- **Key Concepts Adopted:**
  - Iterative critique-and-revise rounds where agents observe peer positions and defend or update their stances.
  - Convergence detection across rounds to establish stable consensus and prevent unproductive looping or deadlocks.

### Multi-Agent Debate (Groq/Llama Implementation)
- **Repository:** [`imohitparkash/multi-agent-debate`](https://github.com/imohitparkash/multi-agent-debate)
- **Author:** Mohit Parkash
- **License:** Open Source
- **Key Concept Adopted:** Round-robin adversarial debate prompts and majority synthesis passes.

### Social Choice & Voting Theory
- **Algorithms:** Borda Count (Jean-Charles de Borda, 1781) and Instant-Runoff Ranked-Choice Voting.
- **Application in Gossip:** Confidence-weighted multi-candidate voting to resolve complex architectural trade-offs without binary gridlock.

---

## 4. Adversarial Red-Teaming, Security & Guardrails

### Microsoft PyRIT (Python Risk Identification Tool)
- **Repository:** [`Azure/PyRIT`](https://github.com/Azure/PyRIT)
- **Authors:** Microsoft AI Red Team
- **License:** MIT License
- **Key Concept Adopted:** Automated multi-turn adversarial attack orchestration, risk identification, and proactive vulnerability discovery.

### MultiAgent RedTeam Toolkit
- **Repository:** [`omarmoukawim/MultiAgent-RedTeam-Toolkit`](https://github.com/omarmoukawim/MultiAgent-RedTeam-Toolkit)
- **Author:** Omar Moukawim
- **License:** MIT License
- **Key Concepts Adopted:** Multi-turn attack elicitation against defensive agent boundaries, evaluator scoring, and executive security simulation reports.

### Promptfoo
- **Repository:** [`promptfoo/promptfoo`](https://github.com/promptfoo/promptfoo)
- **Authors:** Promptfoo Inc. & Contributors
- **License:** MIT License
- **Key Concept Adopted:** Automated security assertions, regression testing against prompt injection, and continuous boundary verification.

### NVIDIA NeMo Guardrails
- **Repository:** [`NVIDIA-NeMo/Guardrails`](https://github.com/NVIDIA-NeMo/Guardrails)
- **Authors:** NVIDIA Corporation
- **License:** Apache License 2.0
- **Key Concept Adopted:** Programmable rails, runtime action interception, sensitive path protection, and LLM behavior guardrails.

### STRIDE Threat Modeling
- **Methodology:** Developed by Loren Kohnfelder and Praerit Garg (Microsoft, 1999).
- **Application in Gossip:** Standardized evaluation of multi-agent and software components across:
  - **S**poofing identity
  - **T**ampering with data / state
  - **R**epudiation
  - **I**nformation disclosure
  - **D**enial of service
  - **E**levation of privilege
