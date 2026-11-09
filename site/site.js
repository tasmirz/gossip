const copySetupPromptButton = document.querySelector("#copy-setup-prompt");
const copySetupStatus = document.querySelector("#copy-setup-status");
const setupPrompt = "Set up Gossip using https://github.com/tasmirz/gossip/blob/main/docs/installation/README.md. Download the prebuilt binary for this OS, install the Gossip skill, and configure this agent's MCP using the private project credential I choose. Preserve other settings. If I don't have a project yet, start the loopback dashboard and help me connect this checkout. Keep remote access disabled. Verify the MCP connection.";

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
