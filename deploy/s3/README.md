# Repository S3 với Object Lock

Thay `BUCKET` và `PREFIX` (giá trị `prefix` của repository trong cấu hình) trong các file JSON.

## 1. Tạo bucket

Object Lock chỉ bật được **lúc tạo bucket** (kéo theo versioning):

```sh
aws s3api create-bucket --bucket BUCKET --object-lock-enabled-for-bucket \
  --create-bucket-configuration LocationConstraint=ap-southeast-1
aws s3api put-bucket-lifecycle-configuration --bucket BUCKET --lifecycle-configuration file://lifecycle.json
```

MinIO: `mc mb --with-lock ALIAS/BUCKET`.

Cistern đặt thời hạn khóa cho từng artifact và manifest theo `object_lock: { mode, days }`; không cần
cấu hình default retention cho bucket (nếu có, file khóa job cũng bị khóa nhưng vẫn giải phóng được).

## 2. Chọn chế độ khóa

| Chế độ | Ai xóa được bản đã khóa trước hạn |
| --- | --- |
| `GOVERNANCE` | Người có quyền `s3:BypassGovernanceRetention` (không cấp cho Cistern) |
| `COMPLIANCE` | Không ai, kể cả root, cho tới hết hạn. Khuyến nghị cho production |

Đặt `days` **ngắn hơn** khoảng giữ của quy tắc retention ngắn nhất (vd. `daily: 7` → `days: 7` hoặc ít hơn).
Khóa dài hơn retention không làm prune lỗi, nhưng dữ liệu bị xóa vẫn tính tiền lưu trữ tới hết hạn khóa.

## 3. Prune trên bucket có Object Lock

Bucket có Object Lock luôn có versioning, nên prune (DELETE) chỉ tạo **delete marker**: snapshot biến
khỏi `cistern list`, bản đã khóa vẫn còn và đọc được bằng version ID cho tới hết hạn khóa. Lifecycle
rule trong `lifecycle.json` dọn các bản cũ (`NoncurrentVersionExpiration`, đặt `NoncurrentDays` ≥ `days`),
delete marker mồ côi và multipart upload dở.

## 4. Quyền tối thiểu (NFR-04)

| Credential | Policy | Dùng cho |
| --- | --- | --- |
| Backup | `iam-backup-policy.json` | `backup`, `verify`, `restore`, `check` — **không xóa được snapshot**, chỉ xóa file khóa job |
| Prune | `iam-prune-policy.json` | CronJob `cistern prune` riêng |

Với credential backup tối thiểu:
- đặt `prune_after_backup: false` và chạy prune bằng credential prune;
- backup lỗi không xóa được artifact đã ghi: Cistern ghi cảnh báo, lần prune sau dọn thư mục dở (> 1 giờ).

Credential lấy từ `access_key_id` / `secret_access_key` (/ `session_token`) trong cấu hình, hoặc biến
`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN`. IRSA và instance profile chưa hỗ trợ (FR-25).
