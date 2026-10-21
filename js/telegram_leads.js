/**
 * js/telegram_leads.js
 *
 * Управление "умным радаром лидов" из Telegram
 * Long-Polling каждые N секунд для получения новых лидов
 * Отправка actions на бэкенд
 * Отдельная консоль для логов обработки
 */

/**
 * Логирование в отдельную консоль радара
 */
function logToRadarConsole(message, type = 'info') {
    const console = document.getElementById('telegram-leads-console');
    if (!console) return;

    const time = new Date().toLocaleTimeString('ru-RU');
    const line = document.createElement('div');
    line.className = `tg-console-line tg-console-${type}`;
    line.innerHTML = `<span class="tg-console-timestamp">[${time}]</span> ${message}`;

    console.appendChild(line);

    // Автоскролл вниз
    console.scrollTop = console.scrollHeight;

    // Ограничиваем количество строк (максимум 500)
    const lines = console.querySelectorAll('.tg-console-line');
    if (lines.length > 500) {
        lines[0].remove();
    }
}

class TelegramLeadsManager {
    constructor(options = {}) {
        this.containerSelector = options.containerSelector || '#telegram-leads-container';
        this.statsSelector = options.statsSelector || '#telegram-leads-stats';
        this.pollInterval = options.pollInterval || 5000; // 5 сек по умолчанию
        this.limit = options.limit || 10;

        this.container = null;
        this.statsContainer = null;
        this.currentOffset = 0;
        this.isPolling = false;
        this.currentStatus = 'new'; // Какой статус сейчас показываем

        this.init();
    }

    init() {
        this.container = document.querySelector(this.containerSelector);
        this.statsContainer = document.querySelector(this.statsSelector);

        if (!this.container) {
            console.warn('[TelegramLeads] Container not found:', this.containerSelector);
            logToRadarConsole('❌ Контейнер лидов не найден', 'error');
            return;
        }

        logToRadarConsole('✅ Радар лидов инициализирован', 'success');

        // Запуск первого опроса сразу
        this.poll();
        this.pollStats();

        // И затем регулярный опрос
        setInterval(() => this.poll(), this.pollInterval);
        setInterval(() => this.pollStats(), this.pollInterval);

        // Обработчик для смены статуса фильтра
        this.setupStatusFilter();
    }

    /**
     * Основной опрос лидов с сервера
     */
    async poll() {
        if (this.isPolling) return; // Защита от параллельных запросов
        this.isPolling = true;

        try {
            const response = await fetch(
                `/?action=telegram_leads&limit=${this.limit}&offset=${this.currentOffset}&status=${this.currentStatus}`,
                {
                    method: 'GET',
                    headers: { 'Content-Type': 'application/json' }
                }
            );

            if (!response.ok) throw new Error(`HTTP ${response.status}`);

            const data = await response.json();

            if (data.leads && data.leads.length > 0) {
                logToRadarConsole(`📥 Получено ${data.leads.length} лидов (всего: ${data.total})`, 'info');
                // Рендерим новые лиды в DOM
                this.renderLeads(data.leads);

                // Обновляем offset для пагинации
                this.currentOffset += data.leads.length;
            }
        } catch (error) {
            console.error('[TelegramLeads] Poll error:', error);
            logToRadarConsole(`⚠️ Ошибка при опросе: ${error.message}`, 'error');
        } finally {
            this.isPolling = false;
        }
    }

    /**
     * Опрос статистики лидов
     */
    async pollStats() {
        if (!this.statsContainer) return;

        try {
            const response = await fetch('/?action=telegram_leads_stats', {
                method: 'GET',
                headers: { 'Content-Type': 'application/json' }
            });

            if (!response.ok) throw new Error(`HTTP ${response.status}`);

            const stats = await response.json();
            this.renderStats(stats);
        } catch (error) {
            console.error('[TelegramLeads] Stats error:', error);
        }
    }

    /**
     * Рендеринг новых лидов в DOM
     */
    renderLeads(leads) {
        leads.forEach(lead => {
            const card = this.createLeadCard(lead);
            this.container.appendChild(card);

            // Анимация входа
            requestAnimationFrame(() => {
                card.classList.add('tg-lead-fade-in');
            });
        });
    }

    /**
     * Создание карточки лида
     */
    createLeadCard(lead) {
        const card = document.createElement('div');
        card.className = 'tg-lead-card';
        card.dataset.leadId = lead.id;

        // Формируем URL для ссылки на профиль в ТГ
        const tgProfileUrl = lead.telegram_profile_url || `https://t.me/${lead.user_id}`;
        const displayName = lead.telegram_username ? `@${lead.telegram_username}` : lead.user_id;

        const createdTime = new Date(lead.created_at).toLocaleString('ru-RU', {
            day: 'numeric',
            month: 'short',
            hour: '2-digit',
            minute: '2-digit'
        });

        card.innerHTML = `
            <div class="tg-lead-header">
                <a href="${this.escapeHtml(tgProfileUrl)}" target="_blank" rel="noopener" class="tg-lead-username">
                    ${this.escapeHtml(displayName)}
                </a>
                <time class="tg-lead-time">${createdTime}</time>
            </div>

            <div class="tg-lead-message">
                ${this.escapeHtml(lead.message).substring(0, 300)}${lead.message.length > 300 ? '...' : ''}
            </div>

            <div class="tg-lead-actions">
                <button class="tg-btn tg-btn-replied" data-action="replied" title="Вы ответили на это сообщение">
                    ✓ Ответил
                </button>
                <button class="tg-btn tg-btn-interested" data-action="interested" title="Интересный лид, добавить в CRM">
                    ★ Интересен
                </button>
                <button class="tg-btn tg-btn-spam" data-action="spam" title="Спам или не релевантное сообщение">
                    🚫 Спам
                </button>
            </div>
        `;

        // Обработчики для кнопок действий
        card.querySelectorAll('.tg-btn').forEach(btn => {
            btn.addEventListener('click', (e) => {
                const action = e.target.dataset.action;
                this.handleLeadAction(lead.id, action, card);
            });
        });

        return card;
    }

    /**
     * Обработка действия над лидом (ответил, спам и т.д.)
     */
    async handleLeadAction(leadId, action, cardElement) {
        // Добавляем визуальный feedback
        cardElement.classList.add('tg-lead-processing');

        const actionLabels = {
            'replied': 'Ответил',
            'interested': 'Интересен',
            'spam': 'Спам'
        };
        const actionLabel = actionLabels[action] || action;
        logToRadarConsole(`🔄 Обновление статуса лида #${leadId}: ${actionLabel}...`, 'info');

        try {
            const response = await fetch('/?action=update_telegram_lead', {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json',
                    'X-Postmaker': '1' // CSRF protection
                },
                body: JSON.stringify({
                    lead_id: leadId,
                    status: action
                })
            });

            if (!response.ok) throw new Error(`HTTP ${response.status}`);

            const result = await response.json();

            if (result.status === 'success') {
                logToRadarConsole(`✓ Лид #${leadId} отмечен как "${actionLabel}"`, 'success');
                // Плавное удаление карточки из DOM
                this.removeCardWithAnimation(cardElement);

                // Обновляем статистику
                this.pollStats();
            } else {
                throw new Error(result.message || 'Unknown error');
            }

        } catch (error) {
            console.error('[TelegramLeads] Action error:', error);
            logToRadarConsole(`❌ Ошибка при обновлении: ${error.message}`, 'error');
            cardElement.classList.remove('tg-lead-processing');
            alert('Ошибка: не удалось обновить статус лида');
        }
    }

    /**
     * Плавное удаление карточки с анимацией
     */
    removeCardWithAnimation(cardElement) {
        cardElement.classList.add('tg-lead-fade-out');

        // Удаляем из DOM после завершения анимации (300ms)
        setTimeout(() => {
            if (cardElement.parentNode) {
                cardElement.remove();
            }
        }, 300);
    }

    /**
     * Рендеринг статистики
     */
    renderStats(stats) {
        if (!this.statsContainer) return;

        const html = `
            <div class="tg-stats-item">
                <span class="tg-stats-label">Новых</span>
                <span class="tg-stats-value">${stats.new}</span>
            </div>
            <div class="tg-stats-item">
                <span class="tg-stats-label">Интересных</span>
                <span class="tg-stats-value">${stats.interested}</span>
            </div>
            <div class="tg-stats-item">
                <span class="tg-stats-label">Ответил</span>
                <span class="tg-stats-value">${stats.replied}</span>
            </div>
            <div class="tg-stats-item">
                <span class="tg-stats-label">Спам</span>
                <span class="tg-stats-value">${stats.spam}</span>
            </div>
            <div class="tg-stats-item tg-stats-pending" title="В очереди на обработку Ollama">
                <span class="tg-stats-label">В очереди</span>
                <span class="tg-stats-value">${stats.pending_in_queue}</span>
            </div>
        `;

        this.statsContainer.innerHTML = html;
    }

    /**
     * Установка фильтра по статусу
     */
    setupStatusFilter() {
        const filterButtons = document.querySelectorAll('.tg-leads-filter-btn');
        filterButtons.forEach(btn => {
            btn.addEventListener('click', (e) => {
                const status = e.target.dataset.status;
                this.changeStatus(status);
            });
        });
    }

    /**
     * Смена статуса фильтра
     */
    changeStatus(status) {
        this.currentStatus = status;
        this.currentOffset = 0;
        this.container.innerHTML = '';

        // Обновляем активную кнопку фильтра
        document.querySelectorAll('.tg-leads-filter-btn').forEach(btn => {
            btn.classList.toggle('tg-active', btn.dataset.status === status);
        });

        // Сразу же опрашиваем новые лиды
        this.poll();
    }

    /**
     * Экранирование HTML
     */
    escapeHtml(text) {
        if (!text) return '';
        const div = document.createElement('div');
        div.textContent = String(text);
        return div.innerHTML;
    }
}

// Инициализация при загрузке страницы
document.addEventListener('DOMContentLoaded', () => {
    // Только если контейнер существует (т.е. компонент включен)
    if (document.querySelector('#telegram-leads-container')) {
        window.telegramLeadsManager = new TelegramLeadsManager({
            containerSelector: '#telegram-leads-container',
            statsSelector: '#telegram-leads-stats',
            pollInterval: 5000, // 5 сек
            limit: 10
        });
    }
});
