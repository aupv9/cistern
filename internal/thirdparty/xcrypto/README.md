Bản sao thuần Go (không assembly) của một phần golang.org/x/crypto, lấy từ bản vendored trong
Go toolchain (`$GOROOT/src/vendor/golang.org/x/crypto`). Giấy phép BSD-3: xem LICENSE, PATENTS.

Thay đổi so với gốc: bỏ build constraint (luôn dùng bản generic), đổi đường dẫn import,
`fips140Enforced()` luôn trả về false. Không sửa logic mật mã.
