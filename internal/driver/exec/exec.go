// Package exec là driver "exec". FR-13: chạy lệnh tùy ý cho hệ thống chưa có driver riêng.
package exec

import "cistern/internal/driver"

func init() {
	driver.Register("exec", func(p driver.Params) (driver.Driver, error) {
		// TODO: hiện thực driver thật.
		return driver.NewStub("exec", p), nil
	})
}
