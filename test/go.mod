module github.com/ishi-o/golem/test

go 1.26

require (
	github.com/cloudwego/eino v0.9.15
	github.com/ishi-o/golem/cmd v0.0.0
	github.com/ishi-o/golem/connector/feishu v0.0.0
	github.com/ishi-o/golem/core v0.0.0
	github.com/ishi-o/golem/internal v0.0.0
	github.com/ishi-o/golem/sandbox/docker v0.0.0
	github.com/ishi-o/golem/sandbox/kubernetes v0.0.0
	github.com/ishi-o/golem/store/mongodb v0.0.0
	github.com/ishi-o/golem/store/redis v0.0.0
	github.com/ishi-o/golem/store/sqlx v0.0.0
	github.com/jmoiron/sqlx v1.4.0
	github.com/mattn/go-sqlite3 v1.14.22
	github.com/stretchr/testify v1.10.0
	go.uber.org/mock v0.4.0
	github.com/redis/go-redis/v9 v9.7.3
	go.mongodb.org/mongo-driver v1.17.4
	k8s.io/api v0.34.1
	k8s.io/apimachinery v0.34.1
	k8s.io/client-go v0.34.1
)
