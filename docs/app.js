const examples = {
  telegram: {
    label: "SEND A FILE TO YOUR TELEGRAM CHAT",
    command: "send tg report.pdf",
    description: "Use your configured bot and remembered receiver. Send several files in one command, or add -m for a message."
  },
  whatsapp: {
    label: "SEND A MESSAGE THROUGH WHATSAPP",
    command: 'send wa -m "On my way"',
    description: "Link your own account once. After a successful send, the recipient is remembered for your next message or file."
  },
  http: {
    label: "OPEN A TEMPORARY BROWSER SHARE",
    command: "send http report.pdf",
    description: "Share files and messages through a browser on your local network while the terminal session is open."
  },
  ssh: {
    label: "SEND A FILE TO A REMOTE MACHINE",
    command: "send ssh user@host report.pdf",
    description: "Use your installed OpenSSH client and existing key or agent authentication to transfer a file."
  }
};

const tabs = [...document.querySelectorAll(".command-tab")];
const panel = document.getElementById("command-panel");
const commandLabel = document.getElementById("command-label");
const commandText = document.getElementById("command-text");
const commandDescription = document.getElementById("command-description");
const copyButton = document.getElementById("copy-command");
const copyStatus = document.getElementById("copy-status");

function selectExample(tab, focus = false) {
  const example = examples[tab.dataset.example];
  if (!example) return;
  for (const item of tabs) {
    const active = item === tab;
    item.classList.toggle("is-active", active);
    item.setAttribute("aria-selected", String(active));
    item.tabIndex = active ? 0 : -1;
  }
  panel.setAttribute("aria-labelledby", tab.id);
  commandLabel.textContent = example.label;
  commandText.textContent = example.command;
  commandDescription.textContent = example.description;
  copyButton.firstChild.textContent = "Copy ";
  if (focus) tab.focus();
}

tabs.forEach((tab, index) => {
  tab.addEventListener("click", () => selectExample(tab));
  tab.addEventListener("keydown", (event) => {
    let next = index;
    if (event.key === "ArrowRight" || event.key === "ArrowDown") next = (index + 1) % tabs.length;
    else if (event.key === "ArrowLeft" || event.key === "ArrowUp") next = (index - 1 + tabs.length) % tabs.length;
    else if (event.key === "Home") next = 0;
    else if (event.key === "End") next = tabs.length - 1;
    else return;
    event.preventDefault();
    selectExample(tabs[next], true);
  });
});

copyButton.addEventListener("click", async () => {
  try {
    await navigator.clipboard.writeText(commandText.textContent);
    copyButton.firstChild.textContent = "Copied ";
    copyStatus.textContent = "Command copied to clipboard.";
  } catch {
    copyStatus.textContent = "Clipboard access is unavailable. Select the command to copy it.";
  }
});

document.getElementById("year").textContent = new Date().getFullYear();
document.querySelectorAll(".mobile-menu nav a").forEach((link) => {
  link.addEventListener("click", () => { link.closest("details").open = false; });
});

// Static fallback links stay usable when the GitHub API is unavailable.
const releaseLinks = [...document.querySelectorAll(".download-links a[data-os][data-arch]")];
const releaseBadge = document.getElementById("release-version");
const releaseController = new AbortController();
const releaseTimeout = setTimeout(() => releaseController.abort(), 6000);

fetch("https://api.github.com/repos/binbard/send-cli/releases/latest", {
  headers: { Accept: "application/vnd.github+json" },
  signal: releaseController.signal
}).then((response) => {
  if (!response.ok) throw new Error("No release available");
  return response.json();
}).then((release) => {
  if (!Array.isArray(release.assets)) return;
  const assets = release.assets.filter((asset) =>
    typeof asset.name === "string" &&
    typeof asset.browser_download_url === "string" &&
    asset.browser_download_url.startsWith("https://github.com/binbard/send-cli/releases/download/")
  );
  for (const link of releaseLinks) {
    const extension = link.dataset.os === "windows" ? ".zip" : ".tar.gz";
    const suffix = `-${link.dataset.os}-${link.dataset.arch}${extension}`;
    const asset = assets.find((item) => item.name.endsWith(suffix));
    if (asset) {
      link.href = asset.browser_download_url;
      link.firstChild.textContent = link.firstChild.textContent.replace("Browse", "Download");
    }
  }
  if (typeof release.tag_name === "string" && release.tag_name.length < 32) {
    releaseBadge.textContent = `${release.tag_name} available ↗`;
    if (typeof release.html_url === "string" && release.html_url.startsWith("https://github.com/binbard/send-cli/releases/")) {
      releaseBadge.href = release.html_url;
    }
  }
}).catch(() => {}).finally(() => clearTimeout(releaseTimeout));
