# Changelog

## v0.1 — 2026-11-02 · Phase 1 (Lab)

Bản đầu tiên chạy được đầu-cuối: backup PostgreSQL vào repository local, mã hóa bằng age, kiểm chứng,
khôi phục, áp retention, có metrics và cảnh báo. Đã kiểm bằng unit test (`go test -race`) và end-to-end
với PostgreSQL 16 thật trong Docker (`make e2e`). Mã yêu cầu tham chiếu `docs/PRD.md`.

### Tính năng

**Cấu hình**
- FR-01 — Một file YAML khai báo repository, khóa mã hóa và job; thay `${VAR}` / `${VAR:-mặc định}`
  (trừ trong `hooks`); khóa lạ, giá trị sai, retention giữ 0 bản đều bị từ chối khi nạp, kèm vị trí lỗi.

**Backup và driver**
- FR-02 — `cistern backup --job J | --all`; `--all` chạy lần lượt, job lỗi không chặn job sau.
- FR-03 — Driver PostgreSQL: `pg_dump` định dạng custom cho từng database, `pg_dumpall --globals-only`
  cho role/tablespace; chặn khi `pg_dump` cũ hơn server; ghi phiên bản client và server vào manifest.
- FR-14 — Repository local: ghi file tạm rồi rename, snapshot dở không bao giờ trông như hoàn tất.
- FR-19 — Khóa job trong repository; lần chạy trùng báo `job is locked` kèm máy, PID, thời điểm;
  khóa hết hạn (timeout + 10 phút) được gỡ.

**Restore và kiểm chứng**
- FR-16 — `cistern restore` theo `latest`, ID đầy đủ hoặc tiền tố duy nhất; ghi đè tham số bằng `--set`;
  bắt xác nhận (gõ lại tên job hoặc `--yes`). PostgreSQL: từ chối ghi đè database có sẵn trừ khi
  `clean=true`, hỗ trợ `restore_suffix`, `no_owner`, `restore_globals`; xóa database dở do chính nó tạo
  khi restore lỗi.
- FR-17 — `cistern verify`: có private key thì giải mã và kiểm cả hai checksum, không có thì kiểm
  kích thước và checksum bản lưu; `--snapshot all`, `--json`.

**Retention**
- FR-18 — `cistern prune` với GFS (`last`, `hourly`, `daily`, `weekly`, `monthly`, `yearly`) theo
  múi giờ scheduler; `--dry-run`; giữ snapshot mà bản khác phụ thuộc (`depends_on`); dọn snapshot dở
  cũ hơn 1 giờ; `prune_after_backup` chạy trong cùng khóa.

**Quan sát**
- FR-21 (một phần) — `cistern daemon` phục vụ `/metrics`, `/healthz`, `/status`, nạp lại số liệu từ
  repository khi khởi động. **Chưa có lập lịch cron.**
- FR-22 — Metrics `cistern_*` (label `cistern_job`) cho chế độ chạy một lần: Pushgateway (POST theo nhóm,
  giữ thời điểm thành công cũ khi lần sau lỗi) và textfile cho node_exporter.
- FR-23 — Webhook Slack, Teams, Telegram, JSON khi backup/verify/prune/restore lỗi (hoặc cả khi thành
  công); thử lại khi lỗi mạng/5xx; token trong URL không bị ghi ra log.
- `deploy/k8s/prometheus-rules.yaml`: backup đang lỗi, quá 36 giờ không thành công, kích thước giảm hơn
  một nửa, quá 8 ngày chưa verify, verify phát hiện hỏng.

**Bảo mật và toàn vẹn**
- NFR-01 — Mã hóa phía client theo chuẩn age v1 (X25519); máy backup chỉ giữ public key;
  `cistern keygen`. Đã kiểm tương thích hai chiều với CLI `age` 1.2.1.
- NFR-02 — Mật khẩu chỉ đi qua biến môi trường, không xuất hiện trên dòng lệnh, log hay manifest.
- NFR-03 — SHA-256 cho bản gốc và bản lưu; manifest ghi sau cùng nên snapshot chỉ hoàn tất khi có
  manifest; sửa 1 byte bị verify và restore phát hiện.
- NFR-05 — Khôi phục không cần Cistern: `age -d -i key | gunzip | pg_restore` (đã chạy trong e2e).
- NFR-06 — Dump, nén, mã hóa, ghi chạy song song dạng stream, không giữ cả artifact trong RAM.
- NFR-07 — Job lỗi xóa artifact đã ghi, không để lại manifest; timeout mặc định 6 giờ, hết hạn thì
  dừng cả process group.

### Chưa có trong v0.1

| Mục | Mã |
| --- | --- |
| Driver MySQL/MariaDB, MongoDB, Redis, etcd, Elasticsearch, Kafka, RabbitMQ, NATS, S3, exec (đã đăng ký tên, gọi vào báo `not implemented`) | FR-04 – FR-13 |
| Repository S3 / MinIO và Object Lock | FR-15, NFR-04 |
| Hook trước/sau backup và restore | FR-20 |
| Lập lịch cron trong daemon | FR-21 |
| Restore test tự động, IRSA, PITR, Kubernetes operator | FR-24 – FR-27 |
| Docker image và manifest Kubernetes chưa được build/apply thử | NFR-08 |

So với kế hoạch Phase 1 trong PRD (end-to-end với PostgreSQL **và Redis** local): phần Redis chưa làm.
