/**
 * Roamind Dashboard – Client-side helpers
 * Uses HTMX events for lightweight interactivity.
 */

// ── Toast Notifications ──────────────────────────────────────────────

function showToast(message, type) {
    const container = document.getElementById('toast-container');
    if (!container) return;

    const bgClass = type === 'error' ? 'bg-danger' : 'bg-success';
    const icon = type === 'error' ? 'bi-x-circle-fill' : 'bi-check-circle-fill';

    const toast = document.createElement('div');
    toast.className = `toast show align-items-center text-white ${bgClass} border-0 shadow`;
    toast.setAttribute('role', 'alert');
    toast.innerHTML = `
        <div class="d-flex">
            <div class="toast-body">
                <i class="bi ${icon} me-2"></i>${escapeHtml(message)}
            </div>
            <button type="button" class="btn-close btn-close-white me-2 m-auto"
                    data-bs-dismiss="toast" aria-label="Close"></button>
        </div>
    `;
    container.appendChild(toast);

    // Auto-dismiss after 4 seconds
    setTimeout(() => {
        toast.classList.remove('show');
        setTimeout(() => toast.remove(), 300);
    }, 4000);
}

function escapeHtml(text) {
    const div = document.createElement('div');
    div.textContent = text;
    return div.innerHTML;
}

// ── HTMX Event Handlers ─────────────────────────────────────────────

// Show toast triggered by HX-Trigger header
document.body.addEventListener('showToast', function (evt) {
    const message = evt.detail.value || evt.detail;
    showToast(message, 'success');
});

// Close a Bootstrap modal triggered by HX-Trigger header
document.body.addEventListener('closeModal', function (evt) {
    const modalId = evt.detail.value || evt.detail;
    const modalEl = document.getElementById(modalId);
    if (modalEl) {
        const modal = bootstrap.Modal.getInstance(modalEl);
        if (modal) modal.hide();

        // Reset form inside modal
        const form = modalEl.querySelector('form');
        if (form) form.reset();
    }
});

// Global HTMX error handler – show toast on server errors
document.body.addEventListener('htmx:responseError', function (evt) {
    const xhr = evt.detail.xhr;
    const message = xhr.responseText || 'An unexpected error occurred.';
    showToast(message, 'error');
});

// Handle non-200 responses that HTMX would ignore
document.body.addEventListener('htmx:beforeSwap', function (evt) {
    if (evt.detail.xhr.status >= 400) {
        evt.detail.shouldSwap = false;
        const message = evt.detail.xhr.responseText || 'Request failed.';
        showToast(message, 'error');
    }
});

// ── User ID Persistence ─────────────────────────────────────────────

// After setting user ID, reload to reflect across all components
document.body.addEventListener('htmx:afterRequest', function (evt) {
    if (evt.detail.pathInfo && evt.detail.pathInfo.requestPath === '/web/api/user/set') {
        if (evt.detail.successful) {
            showToast('User ID updated', 'success');
            setTimeout(() => window.location.reload(), 500);
        }
    }
});
