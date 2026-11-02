// Package kafka là driver "kafka". FR-09: topic, config, offset consumer group; export dữ liệu topic chọn lọc.
package kafka

import "cistern/internal/driver"

func init() {
	driver.Register("kafka", func(p driver.Params) (driver.Driver, error) {
		// TODO: hiện thực driver thật.
		return driver.NewStub("kafka", p), nil
	})
}
