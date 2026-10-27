# Карта кода для защиты

| Файл | Назначение | Что открыть на защите |
|---|---|---|
| `api/main.go` | HTTP-маршруты, Argon2id, opaque-сессии, request ID, Redis fixed-window rate limit/cache, object-level authorization, аудит, транзакционная выдача | `verifyPassword`, `auth`, `rateLimit`, `createApplication`, `application`, `listApplications`, `changeStatus`, `issue`, `getDocument`, `resetConfirm` |
| `db/schema.sql` | DDL, внешние ключи, CHECK/UNIQUE, индексы, поле дедупликации уведомления, триггеры неизменяемости | `documents.application_id UNIQUE`, `trg_documents_immutability`, append-only триггеры, `source_outbox_event_id` |
| `db/roles.sql` | Отдельная ограниченная роль API | `GRANT` и отсутствие `CREATE`, `DROP`, `DELETE` |
| `db/03_worker_role.sh` | Отдельная роль worker с узкими правами | Worker только читает users/applications/outbox, управляет notifications и отмечает outbox published |
| `worker/main.go` | Outbox polling, publish-confirm, durable RabbitMQ, SMTP Mailpit, ACK/NACK и повторная доставка | `publishPending`, `NotifyPublish`, `consume`, `sendMail` |
| `web/index.html` | Структура веб-клиента и доступные формы основных сценариев USER/ADMIN/TECH и восстановления доступа | Формы и элементы `data-action`; inline JavaScript отсутствует |
| `web/app.js` | Клиентские API-вызовы, обработчики форм, управление токеном в памяти и безопасный вывод результатов | `api()`, `login()`, `createApp()`, `changeStatus()`, `issue()`, `recoveryConfirm()`; вывод через `textContent` |
| `web/style.css` | Стили интерфейса, вынесенные отдельно для совместимости со строгой CSP | Адаптивная раскладка и видимые состояния формы |
| `web/nginx.conf` | Веб-обслуживание и проксирование `/api/` к Backend API | `location /api/` — клиент обращается только к API, не к БД |
| `docker-compose.yml` | PostgreSQL, Redis, RabbitMQ, Mailpit, API, worker, nginx web | Состав сервисов, health checks, зависимости, локальная привязка опубликованных портов |
| `requests/requests.http` | Ручная коллекция запросов всех API-маршрутов, включая login/logout/recovery и AI 501 | Последовательно копировать токены и ID из ответов |
| `requests/smoke.sh` | End-to-end проверка живой системы | Чужой объект, mass assignment, idempotency, запрет привилегированной операции, выдача, аудит, logout и DDL-denial |
| `docs/change-log.md` | Журнал расхождений с ЛР № 2 | Каждое изменение имеет причину и оценку влияния |
| `README.md` | Запуск, учётные записи, эндпоинты, контроль безопасности | Пошаговое развёртывание с чистого компьютера |

## Путь обработки запроса

1. `withRequestID` создаёт/нормализует корреляционный UUID; `withSecurityHeaders` задаёт заголовки ответа.
2. `auth` проверяет opaque bearer-сессию в Redis и восстанавливает principal. Роль и user ID не извлекаются из тела клиента.
3. Обработчик проверяет RBAC и object-level scope; поля тела декодируются с `DisallowUnknownFields`.
4. SQL выполняется параметризованно; изменение состояния и юридически значимая выдача фиксируются транзакцией PostgreSQL.
5. Для асинхронного уведомления событие `outbox_events` создаётся в той же транзакции; worker публикует его в RabbitMQ и отправляет SMTP-письмо в Mailpit.
6. Ошибки клиенту возвращаются нейтрально; детали технических ошибок журналируются без передачи stack trace/SQL наружу.

## Важные точки защиты при восстановлении доступа

`resetRequest` возвращает одинаковый публичный ответ для существующего и отсутствующего пользователя и хранит SHA-256 одноразового кода с TTL. `resetConfirm` блокирует строку кода через `FOR UPDATE`, обновляет пароль и использует fail-closed порядок: при ошибке Redis транзакция откатывается, а активные сессии отзываются до commit. Это снижает риск продолжения работы со старым bearer после смены пароля.
