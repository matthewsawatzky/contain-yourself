for (const form of document.querySelectorAll("form[data-confirm]")) {
  form.addEventListener("submit", event => {
    if (!window.confirm(form.dataset.confirm)) event.preventDefault();
  });
}

const dynamicStates = new Set(["creating","pulling-images","creating-storage","starting-vpn","waiting-for-vpn","starting-apps","stopping","deleting"]);
const stateBadge = document.querySelector(".machine-head .status");
if (stateBadge && dynamicStates.has(stateBadge.textContent.trim())) {
  window.setTimeout(() => window.location.reload(), 2500);
}

const templateSelect = document.querySelector("[data-template-select]");
if (templateSelect) {
  const syncApps = () => {
    const option = templateSelect.selectedOptions[0];
    const selected = new Set(option?.dataset.apps.trim().split(/\s+/) || []);
    for (const checkbox of document.querySelectorAll('input[name="apps"]')) {
      checkbox.checked = selected.has(checkbox.value);
    }
    for (const field of document.querySelectorAll("[data-vpn-profile]")) {
      field.hidden = option?.dataset.vpn !== "true";
    }
    const profileSelect = document.querySelector('select[name="vpn_profile_id"]');
    if (profileSelect) profileSelect.required = option?.dataset.vpn === "true";
  };
  templateSelect.addEventListener("change", syncApps);
  syncApps();
}

const copyButton = document.querySelector("[data-copy]");
if (copyButton) {
  copyButton.addEventListener("click", async () => {
    const field = document.querySelector("[data-copy-value]");
    const value = new URL(field.value, window.location.origin).href;
    await navigator.clipboard.writeText(value);
    copyButton.textContent = "Copied";
  });
}

for (const details of document.querySelectorAll("[data-log-url]")) {
  details.addEventListener("toggle", async () => {
    if (!details.open || details.dataset.loaded) return;
    details.dataset.loaded = "true";
    const output = details.querySelector("pre");
    try {
      const response = await fetch(details.dataset.logUrl);
      const body = await response.json();
      output.textContent = response.ok ? (body.logs || "No log output.") : body.error;
    } catch (error) {
      output.textContent = `Unable to load logs: ${error}`;
    }
  });
}
