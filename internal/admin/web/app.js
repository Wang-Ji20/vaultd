// Copy buttons. Everything else on these pages is htmx or plain HTML.
document.addEventListener("click", async (event) => {
  const button = event.target.closest("[data-copy]");
  if (!button) return;
  const source = document.getElementById(button.dataset.copy);
  try {
    await navigator.clipboard.writeText(source.textContent);
    button.textContent = "Copied";
  } catch {
    // The page is plain HTTP on a non-localhost name, or permission was denied.
    getSelection().selectAllChildren(source);
    button.textContent = "Press Ctrl+C";
  }
});
