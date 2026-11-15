const copySetupPromptButton = document.querySelector("#copy-setup-prompt");
const copySetupStatus = document.querySelector("#copy-setup-status");
const setupPromptElement = document.querySelector("#setup-prompt-text");
const setupPrompt = setupPromptElement?.textContent.trim() || "Install and configure Gossip from https://github.com/tasmirz/gossip using the LLM setup guide at https://github.com/tasmirz/gossip/blob/main/docs/installation/llm-install.md and agent config reference at https://github.com/tasmirz/gossip/blob/main/docs/installation/agent-configs.md.";

copySetupPromptButton?.addEventListener("click", async () => {
  const label = copySetupPromptButton.querySelector("span");

  try {
    await navigator.clipboard.writeText(setupPrompt);
  } catch {
    const field = document.createElement("textarea");
    field.value = setupPrompt;
    field.className = "clipboard-fallback";
    field.setAttribute("readonly", "");
    document.body.append(field);
    field.select();
    const copied = document.execCommand("copy");
    field.remove();
    if (!copied) {
      copySetupStatus.textContent = "Clipboard unavailable. Open the setup guide instead.";
      return;
    }
  }

  label.textContent = "Copied";
  copySetupStatus.textContent = "Paste it into your coding agent.";
  window.setTimeout(() => {
    label.textContent = "Copy setup prompt";
    copySetupStatus.textContent = "";
  }, 2500);
});
