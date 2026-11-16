const escapeText = value => String(value).replace(/[&<>"']/g, character => ({
  '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
})[character])

class OpsQueue extends HTMLElement {
  #records = []
  #selectedId = null
  #filter
  #list
  #count

  connectedCallback() {
    if (this.#filter) return
    this.innerHTML = `
      <div class="queue-head">
        <input type="search" class="queue-filter" aria-label="Filter records" placeholder="Filter records…">
        <span class="queue-count"></span>
      </div>
      <div class="queue-list" role="group" aria-label="Operations queue"></div>`
    this.#filter = this.querySelector('input')
    this.#list = this.querySelector('.queue-list')
    this.#count = this.querySelector('.queue-count')
    this.#filter.addEventListener('input', () => this.#paint())
    this.#list.addEventListener('click', event => {
      const button = event.target.closest('button[data-id]')
      if (!button) return
      this.selectedId = button.dataset.id
      this.dispatchEvent(new CustomEvent('queue-selected', {
        detail: { id: this.selectedId }, bubbles: true,
      }))
    })
    this.#paint()
  }

  set records(records) {
    this.#records = records
    this.#paint()
  }

  get selectedId() { return this.#selectedId }

  set selectedId(id) {
    this.#selectedId = id
    this.#paint()
  }

  #paint() {
    if (!this.#list) return
    const focused = this.#list.contains(document.activeElement) && document.activeElement.dataset.id
    const query = this.#filter.value.trim().toLowerCase()
    const rows = this.#records.filter(record => record.title.toLowerCase().includes(query))
    this.#count.textContent = `${rows.length} / ${this.#records.length}`
    this.#list.innerHTML = rows.map(record => {
      const selected = record.id === this.#selectedId
      return `
        <button type="button" class="queue-row ${selected ? 'is-selected' : ''}"
          aria-pressed="${selected}" data-id="${escapeText(record.id)}">
          <span><b>${escapeText(record.title)}</b><small>${escapeText(record.detail ?? '')}</small></span>
          <i class="status-dot ${escapeText(record.status ?? '')}" aria-hidden="true"></i>
        </button>`
    }).join('') || '<p class="muted empty" role="status">No matching records.</p>'
    if (focused) {
      const row = [...this.#list.querySelectorAll('button')].find(button => button.dataset.id === focused)
      ;(row || this.#filter).focus()
    }
  }
}

class OpsMetric extends HTMLElement {
  static observedAttributes = ['label', 'value', 'delta', 'variant']

  connectedCallback() { this.#render() }
  attributeChangedCallback() { if (this.isConnected) this.#render() }

  #render() {
    this.innerHTML = `<div class="metric">
      <span class="eyebrow">${escapeText(this.getAttribute('label') ?? 'Metric')}</span>
      <strong>${escapeText(this.getAttribute('value') ?? '—')}</strong>
      <small class="${escapeText(this.getAttribute('variant') ?? '')}">${escapeText(this.getAttribute('delta') ?? '')}</small>
    </div>`
  }
}

class OpsDrawer extends HTMLElement {
  #dialog
  #trigger

  connectedCallback() {
    if (this.#dialog) return
    const content = [...this.childNodes]
    this.innerHTML = `<dialog class="ops-dialog">
      <form method="dialog">
        <div class="drawer-content"></div>
        <div class="drawer-actions">
          <button value="cancel" class="ghost">Cancel</button>
          <button value="save" class="primary">Save changes</button>
        </div>
      </form>
    </dialog>`
    this.#dialog = this.querySelector('dialog')
    this.querySelector('.drawer-content').append(...content)
    const heading = this.querySelector('.drawer-content :is(h1, h2, h3)')
    if (heading) {
      heading.id ||= `${this.id}-heading`
      this.#dialog.setAttribute('aria-labelledby', heading.id)
    }
    if (this.hasAttribute('save-label')) {
      this.querySelector('button[value="save"]').textContent = this.getAttribute('save-label')
    }
    this.#dialog.addEventListener('close', () => {
      this.#trigger?.focus()
      if (this.#dialog.returnValue === 'save') {
        this.dispatchEvent(new CustomEvent('drawer-saved', {
          detail: { id: this.id }, bubbles: true,
        }))
      }
    })
  }

  open(trigger = document.activeElement) {
    if (this.#dialog.open) return
    this.#trigger = trigger
    this.#dialog.returnValue = ''
    this.#dialog.showModal()
  }
}

customElements.define('ops-queue', OpsQueue)
customElements.define('ops-metric', OpsMetric)
customElements.define('ops-drawer', OpsDrawer)
