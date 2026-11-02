# Cistern

Công cụ backup hợp nhất cho database, message broker, object storage, etcd và Elasticsearch:
một binary Go, một định dạng snapshot (`manifest.json` + artifact `.gz.age`), một bộ cảnh báo,
restore được kiểm chứng định kỳ.


## Trạng thái

| Phần | Trạng thái |
| --- | --- |
| Cấu hình YAML (`internal/config`) | Xong: tập con YAML, `${VAR}`/`${VAR:-mặc định}`, mặc định, từ chối khóa lạ |
| Mã hóa age v1 (`internal/crypt`) | Xong: tương thích hai chiều với CLI `age` 1.2.1 |
| Pipeline SHA-256 → gzip → age → SHA-256 | Xong, dạng stream |
| Repository local | Xong: ghi file tạm + rename, khóa job bằng `link(2)` |
| Engine: backup, list, verify, restore, check, khóa job | Xong |
| Prune + retention GFS (`--dry-run`, `prune_after_backup`, dọn bản dở > 1 giờ, `depends_on`) | Xong |
| Metrics: `/metrics` của daemon, Pushgateway, textfile; cảnh báo trong `deploy/k8s/prometheus-rules.yaml` | Xong |
| Webhook Slack / Teams / Telegram / JSON | Xong |
| Driver `postgres` | Xong, đã chạy e2e với PostgreSQL 16 |
| CLI: `backup` `restore` `list` `verify` `prune` `check` `keygen` `daemon` | Xong (daemon chưa lập lịch) |
| Repository S3 / MinIO: SigV4, multipart, ghi có điều kiện, Object Lock, tự thử lại | Xong, đã chạy e2e với MinIO |
| Hook pre/post, scheduler của daemon | TODO |
| 10 driver còn lại, IRSA | TODO |

## Bắt đầu

```sh
make build && make test
./bin/cistern keygen --out identity.txt > recipient.txt   # giữ identity.txt ngoài hệ thống backup
export CISTERN_CONFIG=examples/cistern.yaml                # hoặc -c FILE
./bin/cistern check  --job orders-db
./bin/cistern backup --job orders-db
./bin/cistern list   --job orders-db
./bin/cistern verify --job orders-db                      # --snapshot all để kiểm mọi snapshot
./bin/cistern restore --job orders-db --set restore_suffix=_restored --yes
./bin/cistern prune  --job orders-db --dry-run            # xem trước, rồi bỏ --dry-run
```

## Metrics và cảnh báo

Mọi metric mang label `cistern_job` (không dùng `job` vì trùng label của Prometheus/Pushgateway).

| Chế độ | Cách đưa metrics tới Prometheus |
| --- | --- |
| `cistern daemon` | Phục vụ `/metrics`; khi khởi động nạp lại thời điểm backup thành công từ repository |
| Chạy một lần (CronJob, systemd timer) | `metrics.pushgateway_url`: POST nhóm `job=cistern, instance=<job>`; hoặc `metrics.textfile_dir`: ghi `cistern_<job>.prom` cho node_exporter |

Lần chạy lỗi không xóa thời điểm thành công cũ (POST của Pushgateway chỉ thay metric cùng tên; textfile
được gộp với nội dung cũ). Cảnh báo dựa trên gauge thời điểm thay vì counter, vì mỗi lần CronJob là một
process mới.

Webhook gửi khi backup, verify, prune hoặc restore lỗi (mặc định `on: [failure]`, thêm `success` nếu muốn).
Webhook lỗi chỉ được ghi log; URL trong log được cắt bỏ phần path chứa token.

Khôi phục thủ công khi không còn Cistern:

```sh
age -d -i identity.txt <snapshot>/artifacts/db/orders.dump.gz.age | gunzip | pg_restore -d orders
```

End-to-end (Docker): `make e2e` chạy PostgreSQL + repository local và PostgreSQL + MinIO có Object Lock;
`./scripts/e2e-local.sh postgres` hoặc `./scripts/e2e-local.sh s3` để chạy riêng.

Repository S3 với Object Lock (tạo bucket, IAM tối thiểu, lifecycle): xem [deploy/s3/README.md](deploy/s3/README.md).

Yêu cầu: Go ≥ 1.24, chỉ dùng thư viện chuẩn (ChaCha20-Poly1305 sao chép từ x/crypto vào
`internal/thirdparty/xcrypto`). Driver `postgres` cần `pg_dump`, `pg_dumpall`, `pg_restore`, `psql`
có major version ≥ server.

## Cấu trúc

| Đường dẫn | Vai trò |
| --- | --- |
| `cmd/cistern` | CLI: backup, restore, list, verify, prune, check, daemon, drivers, keygen, version |
| `internal/engine` | Điều phối job: khóa, hook, manifest, verify, prune, metrics, webhook |
| `internal/driver` | Interface `Driver`/`Sink`/`Source`, registry |
| `internal/driver/<tên>` | 11 driver: postgres, mysql, mongodb, redis, etcd, elasticsearch, kafka, rabbitmq, nats, s3, exec |
| `internal/pipeline` | SHA-256 → gzip → age → SHA-256 |
| `internal/crypt` | Định dạng age v1 (X25519, ChaCha20-Poly1305) |
| `internal/repo`, `internal/s3` | Repository local và S3 (SigV4 tự viết) |
| `internal/config` | Parser tập con YAML, `${VAR}` |
| `internal/manifest` | Manifest, snapshot ID, bố cục khóa |
| `internal/retention`, `internal/cron` | GFS, biểu thức cron |
| `internal/daemon`, `internal/metrics`, `internal/notify` | Scheduler + HTTP, Prometheus, webhook |
| `deploy/` | Kubernetes (kustomize), Docker Compose, systemd |
