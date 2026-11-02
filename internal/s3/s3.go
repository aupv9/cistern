// Package s3 là client S3 tự viết: SigV4, put đơn hoặc multipart (part mặc định 16 MiB),
// get, list v2, delete, If-None-Match, Object Lock. Không phụ thuộc SDK.
package s3

// TODO: Client, Sign (SigV4), PutObject, multipart (Abort khi lỗi; phát hiện 200 kèm <Error>).
