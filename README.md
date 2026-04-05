### Описание
Проект включает CRUD по пользователям, health-check, автоматическое создание таблицы users при старте и проверку входных данных, включая уникальность email.
API реализует маршруты `POST`, `GET`, `PUT`, `DELETE` по `/api/v1/users` и `GET /health`.

Приложение написано на Go с `gorilla/mux`, а подключение к MariaDB сделано через `go-sql-driver/mysql`

Пример файла `.env`
```ini
APP_PORT=8080
DB_HOST=mariadb
DB_PORT=3306
DB_NAME=usersdb
DB_USER=appuser
DB_PASSWORD=apppassword
```

### Проверки
```bash
curl http://localhost:8080/health

curl -X POST http://localhost:8080/api/v1/users \
  -H "Content-Type: application/json" \
  -d '{"name":"Alice Johnson","email":"alice@example.com","age":29}'

curl http://localhost:8080/api/v1/users

curl http://localhost:8080/api/v1/users/1

curl -X PUT http://localhost:8080/api/v1/users/1 \
  -H "Content-Type: application/json" \
  -d '{"name":"Alice Cooper","email":"alice.cooper@example.com","age":30}'

curl -X DELETE http://localhost:8080/api/v1/users/1
```
