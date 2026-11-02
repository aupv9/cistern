// Package all import mọi driver để chúng tự đăng ký.
package all

import (
	_ "cistern/internal/driver/elasticsearch"
	_ "cistern/internal/driver/etcd"
	_ "cistern/internal/driver/exec"
	_ "cistern/internal/driver/kafka"
	_ "cistern/internal/driver/mongodb"
	_ "cistern/internal/driver/mysql"
	_ "cistern/internal/driver/nats"
	_ "cistern/internal/driver/postgres"
	_ "cistern/internal/driver/rabbitmq"
	_ "cistern/internal/driver/redis"
	_ "cistern/internal/driver/s3"
)
