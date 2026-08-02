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

// Accent picker. The server is authoritative on save: it revalidates the
// colour and recomputes the readable foreground. This only previews the choice
// before the form is submitted, so the same contrast rule is mirrored here.
const accentForm = document.querySelector("[data-accent-form]");
if (accentForm) {
  const input = accentForm.querySelector("[data-accent-input]");
  const swatches = [...accentForm.querySelectorAll("[data-accent-preset]")];

  const channel = value => {
    const v = value / 255;
    return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4);
  };
  const luminance = ([r, g, b]) =>
    0.2126 * channel(r) + 0.7152 * channel(g) + 0.0722 * channel(b);
  const contrast = (a, b) => (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);
  const parse = hex => [1, 3, 5].map(i => parseInt(hex.slice(i, i + 2), 16));
  const mix = (rgb, target, amount) => rgb
    .map((c, i) => Math.round(c + (target[i] - c) * amount))
    .map(c => c.toString(16).padStart(2, "0"))
    .join("");

  const apply = value => {
    const rgb = parse(value);
    const onAccent =
      contrast(luminance(rgb), luminance([0x0a, 0x08, 0x14])) >=
      contrast(luminance(rgb), luminance([255, 255, 255]))
        ? "#0a0814"
        : "#ffffff";
    const root = document.documentElement.style;
    root.setProperty("--accent", value);
    root.setProperty("--accent-strong", "#" + mix(rgb, [255, 255, 255], 0.22));
    root.setProperty("--accent-muted", "#" + mix(rgb, [0x11, 0x15, 0x1b], 0.82));
    root.setProperty("--accent-soft", "#" + mix(rgb, [0x0a, 0x0c, 0x10], 0.9));
    root.setProperty("--on-accent", onAccent);
    for (const code of document.querySelectorAll("[data-accent-code]")) {
      code.textContent = value;
    }
    for (const swatch of swatches) {
      swatch.setAttribute(
        "aria-pressed",
        String(swatch.dataset.accentPreset.toLowerCase() === value.toLowerCase()),
      );
    }
  };

  input.addEventListener("input", () => apply(input.value));
  for (const swatch of swatches) {
    swatch.addEventListener("click", () => {
      input.value = swatch.dataset.accentPreset;
      apply(input.value);
    });
  }
  apply(input.value);
}

// Template builder: show what the selected connection mode does, and reveal
// the VPN-profile requirement only for modes that need one.
const egressSelect = document.querySelector("[data-egress-select]");
if (egressSelect) {
  const description = document.querySelector("[data-egress-description]");
  const descriptions = new Map(
    [...egressSelect.options].map(option => [option.value, option.dataset.description || ""]),
  );
  const sync = () => {
    const option = egressSelect.selectedOptions[0];
    if (description) description.textContent = descriptions.get(option?.value) || "";
  };
  egressSelect.addEventListener("change", sync);
  sync();
}
